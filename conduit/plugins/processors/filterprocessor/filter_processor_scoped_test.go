package filterprocessor

import (
	"context"
	"testing"

	sdk "github.com/algorand/go-algorand-sdk/v2/types"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/algorand/conduit/conduit"
	"github.com/algorand/conduit/conduit/data"
	"github.com/algorand/conduit/conduit/plugins"
)

// makeScopedBlock builds a block with two top-level transactions. The first
// one carries an inner transaction whose predicates are split across nodes:
// the root satisfies only the amount predicate while its inner transaction
// satisfies only the sender predicate. No single transaction node satisfies
// both predicates simultaneously.
func makeScopedBlock(t *testing.T) data.BlockData {
	t.Helper()

	senderA := sdk.Address{1}
	senderB := sdk.Address{2}

	bd := data.BlockData{}
	bd.BlockHeader = sdk.BlockHeader{Round: 42}
	bd.Payset = append(bd.Payset,
		// top-level txn with a split-predicate inner transaction
		sdk.SignedTxnInBlock{
			SignedTxnWithAD: sdk.SignedTxnWithAD{
				SignedTxn: sdk.SignedTxn{
					Txn: sdk.Transaction{
						Header: sdk.Header{
							Sender: senderA,
							Group:  sdk.Digest{7},
						},
						PaymentTxnFields: sdk.PaymentTxnFields{
							Amount: 500,
						},
					},
				},
				ApplyData: sdk.ApplyData{
					EvalDelta: sdk.EvalDelta{
						InnerTxns: []sdk.SignedTxnWithAD{
							{
								SignedTxn: sdk.SignedTxn{
									Txn: sdk.Transaction{
										Header: sdk.Header{
											Sender: senderB,
										},
										PaymentTxnFields: sdk.PaymentTxnFields{
											Amount: 1,
										},
									},
								},
							},
						},
					},
				},
			},
		},
		// a second txn in the same group
		sdk.SignedTxnInBlock{
			SignedTxnWithAD: sdk.SignedTxnWithAD{
				SignedTxn: sdk.SignedTxn{
					Txn: sdk.Transaction{
						Header: sdk.Header{
							Sender: senderA,
							Group:  sdk.Digest{7},
						},
					},
				},
			},
		},
		// an unrelated txn where the inner node satisfies the full expression
		sdk.SignedTxnInBlock{
			SignedTxnWithAD: sdk.SignedTxnWithAD{
				SignedTxn: sdk.SignedTxn{
					Txn: sdk.Transaction{
						Header: sdk.Header{
							Sender: senderA,
						},
						PaymentTxnFields: sdk.PaymentTxnFields{
							Amount: 1,
						},
					},
				},
				ApplyData: sdk.ApplyData{
					EvalDelta: sdk.EvalDelta{
						InnerTxns: []sdk.SignedTxnWithAD{
							{
								SignedTxn: sdk.SignedTxn{
									Txn: sdk.Transaction{
										Header: sdk.Header{
											Sender: senderB,
										},
										PaymentTxnFields: sdk.PaymentTxnFields{
											Amount: 500,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	)

	return bd
}

func initFilterProcessor(t *testing.T, cfg string) *FilterProcessor {
	t.Helper()
	fp := &FilterProcessor{}
	err := fp.Init(context.Background(), &conduit.PipelineInitProvider{}, plugins.MakePluginConfig(cfg), logrus.New())
	require.NoError(t, err)
	return fp
}

// TestFilterProcessor_WholeExpressionScope rejects a match assembled from
// predicates that hold on different transaction nodes, while the legacy
// per-field mode still accepts it.
func TestFilterProcessor_WholeExpressionScope(t *testing.T) {
	senderB := sdk.Address{2}
	bd := makeScopedBlock(t)

	cfg := func(mode string) string {
		return `---
search-inner: true
inner-match-mode: "` + mode + `"
filters:
  - all:
    - tag: txn.snd
      expression-type: equal
      expression: "` + senderB.String() + `"
    - tag: txn.amt
      expression-type: equal
      expression: 500
`
	}

	// Legacy per-field mode: the sender predicate matches the inner txn and
	// the amount predicate matches the root, so the group is retained.
	legacy := initFilterProcessor(t, cfg("per-field"))
	out, err := legacy.Process(bd)
	require.NoError(t, err)
	require.Equal(t, 3, len(out.Payset), "legacy mode combines predicates from different nodes")

	// Whole-expression mode: no single node satisfies both predicates in the
	// first group. Only the third top-level txn, whose inner node satisfies
	// the complete expression, is exported.
	scoped := initFilterProcessor(t, cfg("whole-expression"))
	out, err = scoped.Process(bd)
	require.NoError(t, err)
	require.Equal(t, 1, len(out.Payset), "only the txn with a fully-matching inner node is exported")
	assert.Equal(t, bd.Payset[2], out.Payset[0])
}

// TestFilterProcessor_DefaultInnerMatchModeIsPerField verifies that omitting
// inner-match-mode preserves the historical per-field recursive behavior.
func TestFilterProcessor_DefaultInnerMatchModeIsPerField(t *testing.T) {
	senderB := sdk.Address{2}
	bd := makeScopedBlock(t)

	cfg := `---
search-inner: true
filters:
  - all:
    - tag: txn.snd
      expression-type: equal
      expression: "` + senderB.String() + `"
    - tag: txn.amt
      expression-type: equal
      expression: 500
`
	fp := initFilterProcessor(t, cfg)
	assert.Equal(t, "per-field", string(fp.cfg.InnerMatchMode))

	out, err := fp.Process(bd)
	require.NoError(t, err)
	assert.Equal(t, 3, len(out.Payset))
}

// TestFilterProcessor_WholeExpressionRetainsGroup verifies that, on a
// whole-expression inner match, the top-level transaction and its transaction
// group are still retained according to omit-group-transactions, and that the
// immutable BlockHeader is never modified.
func TestFilterProcessor_WholeExpressionRetainsGroup(t *testing.T) {
	senderA := sdk.Address{1}
	senderB := sdk.Address{2}

	bd := data.BlockData{}
	bd.BlockHeader = sdk.BlockHeader{Round: 42}
	// A two-transaction group. The first txn's inner node satisfies the
	// complete expression; the second is a plain group member.
	bd.Payset = append(bd.Payset,
		sdk.SignedTxnInBlock{
			SignedTxnWithAD: sdk.SignedTxnWithAD{
				SignedTxn: sdk.SignedTxn{
					Txn: sdk.Transaction{
						Header: sdk.Header{
							Sender: senderA,
							Group:  sdk.Digest{9},
						},
						PaymentTxnFields: sdk.PaymentTxnFields{Amount: 1},
					},
				},
				ApplyData: sdk.ApplyData{
					EvalDelta: sdk.EvalDelta{
						InnerTxns: []sdk.SignedTxnWithAD{
							{
								SignedTxn: sdk.SignedTxn{
									Txn: sdk.Transaction{
										Header: sdk.Header{Sender: senderB},
										PaymentTxnFields: sdk.PaymentTxnFields{
											Amount: 500,
										},
									},
								},
							},
						},
					},
				},
			},
		},
		sdk.SignedTxnInBlock{
			SignedTxnWithAD: sdk.SignedTxnWithAD{
				SignedTxn: sdk.SignedTxn{
					Txn: sdk.Transaction{
						Header: sdk.Header{
							Sender: senderA,
							Group:  sdk.Digest{9},
						},
					},
				},
			},
		},
	)
	originalHeader := bd.BlockHeader

	cfg := `---
search-inner: true
inner-match-mode: whole-expression
omit-group-transactions: false
filters:
  - all:
    - tag: txn.snd
      expression-type: equal
      expression: "` + senderB.String() + `"
    - tag: txn.amt
      expression-type: equal
      expression: 500
`
	fp := initFilterProcessor(t, cfg)
	out, err := fp.Process(bd)
	require.NoError(t, err)

	// The whole group is retained because the inner node of the first txn
	// satisfies the complete expression.
	require.Equal(t, 2, len(out.Payset))
	assert.Equal(t, bd.Payset[0], out.Payset[0])
	assert.Equal(t, bd.Payset[1], out.Payset[1])

	// The immutable block header must pass through untouched.
	assert.Equal(t, originalHeader, out.BlockHeader)
	assert.Equal(t, uint64(42), out.Round())

	// With omit-group-transactions enabled the same match exports only the
	// matched top-level txn.
	cfgOmit := `---
search-inner: true
inner-match-mode: whole-expression
omit-group-transactions: true
filters:
  - all:
    - tag: txn.snd
      expression-type: equal
      expression: "` + senderB.String() + `"
    - tag: txn.amt
      expression-type: equal
      expression: 500
`
	fpOmit := initFilterProcessor(t, cfgOmit)
	out, err = fpOmit.Process(bd)
	require.NoError(t, err)
	require.Equal(t, 1, len(out.Payset))
	assert.Equal(t, bd.Payset[0], out.Payset[0])
	assert.Equal(t, originalHeader, out.BlockHeader)
}

// TestFilterProcessor_InvalidInnerMatchMode verifies that an invalid
// inner-match-mode is rejected during initialization.
func TestFilterProcessor_InvalidInnerMatchMode(t *testing.T) {
	cfg := `---
search-inner: true
inner-match-mode: made-up-mode
filters:
  - any:
    - tag: txn.snd
      expression-type: equal
      expression: "SAMPLEADDRESS"
`
	fp := &FilterProcessor{}
	err := fp.Init(context.Background(), &conduit.PipelineInitProvider{}, plugins.MakePluginConfig(cfg), logrus.New())
	require.Error(t, err)
	assert.ErrorContains(t, err, "inner-match-mode was not a valid value")
}

// TestFilterProcessor_WholeExpressionNone verifies "none" scoping: the
// complete none-expression is evaluated per node before recursing.
func TestFilterProcessor_WholeExpressionNone(t *testing.T) {
	senderA := sdk.Address{1}
	senderB := sdk.Address{2}

	bd := data.BlockData{}
	bd.BlockHeader = sdk.BlockHeader{Round: 7}
	// Root has senderA (fails the none expression), its inner txn has
	// senderB (satisfies the none expression on that node).
	bd.Payset = append(bd.Payset,
		sdk.SignedTxnInBlock{
			SignedTxnWithAD: sdk.SignedTxnWithAD{
				SignedTxn: sdk.SignedTxn{
					Txn: sdk.Transaction{
						Header: sdk.Header{Sender: senderA},
					},
				},
				ApplyData: sdk.ApplyData{
					EvalDelta: sdk.EvalDelta{
						InnerTxns: []sdk.SignedTxnWithAD{
							{
								SignedTxn: sdk.SignedTxn{
									Txn: sdk.Transaction{
										Header: sdk.Header{Sender: senderB},
									},
								},
							},
						},
					},
				},
			},
		},
	)

	cfg := `---
search-inner: true
inner-match-mode: whole-expression
filters:
  - none:
    - tag: txn.snd
      expression-type: equal
      expression: "` + senderA.String() + `"
`
	fp := initFilterProcessor(t, cfg)
	out, err := fp.Process(bd)
	require.NoError(t, err)
	assert.Equal(t, 1, len(out.Payset))

	// When every node has senderA the none expression is never satisfied.
	bd.Payset[0].EvalDelta.InnerTxns[0].SignedTxn.Txn.Header.Sender = senderA
	out, err = fp.Process(bd)
	require.NoError(t, err)
	assert.Equal(t, 0, len(out.Payset))
	// The header remains untouched even when the payset is fully filtered.
	assert.Equal(t, uint64(7), out.Round())
}
