// Copyright (c) 2015 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package txscript

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"sync"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/lazybeaver/xorshift"
)

// SigCache implements an ECDSA signature verification cache with a randomized
// entry eviction policy. Only valid signatures will be added to the cache. The
// benefits of SigCache are two fold. Firstly, usage of SigCache mitigates a DoS
// attack wherein an attack causes a victim's client to hang due to worst-case
// behavior triggered while processing attacker crafted invalid transactions. A
// detailed description of the mitigated DoS attack can be found here:
// https://bitslog.wordpress.com/2013/01/23/fixed-bitcoin-vulnerability-explanation-why-the-signature-cache-is-a-dos-protection/.
// Secondly, usage of the SigCache introduces a signature verification
// optimization which speeds up the validation of transactions within a block,
// if they've already been seen and verified within the mempool.
type SigCacheXor struct {
	sync.RWMutex
	validSigs  map[wire.ShaHash]sigCacheEntry
	maxEntries uint

	xorShift xorshift.XorShift
}

// NewSigCache creates and initializes a new instance of SigCache. Its sole
// parameter 'maxEntries' represents the maximum number of entries allowed to
// exist in the SigCache at any particular moment. Random entries are evicted
// to make room for new entries that would cause the number of entries in the
// cache to exceed the max.
func NewSigCacheXor(maxEntries uint) (*SigCacheXor, error) {
	cache := &SigCacheXor{
		validSigs:  make(map[wire.ShaHash]sigCacheEntry),
		maxEntries: maxEntries,
	}

	var seed [8]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return nil, err
	}

	randSeed := binary.BigEndian.Uint64(seed[:])
	cache.xorShift = xorshift.NewXorShift128Plus(randSeed)

	return cache, nil
}

// Exists returns true if an existing entry of 'sig' over 'sigHash' for public
// key 'pubKey' is found within the SigCache. Otherwise, false is returned.
//
// NOTE: This function is safe for concurrent access. Readers won't be blocked
// unless there exists a writer, adding an entry to the SigCache.
func (s *SigCacheXor) Exists(sigHash wire.ShaHash, sig *btcec.Signature, pubKey *btcec.PublicKey) bool {
	s.RLock()
	defer s.RUnlock()

	if entry, ok := s.validSigs[sigHash]; ok {
		return entry.pubKey.Equals(pubKey) && entry.sig.Equals(sig)
	}

	return false
}

// Add adds an entry for a signature over 'sigHash' under public key 'pubKey'
// to the signature cache. In the event that the SigCache is 'full', an
// existing entry is randomly chosen to be evicted in order to make space for
// the new entry.
//
// NOTE: This function is safe for concurrent access. Writers will block
// simultaneous readers until function execution has concluded.
func (s *SigCacheXor) Add(sigHash wire.ShaHash, sig *btcec.Signature, pubKey *btcec.PublicKey) {
	s.Lock()
	defer s.Unlock()

	if s.maxEntries <= 0 {
		return
	}

	// If adding this new entry will put us over the max number of allowed
	// entries, then evict an entry.
	if uint(len(s.validSigs)+1) > s.maxEntries {
		// Generate a random hash.
		var randHashBytes [wire.HashSize]byte
		for i := 0; i < 4; i++ {
			randInt := s.xorShift.Next()
			binary.BigEndian.PutUint64(randHashBytes[i*8:], randInt)
		}

		// Try to find the first entry that is greater than the random
		// hash. Use the first entry (which is already pseudo random due
		// to Go's range statement over maps) as a fall back if none of
		// the hashes in the rejected transactions pool are larger than
		// the random hash.
		var foundEntry wire.ShaHash
		var zeroEntry wire.ShaHash
		for sigEntry := range s.validSigs {
			if foundEntry == zeroEntry {
				foundEntry = sigEntry
			}
			if bytes.Compare(sigEntry[:], randHashBytes[:]) > 0 {
				foundEntry = sigEntry
				break
			}
		}
		delete(s.validSigs, foundEntry)
	}

	s.validSigs[sigHash] = sigCacheEntry{sig, pubKey}
}