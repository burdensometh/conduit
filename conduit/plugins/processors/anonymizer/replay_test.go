package anonymizer_test

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"sort"
	"testing"

	logrustest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdk "github.com/algorand/go-algorand-sdk/v2/types"

	"github.com/algorand/conduit/conduit"
	"github.com/algorand/conduit/conduit/data"
	"github.com/algorand/conduit/conduit/plugins"
	"github.com/algorand/conduit/conduit/plugins/exporters"
	"github.com/algorand/conduit/conduit/plugins/exporters/filewriter"
	"github.com/algorand/conduit/conduit/plugins/importers"
	"github.com/algorand/conduit/conduit/plugins/processors"

	_ "github.com/algorand/conduit/conduit/plugins/importers/filereader"
	_ "github.com/algorand/conduit/conduit/plugins/processors/anonymizer"
)

const (
	testBlocksDir  = "../../importers/filereader/test_resources/filereader_blocks"
	testPattern    = "%[1]d_block.msgp.gz"
	testDatasetKey = "replay-dataset-key"
	testLastRound  = 9
)

func readSourceBlock(t *testing.T, round uint64) data.BlockData {
	t.Helper()
	var block data.BlockData
	filename := path.Join(testBlocksDir, fmt.Sprintf(testPattern, round))
	require.NoError(t, filewriter.DecodeFromFile(filename, &block, filewriter.MessagepackFormat, true))
	return block
}

func readSourceGenesis(t *testing.T) *sdk.Genesis {
	t.Helper()
	genesis := &sdk.Genesis{}
	require.NoError(t, filewriter.DecodeFromFile(path.Join(testBlocksDir, filewriter.GenesisFilename), genesis, filewriter.JSONFormat, false))
	return genesis
}

// collectAddresses returns every SDK address-typed value in a block, in a
// deterministic order. This mirrors the set of fields the anonymizer rewrites.
func collectAddresses(block data.BlockData) []sdk.Address {
	var addresses []sdk.Address
	add := func(address sdk.Address) {
		if !address.IsZero() {
			addresses = append(addresses, address)
		}
	}
	addAll := func(list []sdk.Address) {
		for _, address := range list {
			add(address)
		}
	}
	addTxn := func(txn sdk.Transaction) {
		add(txn.Sender)
		add(txn.RekeyTo)
		add(txn.Receiver)
		add(txn.CloseRemainderTo)
		add(txn.AssetParams.Manager)
		add(txn.AssetParams.Reserve)
		add(txn.AssetParams.Freeze)
		add(txn.AssetParams.Clawback)
		add(txn.AssetSender)
		add(txn.AssetReceiver)
		add(txn.AssetCloseTo)
		add(txn.FreezeAccount)
		addAll(txn.Accounts)
		for _, access := range txn.Access {
			add(access.Address)
		}
		if txn.HeartbeatTxnFields != nil {
			add(txn.HeartbeatTxnFields.HbAddress)
		}
	}
	var visit func(stxn sdk.SignedTxnWithAD)
	visit = func(stxn sdk.SignedTxnWithAD) {
		add(stxn.AuthAddr)
		addTxn(stxn.Txn)
		addAll(stxn.EvalDelta.SharedAccts)
		for _, inner := range stxn.EvalDelta.InnerTxns {
			visit(inner)
		}
	}

	add(block.BlockHeader.Proposer)
	add(block.BlockHeader.FeeSink)
	add(block.BlockHeader.RewardsPool)
	addAll(block.BlockHeader.ExpiredParticipationAccounts)
	addAll(block.BlockHeader.AbsentParticipationAccounts)
	for _, stxn := range block.Payset {
		visit(stxn.SignedTxnWithAD)
	}
	if block.Delta != nil {
		for _, record := range block.Delta.Accts.Accts {
			add(record.Addr)
			add(record.AuthAddr)
		}
		for _, record := range block.Delta.Accts.AppResources {
			add(record.Addr)
			if record.Params.Params != nil {
				add(record.Params.Params.SizeSponsor)
			}
		}
		for _, record := range block.Delta.Accts.AssetResources {
			add(record.Addr)
			if record.Params.Params != nil {
				add(record.Params.Params.Manager)
				add(record.Params.Params.Reserve)
				add(record.Params.Params.Freeze)
				add(record.Params.Params.Clawback)
			}
		}
		for _, lease := range sortedTxleases(block.Delta.Txleases) {
			add(lease.Sender)
		}
		for _, index := range sortedCreatableIndices(block.Delta.Creatables) {
			add(block.Delta.Creatables[index].Creator)
		}
	}
	return addresses
}

