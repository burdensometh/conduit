package appevents

import (
	"github.com/algorand/go-algorand-sdk/v2/crypto"
	sdk "github.com/algorand/go-algorand-sdk/v2/types"
	iutil "github.com/algorand/indexer/v3/util"

	"github.com/algorand/conduit/conduit/data"
)

// logRecord is a single application log line together with its provenance:
// where in the block it came from and which application emitted it.
type logRecord struct {
	// round is the block round.
	round uint64
	// appID is the id of the application that emitted the log.
	appID uint64
	// txnIndex is the index of the top-level transaction in the block payset.
	txnIndex uint64
	// intraPath is the path of inner transaction indexes leading to the
	// transaction that emitted the log. Empty for top-level transactions.
	intraPath []int32
	// logIndex is the index of the log within the transaction's log list.
	logIndex uint64
	// txID is the id of the top-level transaction.
	txID string
	// raw holds the original log bytes.
	raw []byte
}

// extractLogs walks the payset of a block, including all nested inner
// transactions, and returns every log emitted by one of the watched
// applications, in payset order.
func extractLogs(blk data.BlockData, apps map[uint64]struct{}) []logRecord {
	var records []logRecord
	for i := range blk.Payset {
		stib := &blk.Payset[i]
		txID := blockTxnID(blk.BlockHeader, stib)
		walkTxnLogs(&stib.SignedTxnWithAD, blk.Round(), uint64(i), nil, txID, apps, &records)
	}
	return records
}

// walkTxnLogs appends the logs of a single (possibly inner) transaction and
// recursively walks its inner transactions.
func walkTxnLogs(stxn *sdk.SignedTxnWithAD, round uint64, txnIndex uint64, path []int32, txID string, apps map[uint64]struct{}, records *[]logRecord) {
	if stxn.Txn.Type == sdk.ApplicationCallTx {
		// Logs are attributed to the application being called. For app
		// creation calls the id is found in the apply data.
		appID := uint64(stxn.Txn.ApplicationID)
		if appID == 0 {
			applyData := stxn.ApplyData
			appID = uint64(applyData.ApplicationID)
		}
		if _, ok := apps[appID]; ok {
			for j := range stxn.EvalDelta.Logs {
				recordPath := make([]int32, len(path))
				copy(recordPath, path)
				*records = append(*records, logRecord{
					round:     round,
					appID:     appID,
					txnIndex:  txnIndex,
					intraPath: recordPath,
					logIndex:  uint64(j),
					txID:      txID,
					raw:       []byte(stxn.EvalDelta.Logs[j]),
				})
			}
		}
	}
	for k := range stxn.EvalDelta.InnerTxns {
		walkTxnLogs(&stxn.EvalDelta.InnerTxns[k], round, txnIndex, append(path, int32(k)), txID, apps, records)
	}
}

// blockTxnID computes the canonical id of a top-level transaction. The
// genesis fields stripped from the block encoding are restored first so the
// id matches the one produced by algod and the SDK.
func blockTxnID(bh sdk.BlockHeader, stib *sdk.SignedTxnInBlock) string {
	stxn, _, err := iutil.DecodeSignedTxn(bh, *stib)
	if err != nil {
		// Fall back to a best effort reconstruction. This happens for
		// synthetic blocks whose protocol is not part of the consensus
		// parameters, e.g. in tests.
		stxn = stib.SignedTxn
		if stib.HasGenesisID {
			stxn.Txn.GenesisID = bh.GenesisID
		}
		if stib.HasGenesisHash {
			stxn.Txn.GenesisHash = bh.GenesisHash
		}
	}
	return crypto.GetTxID(stxn.Txn)
}
