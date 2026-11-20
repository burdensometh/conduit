package data

import (
	sdk "github.com/algorand/go-algorand-sdk/v2/types"

	"github.com/algorand/conduit/conduit/telemetry"
)

// RoundProvider is the interface which all data types sent to Exporters should implement
type RoundProvider interface {
	Round() uint64
	Empty() bool
}

// InitProvider is the interface that can be used when initializing to get common algod related
// variables
type InitProvider interface {
	GetGenesis() *sdk.Genesis
	SetGenesis(*sdk.Genesis)
	NextDBRound() sdk.Round
	GetTelemetryClient() telemetry.Client
}

// AnonymizedProvider is an optional interface an InitProvider may implement. It
// reports whether the data entering the pipeline was already transformed (for
// example a dataset previously written by the anonymizer). Importers that can
// detect transformed input expose it here so processors do not transform it a
// second time.
type AnonymizedProvider interface {
	IsAnonymized() bool
}

// AnonymizedSetter is the optional setter counterpart to AnonymizedProvider,
// used by importers to mark their source as already transformed.
type AnonymizedSetter interface {
	SetAnonymized(bool)
}

// BlockData is provided to the Exporter on each round.
type BlockData struct {

	// BlockHeader is the immutable header from the block
	BlockHeader sdk.BlockHeader `json:"block,omitempty"`

	// Payset is the set of data the block is carrying--can be modified as it is processed
	Payset []sdk.SignedTxnInBlock `json:"payset,omitempty"`

	// Delta contains a list of account changes resulting from the block. Processor plugins may have modify this data.
	Delta *sdk.LedgerStateDelta `json:"delta,omitempty"`

	// Certificate contains voting data that certifies the block. The certificate is non deterministic, a node stops collecting votes once the voting threshold is reached.
	Certificate *map[string]interface{} `json:"cert,omitempty"`

	// Anonymized marks this block as transformed data: irreversible processor plugins
	// (for example the anonymizer) replace address values, so the payload no longer
	// corresponds to the ledger it was imported from. Signatures in the payset no longer
	// verify against the anonymized transactions and the ledger state delta no longer
	// describes the real ledger, so exporters that require either must refuse to consume
	// the data. The flag is carried through the file exporter/importer to prevent
	// transformed data from being mistaken for, or replayed as, original data.
	Anonymized bool `json:"anonymized,omitempty"`
}

// Round returns the round to which the BlockData corresponds
func (blkData BlockData) Round() uint64 {
	return uint64(blkData.BlockHeader.Round)
}

// Empty returns whether the Block contains Txns. Assumes the Block is never nil
func (blkData BlockData) Empty() bool {
	return len(blkData.Payset) == 0
}
