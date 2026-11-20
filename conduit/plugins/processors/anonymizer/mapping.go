package anonymizer

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"sync"

	sdk "github.com/algorand/go-algorand-sdk/v2/types"
)

// mappingVersion is mixed into the HMAC domain so that changing the derivation
// algorithm does not silently recycle old pseudonyms.
const mappingVersion = "conduit-anonymizer/address/v1"

// addressMapper deterministically maps an Algorand account address to a
// pseudonymous Algorand account address.
//
// The same input address always maps to the same output address for a given
// dataset key, so equality of accounts is preserved across transactions,
// transaction roles (sender, receiver, close-to, rekey, ...), inner
// transactions, blocks, and the ledger state delta.
//
// The derivation is a keyed HMAC-SHA-256 over the raw 32 address bytes, so the
// key cannot be recovered from the output and the output is a normal 32-byte
// Algorand address (with a valid checksum when rendered with Address.String).
type addressMapper struct {
	key []byte

	mu    sync.Mutex
	cache map[sdk.Address]sdk.Address
}

func newAddressMapper(key []byte) *addressMapper {
	k := make([]byte, len(key))
	copy(k, key)
	return &addressMapper{
		key:   k,
		cache: make(map[sdk.Address]sdk.Address),
	}
}

// mapAddress returns the pseudonym for addr. The zero address is a sentinel in
// the Algorand data model (it means "field not set"): it is returned unchanged
// so that the meaning of optional address fields is preserved.
func (m *addressMapper) mapAddress(addr sdk.Address) sdk.Address {
	if addr.IsZero() {
		return addr
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if mapped, ok := m.cache[addr]; ok {
		return mapped
	}

	var mapped sdk.Address
	var counter uint64
	for {
		sum := m.digest(addr, counter)
		copy(mapped[:], sum)
		// A pseudonym must never collide with the zero sentinel (which would
		// change the field's meaning) or with its own input (which would leak
		// the original address). Both cases are astronomically unlikely; the
		// counter makes the loop terminate deterministically.
		if !mapped.IsZero() && mapped != addr {
			break
		}
		counter++
	}

	m.cache[addr] = mapped
	return mapped
}

func (m *addressMapper) digest(addr sdk.Address, counter uint64) []byte {
	mac := hmac.New(sha256.New, m.key)
	mac.Write([]byte(mappingVersion))
	var zeroPrefix [1]byte
	mac.Write(zeroPrefix[:])
	var ctr [8]byte
	binary.BigEndian.PutUint64(ctr[:], counter)
	mac.Write(ctr[:])
	mac.Write(addr[:])
	return mac.Sum(nil)
}

// mapAddresses returns a new slice with each address mapped. Nil and empty
// slices keep their kind so encoding decisions are unaffected.
func (m *addressMapper) mapAddresses(addresses []sdk.Address) []sdk.Address {
	if addresses == nil {
		return nil
	}
	out := make([]sdk.Address, len(addresses))
	for i := range addresses {
		out[i] = m.mapAddress(addresses[i])
	}
	return out
}
