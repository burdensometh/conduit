package anonymizer

import (
	"context"
	"os"
	"path"
	"testing"

	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	sdk "github.com/algorand/go-algorand-sdk/v2/types"

	"github.com/algorand/conduit/conduit"
	"github.com/algorand/conduit/conduit/data"
	"github.com/algorand/conduit/conduit/plugins"
)

const testDatasetKey = "unit-test-dataset-key"

var testLogger *logrus.Logger

func init() {
	testLogger, _ = logrustest.NewNullLogger()
}

func mustDecodeAddress(t *testing.T, address string) sdk.Address {
	t.Helper()
	decoded, err := sdk.DecodeAddress(address)
	require.NoError(t, err)
	return decoded
}

func newTestTransformer(t *testing.T, roles []Role) *transformer {
	t.Helper()
	resolved, err := resolveRoles(roles)
	require.NoError(t, err)
	return &transformer{
		mapper: newAddressMapper([]byte(testDatasetKey)),
		roles:  resolved,
	}
}

// TestSameAddressMapsEverywhere verifies that a single address receives the
// same pseudonym across every role it can appear in, while distinct addresses
// stay distinct.
func TestSameAddressMapsEverywhere(t *testing.T) {
	transformer := newTestTransformer(t, nil)

	sender := mustDecodeAddress(t, "NVCAFYNKJL2NGAIZHWLIKI6HGMTLYXL7BXPBO7NXX4A7GMMWKNFKFKDKP4")
	receiver := mustDecodeAddress(t, "GD64YIY3TWGDMCNPP553DZPPR6LDUSFQOIJVFDPPXWEG3FVOJCCDBBHU5A")

	block := data.BlockData{
		BlockHeader: sdk.BlockHeader{
			Round:    42,
			Proposer: sender,
		},
		Payset: []sdk.SignedTxnInBlock{
			{
				SignedTxnWithAD: sdk.SignedTxnWithAD{
					SignedTxn: sdk.SignedTxn{
						AuthAddr: sender,
						Txn: sdk.Transaction{
							Type:   sdk.PaymentTx,
							Header: sdk.Header{Sender: sender, RekeyTo: sender},
							PaymentTxnFields: sdk.PaymentTxnFields{
								Receiver:         receiver,
								CloseRemainderTo: receiver,
							},
						},
					},
					ApplyData: sdk.ApplyData{
						EvalDelta: sdk.EvalDelta{
							SharedAccts: []sdk.Address{receiver},
							InnerTxns: []sdk.SignedTxnWithAD{
								{
									SignedTxn: sdk.SignedTxn{
										Txn: sdk.Transaction{
											Type:             sdk.PaymentTx,
											Header:           sdk.Header{Sender: receiver},
											PaymentTxnFields: sdk.PaymentTxnFields{Receiver: sender},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		Delta: &sdk.LedgerStateDelta{
			Accts: sdk.AccountDeltas{
				Accts: []sdk.BalanceRecord{
					{Addr: sender, AccountData: sdk.AccountData{AccountBaseData: sdk.AccountBaseData{AuthAddr: receiver}}},
				},
			},
			Txleases: map[sdk.Txlease]sdk.Round{
				{Sender: sender}: 100,
			},
			Creatables: map[sdk.CreatableIndex]sdk.ModifiedCreatable{
				1: {Creator: receiver},
			},
		},
	}

	out, err := (&Anonymizer{transformer: transformer}).Process(block)
	require.NoError(t, err)

	mappedSender := out.Payset[0].Txn.Sender
	mappedReceiver := out.Payset[0].Txn.Receiver

	// The pseudonyms must differ from the originals.
	assert.NotEqual(t, sender, mappedSender)
	assert.NotEqual(t, receiver, mappedReceiver)
	assert.NotEqual(t, mappedSender, mappedReceiver)

	// Every occurrence of the sender maps to the same pseudonym.
	assert.Equal(t, mappedSender, out.BlockHeader.Proposer)
	assert.Equal(t, mappedSender, out.Payset[0].AuthAddr)
	assert.Equal(t, mappedSender, out.Payset[0].Txn.RekeyTo)
	assert.Equal(t, mappedSender, out.Payset[0].EvalDelta.InnerTxns[0].Txn.Receiver)
	assert.Equal(t, mappedSender, out.Delta.Accts.Accts[0].Addr)
	assert.Equal(t, sdk.Round(100), out.Delta.Txleases[sdk.Txlease{Sender: mappedSender}])

	// Every occurrence of the receiver maps to the same pseudonym.
	assert.Equal(t, mappedReceiver, out.Payset[0].Txn.CloseRemainderTo)
	assert.Equal(t, mappedReceiver, out.Payset[0].EvalDelta.SharedAccts[0])
	assert.Equal(t, mappedReceiver, out.Payset[0].EvalDelta.InnerTxns[0].Txn.Sender)
	assert.Equal(t, mappedReceiver, out.Delta.Accts.Accts[0].AuthAddr)
	assert.Equal(t, mappedReceiver, out.Delta.Creatables[1].Creator)

	// Output is marked as transformed.
	assert.True(t, out.Anonymized)
}

// TestDigestsAreNotAddresses verifies that 32-byte values which are hashes, not
// addresses, are left untouched.
func TestDigestsAreNotAddresses(t *testing.T) {
	transformer := newTestTransformer(t, nil)

	var digest sdk.Digest
	for i := range digest {
		digest[i] = byte(i + 1)
	}
	group := digest
	lease := [32]byte(digest)
	txID := sdk.Txid(digest)

	sender := mustDecodeAddress(t, "NVCAFYNKJL2NGAIZHWLIKI6HGMTLYXL7BXPBO7NXX4A7GMMWKNFKFKDKP4")
	block := data.BlockData{
		BlockHeader: sdk.BlockHeader{
			Round:          7,
			Branch:         sdk.BlockHash(digest),
			GenesisHash:    digest,
			TxnCommitments: sdk.TxnCommitments{NativeSha512_256Commitment: digest},
		},
		Payset: []sdk.SignedTxnInBlock{
			{
				SignedTxnWithAD: sdk.SignedTxnWithAD{
					SignedTxn: sdk.SignedTxn{
						Txn: sdk.Transaction{
							Type:   sdk.PaymentTx,
							Header: sdk.Header{Sender: sender, Group: group, Lease: lease},
						},
					},
				},
			},
		},
		Delta: &sdk.LedgerStateDelta{
			Txids: map[sdk.Txid]sdk.IncludedTransactions{txID: {LastValid: 10}},
		},
	}

	out, err := (&Anonymizer{transformer: transformer}).Process(block)
	require.NoError(t, err)

	assert.Equal(t, sdk.BlockHash(digest), out.BlockHeader.Branch)
	assert.Equal(t, digest, out.BlockHeader.GenesisHash)
	assert.Equal(t, digest, out.BlockHeader.NativeSha512_256Commitment)
	assert.Equal(t, group, out.Payset[0].Txn.Group)
	assert.Equal(t, lease, out.Payset[0].Txn.Lease)
	_, ok := out.Delta.Txids[txID]
	assert.True(t, ok, "transaction id must be preserved")
}

// TestZeroAddressPreserved verifies the zero-address sentinel is not rewritten.
func TestZeroAddressPreserved(t *testing.T) {
	transformer := newTestTransformer(t, nil)
	sender := mustDecodeAddress(t, "NVCAFYNKJL2NGAIZHWLIKI6HGMTLYXL7BXPBO7NXX4A7GMMWKNFKFKDKP4")

	block := data.BlockData{
		Payset: []sdk.SignedTxnInBlock{{
			SignedTxnWithAD: sdk.SignedTxnWithAD{
				SignedTxn: sdk.SignedTxn{
					Txn: sdk.Transaction{
						Type:             sdk.PaymentTx,
						Header:           sdk.Header{Sender: sender},
						PaymentTxnFields: sdk.PaymentTxnFields{Receiver: sdk.ZeroAddress},
					},
				},
			},
		}},
	}

	out, err := (&Anonymizer{transformer: transformer}).Process(block)
	require.NoError(t, err)
	assert.True(t, out.Payset[0].Txn.Receiver.IsZero())
}

// TestInputNotMutated verifies the processor clones rather than mutating the
// block owned by the importer.
func TestInputNotMutated(t *testing.T) {
	transformer := newTestTransformer(t, nil)
	sender := mustDecodeAddress(t, "NVCAFYNKJL2NGAIZHWLIKI6HGMTLYXL7BXPBO7NXX4A7GMMWKNFKFKDKP4")

	block := data.BlockData{
		BlockHeader: sdk.BlockHeader{Round: 1, Proposer: sender},
		Payset: []sdk.SignedTxnInBlock{{
			SignedTxnWithAD: sdk.SignedTxnWithAD{
				SignedTxn: sdk.SignedTxn{Txn: sdk.Transaction{Type: sdk.PaymentTx, Header: sdk.Header{Sender: sender}}},
			},
		}},
		Delta: &sdk.LedgerStateDelta{
			Accts: sdk.AccountDeltas{Accts: []sdk.BalanceRecord{{Addr: sender}}},
		},
	}

	_, err := (&Anonymizer{transformer: transformer}).Process(block)
	require.NoError(t, err)

	assert.Equal(t, sender, block.BlockHeader.Proposer)
	assert.Equal(t, sender, block.Payset[0].Txn.Sender)
	assert.Equal(t, sender, block.Delta.Accts.Accts[0].Addr)
	assert.False(t, block.Anonymized)
}

// TestDeterministicAcrossInstances verifies that two processor instances with
// the same dataset key produce the same mapping, and that a different key does
// not.
func TestDeterministicAcrossInstances(t *testing.T) {
	address := mustDecodeAddress(t, "NVCAFYNKJL2NGAIZHWLIKI6HGMTLYXL7BXPBO7NXX4A7GMMWKNFKFKDKP4")

	first := newAddressMapper([]byte(testDatasetKey)).mapAddress(address)
	second := newAddressMapper([]byte(testDatasetKey)).mapAddress(address)
	assert.Equal(t, first, second)

	other := newAddressMapper([]byte("a-different-key")).mapAddress(address)
	assert.NotEqual(t, first, other)
}

// TestRolesRestrictFields verifies that selecting a subset of roles leaves the
// other fields untouched.
func TestRolesRestrictFields(t *testing.T) {
	transformer := newTestTransformer(t, []Role{RoleReceiver})
	sender := mustDecodeAddress(t, "NVCAFYNKJL2NGAIZHWLIKI6HGMTLYXL7BXPBO7NXX4A7GMMWKNFKFKDKP4")
	receiver := mustDecodeAddress(t, "GD64YIY3TWGDMCNPP553DZPPR6LDUSFQOIJVFDPPXWEG3FVOJCCDBBHU5A")

	block := data.BlockData{
		BlockHeader: sdk.BlockHeader{Round: 1, Proposer: sender},
		Payset: []sdk.SignedTxnInBlock{{
			SignedTxnWithAD: sdk.SignedTxnWithAD{
				SignedTxn: sdk.SignedTxn{
					AuthAddr: sender,
					Txn: sdk.Transaction{
						Type:             sdk.PaymentTx,
						Header:           sdk.Header{Sender: sender, RekeyTo: sender},
						PaymentTxnFields: sdk.PaymentTxnFields{Receiver: receiver},
					},
				},
			},
		}},
	}

	out, err := (&Anonymizer{transformer: transformer}).Process(block)
	require.NoError(t, err)
	assert.Equal(t, sender, out.BlockHeader.Proposer)
	assert.Equal(t, sender, out.Payset[0].AuthAddr)
	assert.Equal(t, sender, out.Payset[0].Txn.Sender)
	assert.Equal(t, sender, out.Payset[0].Txn.RekeyTo)
	assert.NotEqual(t, receiver, out.Payset[0].Txn.Receiver)
}

// TestAlreadyTransformedPassThrough verifies that data marked as transformed
// is returned unchanged, so a replay cannot double-map pseudonyms.
func TestAlreadyTransformedPassThrough(t *testing.T) {
	transformer := newTestTransformer(t, nil)
	sender := mustDecodeAddress(t, "NVCAFYNKJL2NGAIZHWLIKI6HGMTLYXL7BXPBO7NXX4A7GMMWKNFKFKDKP4")

	block := data.BlockData{
		Anonymized: true,
		Payset: []sdk.SignedTxnInBlock{{
			SignedTxnWithAD: sdk.SignedTxnWithAD{
				SignedTxn: sdk.SignedTxn{Txn: sdk.Transaction{Type: sdk.PaymentTx, Header: sdk.Header{Sender: sender}}},
			},
		}},
	}

	out, err := (&Anonymizer{transformer: transformer}).Process(block)
	require.NoError(t, err)
	assert.Equal(t, sender, out.Payset[0].Txn.Sender)
	assert.True(t, out.Anonymized)
}

func makeInitProvider(genesis *sdk.Genesis) *conduit.PipelineInitProvider {
	round := sdk.Round(0)
	return conduit.MakePipelineInitProvider(&round, genesis, nil)
}

func TestInitRequiresKey(t *testing.T) {
	anonymizer := &Anonymizer{}
	err := anonymizer.Init(context.Background(), makeInitProvider(nil), plugins.MakePluginConfig(""), testLogger)
	assert.ErrorContains(t, err, "dataset-key")
}

func TestInitRejectsBothKeys(t *testing.T) {
	anonymizer := &Anonymizer{}
	cfg := plugins.MakePluginConfig("dataset-key: a\ndataset-key-file: /tmp/nope")
	err := anonymizer.Init(context.Background(), makeInitProvider(nil), cfg, testLogger)
	assert.ErrorContains(t, err, "only one")
}

func TestInitRejectsUnknownRole(t *testing.T) {
	anonymizer := &Anonymizer{}
	cfg := plugins.MakePluginConfig("dataset-key: a\nroles: [not-a-role]")
	err := anonymizer.Init(context.Background(), makeInitProvider(nil), cfg, testLogger)
	assert.ErrorContains(t, err, "unknown address role")
}

func TestInitKeyFile(t *testing.T) {
	keyPath := path.Join(t.TempDir(), "dataset.key")
	require.NoError(t, os.WriteFile(keyPath, []byte("  file-dataset-key\n"), 0600))

	anonymizer := &Anonymizer{}
	cfg := plugins.MakePluginConfig("dataset-key-file: " + keyPath)
	require.NoError(t, anonymizer.Init(context.Background(), makeInitProvider(nil), cfg, testLogger))

	address := mustDecodeAddress(t, "NVCAFYNKJL2NGAIZHWLIKI6HGMTLYXL7BXPBO7NXX4A7GMMWKNFKFKDKP4")
	expected := newAddressMapper([]byte("file-dataset-key")).mapAddress(address)
	assert.Equal(t, expected, anonymizer.transformer.mapper.mapAddress(address))
}

func TestInitEmptyKeyFile(t *testing.T) {
	keyPath := path.Join(t.TempDir(), "dataset.key")
	require.NoError(t, os.WriteFile(keyPath, []byte("   \n"), 0600))

	anonymizer := &Anonymizer{}
	cfg := plugins.MakePluginConfig("dataset-key-file: " + keyPath)
	err := anonymizer.Init(context.Background(), makeInitProvider(nil), cfg, testLogger)
	assert.ErrorContains(t, err, "empty")
}

// TestInitAnonymizesGenesis verifies the pipeline genesis is rewritten to the
// same pseudonyms the blocks use.
func TestInitAnonymizesGenesis(t *testing.T) {
	feeSink := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAVIOOBQA"
	allocation := "AEAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAKE3PRHE"
	genesis := &sdk.Genesis{
		Network:     "testnet",
		FeeSink:     feeSink,
		RewardsPool: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABFFF5B2Y",
		Allocation:  []sdk.GenesisAllocation{{Address: allocation}},
	}
	provider := makeInitProvider(genesis)

	anonymizer := &Anonymizer{}
	hashBefore := genesis.Hash()
	require.NoError(t, anonymizer.Init(context.Background(), provider, plugins.MakePluginConfig("dataset-key: k"), testLogger))

	anonymizedGenesis := provider.GetGenesis()
	assert.NotEqual(t, feeSink, anonymizedGenesis.FeeSink)
	assert.NotEqual(t, allocation, anonymizedGenesis.Allocation[0].Address)

	// The anonymized genesis must use the same pseudonym as the block mapper.
	mappedFeeSink := anonymizer.transformer.mapper.mapAddress(mustDecodeAddress(t, feeSink)).String()
	assert.Equal(t, mappedFeeSink, anonymizedGenesis.FeeSink)

	// The input genesis object is not modified.
	assert.Equal(t, feeSink, genesis.FeeSink)
	assert.Equal(t, hashBefore, genesis.Hash())
}

func TestInitPassesThroughAlreadyAnonymizedInput(t *testing.T) {
	genesis := &sdk.Genesis{Network: "testnet", FeeSink: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAVIOOBQA"}
	provider := makeInitProvider(genesis)
	provider.SetAnonymized(true)

	anonymizer := &Anonymizer{}
	require.NoError(t, anonymizer.Init(context.Background(), provider, plugins.MakePluginConfig("dataset-key: k"), testLogger))

	// The genesis must not be rewritten when the input is already transformed.
	assert.Equal(t, genesis.FeeSink, provider.GetGenesis().FeeSink)
	assert.True(t, anonymizer.alreadyTransformed)
}

func TestMetadata(t *testing.T) {
	anonymizer := &Anonymizer{}
	assert.Equal(t, PluginName, anonymizer.Metadata().Name)
}

func TestConfigSampleIsValid(t *testing.T) {
	var cfg data.NameConfigPair
	require.NoError(t, yaml.Unmarshal([]byte(sampleConfig), &cfg))
	assert.Equal(t, PluginName, cfg.Name)
}

// TestConfigUnmarshal verifies the yaml shape, including the role list.
func TestConfigUnmarshal(t *testing.T) {
	var cfg Config
	require.NoError(t, yaml.Unmarshal([]byte(`
dataset-key: inline
roles:
  - sender
  - receiver
`), &cfg))
	assert.Equal(t, "inline", cfg.DatasetKey)
	assert.Equal(t, []Role{RoleSender, RoleReceiver}, cfg.Roles)

	roles, err := resolveRoles(cfg.Roles)
	require.NoError(t, err)
	assert.True(t, roles.has(RoleSender))
	assert.True(t, roles.has(RoleReceiver))
	assert.False(t, roles.has(RoleProposer))
}