// sortedTxleases returns the leases in a deterministic order. The lease bytes
// are preserved by anonymization, so this order is stable between a source
// block and its anonymized counterpart.
func sortedTxleases(leases map[sdk.Txlease]sdk.Round) []sdk.Txlease {
	keys := make([]sdk.Txlease, 0, len(leases))
	for lease := range leases {
		keys = append(keys, lease)
	}
	sort.Slice(keys, func(i, j int) bool {
		return bytes.Compare(keys[i].Lease[:], keys[j].Lease[:]) < 0
	})
	return keys
}

// sortedCreatableIndices returns creatable indices in ascending order; indices
// are preserved by anonymization.
func sortedCreatableIndices(creatables map[sdk.CreatableIndex]sdk.ModifiedCreatable) []sdk.CreatableIndex {
	indices := make([]sdk.CreatableIndex, 0, len(creatables))
	for index := range creatables {
		indices = append(indices, index)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	return indices
}

func addressStrings(addresses []sdk.Address) []string {
	out := make([]string, len(addresses))
	for i, address := range addresses {
		out[i] = address.String()
	}
	return out
}

func newProcessor(t *testing.T, name string) processors.Processor {
	t.Helper()
	constructor, err := processors.ProcessorConstructorByName(name)
	require.NoError(t, err)
	return constructor.New()
}

// TestFileReplayPreservesEqualityAndHidesAddresses runs the anonymizer over the
// real test blocks, writes them with the file_writer, replays them with the
// file_reader, and verifies that:
//   - no original address survives in the replayed dataset,
//   - equal addresses remain equal and distinct addresses remain distinct,
//   - the replayed data is still marked as transformed, and
//   - re-processing replayed data does not remap the pseudonyms again.
func TestFileReplayPreservesEqualityAndHidesAddresses(t *testing.T) {
	ctx := context.Background()
	logger, _ := logrustest.NewNullLogger()

	round := sdk.Round(0)
	provider := conduit.MakePipelineInitProvider(&round, nil, nil)

	// --- Anonymize the source data. ---
	anonymizer := newProcessor(t, "anonymizer")
	genesis := readSourceGenesis(t)
	provider.SetGenesis(genesis)
	require.NoError(t, anonymizer.Init(ctx, provider, plugins.MakePluginConfig("dataset-key: "+testDatasetKey), logger))
	anonymizedGenesis := provider.GetGenesis()

	exporterDir := t.TempDir()
	exporterConstructor, err := exporters.ExporterConstructorByName("file_writer")
	require.NoError(t, err)
	exporter := exporterConstructor.New()
	exporterCfg := plugins.MakePluginConfig(fmt.Sprintf("block-dir: %s\nfilename-pattern: '%s'", exporterDir, testPattern))
	require.NoError(t, exporter.Init(ctx, provider, exporterCfg, logger))

	// mapping records original -> anonymized for the whole dataset
	mapping := map[string]string{}
	originalAddresses := map[string]bool{}
	for rnd := uint64(0); rnd <= testLastRound; rnd++ {
		source := readSourceBlock(t, rnd)
		out, err := anonymizer.Process(source)
		require.NoError(t, err)
		require.True(t, out.Anonymized, "round %d must be marked as transformed", rnd)

		sourceAddresses := collectAddresses(source)
		outAddresses := collectAddresses(out)
		require.Equal(t, len(sourceAddresses), len(outAddresses), "round %d: address count changed", rnd)

		for i := range sourceAddresses {
			original := sourceAddresses[i].String()
			anonymized := outAddresses[i].String()
			originalAddresses[original] = true
			if previous, ok := mapping[original]; ok {
				require.Equal(t, previous, anonymized, "round %d: %s mapped inconsistently", rnd, original)
			}
			mapping[original] = anonymized
		}

		require.NoError(t, exporter.Receive(out))
	}
	require.NoError(t, exporter.Close())
	require.NotEmpty(t, mapping)

	// The mapping is injective, and never maps an address to itself.
	pseudonymOwner := map[string]string{}
	for original, anonymized := range mapping {
		assert.NotEqual(t, original, anonymized, "address %s was left unchanged", original)
		if owner, ok := pseudonymOwner[anonymized]; ok {
			assert.Failf(t, "collision", "%s and %s both map to %s", original, owner, anonymized)
		}
		pseudonymOwner[anonymized] = original
	}

	// --- Replay with the file_reader. ---
	importerConstructor, err := importers.ImporterConstructorByName("file_reader")
	require.NoError(t, err)
	reader := importerConstructor.New()
	readerCfg := plugins.MakePluginConfig(fmt.Sprintf("block-dir: %s\nfilename-pattern: '%s'", exporterDir, testPattern))
	replayProvider := conduit.MakePipelineInitProvider(&round, nil, nil)
	require.NoError(t, reader.Init(ctx, replayProvider, readerCfg, logger))
	require.True(t, replayProvider.IsAnonymized(), "file_reader must detect the transformed marker")

	replayedGenesis, err := reader.GetGenesis()
	require.NoError(t, err)
	assert.Equal(t, anonymizedGenesis.FeeSink, replayedGenesis.FeeSink)
	assert.Equal(t, anonymizedGenesis.RewardsPool, replayedGenesis.RewardsPool)
	assert.NotEqual(t, genesis.FeeSink, replayedGenesis.FeeSink, "genesis fee sink must be anonymized on disk")

	// An anonymizer downstream of an already-transformed source must pass data
	// through, so pseudonyms are not remapped a second time.
	replayAnonymizer := newProcessor(t, "anonymizer")
	require.NoError(t, replayAnonymizer.Init(ctx, replayProvider, plugins.MakePluginConfig("dataset-key: "+testDatasetKey), logger))

	for rnd := uint64(0); rnd <= testLastRound; rnd++ {
		replayed, err := reader.GetBlock(rnd)
		require.NoError(t, err)
		assert.True(t, replayed.Anonymized, "round %d lost its transformed marker", rnd)

		reprocessed, err := replayAnonymizer.Process(replayed)
		require.NoError(t, err)
		assert.Equal(t, addressStrings(collectAddresses(replayed)), addressStrings(collectAddresses(reprocessed)),
			"round %d was re-anonymized on replay", rnd)

		// Equality is preserved: the replayed pseudonyms must be exactly those
		// the first pass produced for the same source round.
		sourceAddresses := collectAddresses(readSourceBlock(t, rnd))
		replayedAddresses := collectAddresses(replayed)
		require.Equal(t, len(sourceAddresses), len(replayedAddresses), "round %d: address count changed on replay", rnd)
		for i := range sourceAddresses {
			original := sourceAddresses[i].String()
			replayedAnonymized := replayedAddresses[i].String()
			assert.Equal(t, mapping[original], replayedAnonymized,
				"round %d: replay changed the pseudonym for %s", rnd, original)
		}

		// No original address may remain in the configuration range.
		for _, address := range replayedAddresses {
			assert.False(t, originalAddresses[address.String()],
				"round %d: original address %s survived the anonymization", rnd, address.String())
		}
	}
}
