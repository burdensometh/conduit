package anonymizer

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdk "github.com/algorand/go-algorand-sdk/v2/types"

	"github.com/algorand/conduit/conduit/data"
)

var sdkAddressType = reflect.TypeOf(sdk.Address{})

// visitSettableAddresses walks v and invokes fn on every settable value whose
// type is sdk.Address, including addresses nested in structs, pointers, slices,
// map keys and map values. Unexported fields are skipped (the SDK uses them only
// for codec options and caches). It is used to fill a fixture with a sentinel
// and to inspect a transformed result without enumerating fields by hand.
func visitSettableAddresses(v reflect.Value, fn func(reflect.Value)) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return
		}
		visitSettableAddresses(v.Elem(), fn)
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		// Interfaces are not address-typed in BlockData; ignore.
	case reflect.Struct:
		structType := v.Type()
		for i := 0; i < v.NumField(); i++ {
			if structType.Field(i).PkgPath != "" {
				continue // unexported
			}
			field := v.Field(i)
			if field.Type() == sdkAddressType {
				if field.CanSet() {
					fn(field)
				}
				continue
			}
			visitSettableAddresses(field, fn)
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem() == sdkAddressType {
			for i := 0; i < v.Len(); i++ {
				if v.Index(i).CanSet() {
					fn(v.Index(i))
				}
			}
			return
		}
		for i := 0; i < v.Len(); i++ {
			visitSettableAddresses(v.Index(i), fn)
		}
	case reflect.Map:
		if !v.CanSet() {
			// Rebuild the map so entries become settable.
			rebuilt := rebuildMap(v, fn)
			if v.CanSet() {
				v.Set(rebuilt)
			}
			return
		}
		v.Set(rebuildMap(v, fn))
	}
}

// rebuildMap returns a new map with fn applied to every address nested in keys
// and values.
func rebuildMap(v reflect.Value, fn func(reflect.Value)) reflect.Value {
	out := reflect.MakeMapWithSize(v.Type(), v.Len())
	iter := v.MapRange()
	for iter.Next() {
		key := reflect.New(v.Type().Key()).Elem()
		key.Set(iter.Key())
		visitSettableAddresses(key, fn)

		value := reflect.New(v.Type().Elem()).Elem()
		value.Set(iter.Value())
		visitSettableAddresses(value, fn)

		out.SetMapIndex(key, value)
	}
	return out
}

// fillAddresses replaces every address in the block with sentinel.
func fillAddresses(block *data.BlockData, sentinel sdk.Address) {
	visitSettableAddresses(reflect.ValueOf(block).Elem(), func(field reflect.Value) {
		field.Set(reflect.ValueOf(sentinel))
	})
}

// collectAddressFields returns all address values found in the block.
func collectAddressFields(block *data.BlockData) []sdk.Address {
	var addresses []sdk.Address
	visitSettableAddresses(reflect.ValueOf(block).Elem(), func(field reflect.Value) {
		addresses = append(addresses, field.Interface().(sdk.Address))
	})
	return addresses
}

// fullFixture builds a block that populates every address-bearing field with
// the same sentinel address, so the reflection-based test below can prove the
// transformer covers all of them.
func fullFixture(t *testing.T, sentinel sdk.Address) data.BlockData {
	t.Helper()
	block := data.BlockData{
		BlockHeader: sdk.BlockHeader{
			Round:    5,
			Proposer: sentinel,
			RewardsState: sdk.RewardsState{
				FeeSink:     sentinel,
				RewardsPool: sentinel,
			},
			ParticipationUpdates: sdk.ParticipationUpdates{
				ExpiredParticipationAccounts: []sdk.Address{sentinel},
				AbsentParticipationAccounts:  []sdk.Address{sentinel},
			},
		},
		Payset: []sdk.SignedTxnInBlock{{
			SignedTxnWithAD: sdk.SignedTxnWithAD{
				SignedTxn: sdk.SignedTxn{
					AuthAddr: sentinel,
					Txn: sdk.Transaction{
						Type: sdk.ApplicationCallTx,
						Header: sdk.Header{
							Sender:  sentinel,
							RekeyTo: sentinel,
						},
						PaymentTxnFields: sdk.PaymentTxnFields{
							Receiver:         sentinel,
							CloseRemainderTo: sentinel,
						},
						AssetConfigTxnFields: sdk.AssetConfigTxnFields{
							AssetParams: sdk.AssetParams{
								Manager:  sentinel,
								Reserve:  sentinel,
								Freeze:   sentinel,
								Clawback: sentinel,
							},
						},
						AssetTransferTxnFields: sdk.AssetTransferTxnFields{
							AssetSender:   sentinel,
							AssetReceiver: sentinel,
							AssetCloseTo:  sentinel,
						},
						AssetFreezeTxnFields: sdk.AssetFreezeTxnFields{
							FreezeAccount: sentinel,
						},
						ApplicationFields: sdk.ApplicationFields{
							ApplicationCallTxnFields: sdk.ApplicationCallTxnFields{
								Accounts: []sdk.Address{sentinel},
								Access:   []sdk.ResourceRef{{Address: sentinel}},
							},
						},
						HeartbeatTxnFields: &sdk.HeartbeatTxnFields{HbAddress: sentinel},
					},
				},
				ApplyData: sdk.ApplyData{
					EvalDelta: sdk.EvalDelta{
						SharedAccts: []sdk.Address{sentinel},
						InnerTxns: []sdk.SignedTxnWithAD{{
							SignedTxn: sdk.SignedTxn{
								AuthAddr: sentinel,
								Txn:      sdk.Transaction{Type: sdk.PaymentTx, Header: sdk.Header{Sender: sentinel}},
							},
						}},
					},
				},
			},
		}},
		Delta: &sdk.LedgerStateDelta{
			Accts: sdk.AccountDeltas{
				Accts: []sdk.BalanceRecord{{
					Addr: sentinel,
					AccountData: sdk.AccountData{
						AccountBaseData: sdk.AccountBaseData{AuthAddr: sentinel},
					},
				}},
				AppResources: []sdk.AppResourceRecord{{
					Addr:   sentinel,
					Params: sdk.AppParamsDelta{Params: &sdk.AppParams{SizeSponsor: sentinel}},
				}},
				AssetResources: []sdk.AssetResourceRecord{{
					Addr: sentinel,
					Params: sdk.AssetParamsDelta{Params: &sdk.AssetParams{
						Manager:  sentinel,
						Reserve:  sentinel,
						Freeze:   sentinel,
						Clawback: sentinel,
					}},
				}},
			},
			Txleases: map[sdk.Txlease]sdk.Round{
				{Sender: sentinel}: 10,
			},
			Creatables: map[sdk.CreatableIndex]sdk.ModifiedCreatable{
				1: {Creator: sentinel},
			},
			Hdr: &sdk.BlockHeader{
				Proposer: sentinel,
				RewardsState: sdk.RewardsState{
					FeeSink:     sentinel,
					RewardsPool: sentinel,
				},
			},
		},
	}
	return block
}

