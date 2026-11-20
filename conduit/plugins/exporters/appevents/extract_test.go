package appevents

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdk "github.com/algorand/go-algorand-sdk/v2/types"

	"github.com/algorand/conduit/conduit/data"
)

const testAppID = uint64(1234)
const otherAppID = uint64(5678)

// makeApplTxn builds an application call SignedTxnWithAD with the given logs
// and inner transactions.
func makeApplTxn(appID uint64, logs []string, inner ...sdk.SignedTxnWithAD) sdk.SignedTxnWithAD {
	stxn := sdk.SignedTxnWithAD{
		SignedTxn: sdk.SignedTxn{
			Txn: sdk.Transaction{
				Type: sdk.ApplicationCallTx,
				Header: sdk.Header{
					Sender: sdk.Address{1, 2, 3},
				},
				ApplicationFields: sdk.ApplicationFields{
					ApplicationCallTxnFields: sdk.ApplicationCallTxnFields{
						ApplicationID: sdk.AppIndex(appID),
					},
				},
			},
		},
	}
	stxn.EvalDelta.Logs = logs
	stxn.EvalDelta.InnerTxns = inner
	return stxn
}

// makeBlock builds a block with the given payset.
func makeBlock(round uint64, payset ...sdk.SignedTxnInBlock) data.BlockData {
	return data.BlockData{
		BlockHeader: sdk.BlockHeader{
			Round:       sdk.Round(round),
			GenesisID:   "testnet-v1.0",
			GenesisHash: sdk.Digest{9, 9, 9},
		},
		Payset: payset,
	}
}

func wrapInBlock(stxn sdk.SignedTxnWithAD) sdk.SignedTxnInBlock {
	return sdk.SignedTxnInBlock{SignedTxnWithAD: stxn}
}

func TestExtractLogsTopLevel(t *testing.T) {
	apps := map[uint64]struct{}{testAppID: {}}
	txn := makeApplTxn(testAppID, []string{"log-a", "log-b"})
	blk := makeBlock(7, wrapInBlock(txn))

	records := extractLogs(blk, apps)
	require.Len(t, records, 2)

	assert.Equal(t, uint64(7), records[0].round)
	assert.Equal(t, testAppID, records[0].appID)
	assert.Equal(t, uint64(0), records[0].txnIndex)
	assert.Empty(t, records[0].intraPath)
	assert.Equal(t, uint64(0), records[0].logIndex)
	assert.Equal(t, []byte("log-a"), records[0].raw)
	assert.NotEmpty(t, records[0].txID)

	assert.Equal(t, uint64(1), records[1].logIndex)
	assert.Equal(t, []byte("log-b"), records[1].raw)
	assert.Equal(t, records[0].txID, records[1].txID)
}

func TestExtractLogsInnerTransactions(t *testing.T) {
	apps := map[uint64]struct{}{testAppID: {}}
	// inner-inner txn (depth 2)
	deep := makeApplTxn(testAppID, []string{"deep-log"})
	// two inner txns, the second has children
	inner0 := makeApplTxn(testAppID, []string{"inner-0-log"})
	inner1 := makeApplTxn(otherAppID, []string{"ignored-log"}, deep)
	top := makeApplTxn(otherAppID, nil, inner0, inner1)
	blk := makeBlock(3, wrapInBlock(top))

	records := extractLogs(blk, apps)
	require.Len(t, records, 2)

	// inner txn 0 of top-level txn 0
	assert.Equal(t, []byte("inner-0-log"), records[0].raw)
	assert.Equal(t, []int32{0}, records[0].intraPath)
	assert.Equal(t, uint64(0), records[0].txnIndex)

	// inner txn 0 of inner txn 1 of top-level txn 0
	assert.Equal(t, []byte("deep-log"), records[1].raw)
	assert.Equal(t, []int32{1, 0}, records[1].intraPath)

	// Both belong to the same top-level transaction.
	assert.Equal(t, records[0].txID, records[1].txID)
}

func TestExtractLogsAppFiltering(t *testing.T) {
	apps := map[uint64]struct{}{testAppID: {}}
	watched := makeApplTxn(testAppID, []string{"watched"})
	unwatched := makeApplTxn(otherAppID, []string{"unwatched"})
	payment := sdk.SignedTxnWithAD{
		SignedTxn: sdk.SignedTxn{
			Txn: sdk.Transaction{Type: sdk.PaymentTx},
		},
	}
	blk := makeBlock(1, wrapInBlock(watched), wrapInBlock(unwatched), wrapInBlock(payment))

	records := extractLogs(blk, apps)
	require.Len(t, records, 1)
	assert.Equal(t, []byte("watched"), records[0].raw)
	assert.Equal(t, uint64(0), records[0].txnIndex)
}

func TestExtractLogsAppCreation(t *testing.T) {
	apps := map[uint64]struct{}{testAppID: {}}
	// App creation: ApplicationID is 0 in the txn, the created id is in the
	// apply data.
	creation := makeApplTxn(0, []string{"creation-log"})
	creation.ApplyData.ApplicationID = sdk.AppIndex(testAppID)
	blk := makeBlock(1, wrapInBlock(creation))

	records := extractLogs(blk, apps)
	require.Len(t, records, 1)
	assert.Equal(t, testAppID, records[0].appID)
	assert.Equal(t, []byte("creation-log"), records[0].raw)
}

func TestExtractLogsEmptyBlock(t *testing.T) {
	apps := map[uint64]struct{}{testAppID: {}}
	assert.Empty(t, extractLogs(makeBlock(5), apps))
}

func TestExtractLogsTxnIndexAndTxID(t *testing.T) {
	apps := map[uint64]struct{}{testAppID: {}}
	txn0 := makeApplTxn(testAppID, []string{"first"})
	txn1 := makeApplTxn(testAppID, []string{"second"})
	txn1.Txn.Header.Sender = sdk.Address{4, 5, 6}
	blk := makeBlock(9, wrapInBlock(txn0), wrapInBlock(txn1))

	records := extractLogs(blk, apps)
	require.Len(t, records, 2)
	assert.Equal(t, uint64(0), records[0].txnIndex)
	assert.Equal(t, uint64(1), records[1].txnIndex)
	assert.NotEqual(t, records[0].txID, records[1].txID)
}

func TestBlockTxnIDGenesisReconstruction(t *testing.T) {
	// A transaction whose genesis fields were stripped by the block encoding
	// must still produce the canonical id.
	bh := sdk.BlockHeader{
		Round:       1,
		GenesisID:   "testnet-v1.0",
		GenesisHash: sdk.Digest{9, 9, 9},
	}
	stib := sdk.SignedTxnInBlock{
		SignedTxnWithAD: makeApplTxn(testAppID, nil),
		HasGenesisID:    true,
		HasGenesisHash:  true,
	}
	withGenesis := blockTxnID(bh, &stib)

	stibPlain := sdk.SignedTxnInBlock{SignedTxnWithAD: makeApplTxn(testAppID, nil)}
	withoutGenesis := blockTxnID(bh, &stibPlain)

	assert.NotEmpty(t, withGenesis)
	// The reconstructed id commits to the genesis fields, the stripped one does not.
	assert.NotEqual(t, withGenesis, withoutGenesis)

	// Reconstruction is deterministic.
	assert.Equal(t, withGenesis, blockTxnID(bh, &stib))
}