// TestEveryAddressFieldIsTransformed fills every address-typed field with the
// same sentinel, transforms the block, then proves via reflection that every
// address field now holds the pseudonym. This catches any address field the
// transformer might have missed, without relying on a hand-written field list.
func TestEveryAddressFieldIsTransformed(t *testing.T) {
	transformer := newTestTransformer(t, nil)

	sentinel := mustDecodeAddress(t, "NVCAFYNKJL2NGAIZHWLIKI6HGMTLYXL7BXPBO7NXX4A7GMMWKNFKFKDKP4")
	expected := transformer.mapper.mapAddress(sentinel)
	require.NotEqual(t, sentinel, expected)

	block := fullFixture(t, sentinel)
	// Fill any address field the fixture left at the zero value, so the check
	// below covers every address-typed field the SDK defines.
	fillAddresses(&block, sentinel)
	before := collectAddressFields(&block)
	require.NotEmpty(t, before)

	out, err := (&Anonymizer{transformer: transformer}).Process(block)
	require.NoError(t, err)

	after := collectAddressFields(&out)
	require.Equal(t, len(before), len(after))
	for i := range after {
		assert.Equal(t, expected, after[i], "address field %d was not transformed to the pseudonym", i)
	}

	// The input remains untouched.
	for i, address := range collectAddressFields(&block) {
		assert.Equal(t, before[i], address)
	}
}

// TestNonAddressDigestsPreserved proves 32-byte values that are not typed as an
// address are bit-for-bit preserved.
func TestNonAddressDigestsPreserved(t *testing.T) {
	transformer := newTestTransformer(t, nil)
	address := mustDecodeAddress(t, "NVCAFYNKJL2NGAIZHWLIKI6HGMTLYXL7BXPBO7NXX4A7GMMWKNFKFKDKP4")

	var digest sdk.Digest
	for i := range digest {
		digest[i] = byte(0xA0 + i)
	}
	var lease [32]byte
	copy(lease[:], digest[:])
	txID := sdk.Txid(digest)

	block := data.BlockData{
		BlockHeader: sdk.BlockHeader{
			Round:       9,
			Branch:      sdk.BlockHash(digest),
			Seed:        sdk.Seed(digest),
			GenesisHash: digest,
		}, Payset: []sdk.SignedTxnInBlock{{
			SignedTxnWithAD: sdk.SignedTxnWithAD{
				SignedTxn: sdk.SignedTxn{
					Txn: sdk.Transaction{
						Type:   sdk.PaymentTx,
						Header: sdk.Header{Sender: address, Group: digest, Lease: lease},
					},
				},
			},
		}},
		Delta: &sdk.LedgerStateDelta{
			Txids: map[sdk.Txid]sdk.IncludedTransactions{txID: {LastValid: 3}},
		},
	}

	out, err := (&Anonymizer{transformer: transformer}).Process(block)
	require.NoError(t, err)

	assert.Equal(t, sdk.BlockHash(digest), out.BlockHeader.Branch)
	assert.Equal(t, sdk.Seed(digest), out.BlockHeader.Seed)
	assert.Equal(t, digest, out.BlockHeader.GenesisHash)
	assert.Equal(t, digest, out.Payset[0].Txn.Group)
	assert.Equal(t, lease, out.Payset[0].Txn.Lease)
	assert.Contains(t, out.Delta.Txids, txID)
}
