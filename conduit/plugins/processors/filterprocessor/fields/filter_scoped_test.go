package fields

import (
	"testing"

	sdk "github.com/algorand/go-algorand-sdk/v2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/algorand/conduit/conduit/plugins/processors/filterprocessor/expression"
)

func makeTestSearcher(t *testing.T, expressionType expression.Type, tag, searchStr string) *Searcher {
	// Derive the target type from the tag so numeric tags get numeric
	// expressions, mirroring the processor initialization path.
	target, err := LookupFieldByTag(tag, &sdk.SignedTxnWithAD{})
	require.NoError(t, err)
	exp, err := expression.MakeExpression(expressionType, searchStr, target)
	require.NoError(t, err)
	searcher, err := MakeFieldSearcher(exp, expressionType, tag, true)
	require.NoError(t, err)
	return searcher
}

// TestFilter_InnerMatchModeScoping builds a transaction whose root and inner
// transactions each satisfy only one of the two "all" sub-expressions. The
// whole-expression mode must not match because no single transaction node
// satisfies the complete expression, while the legacy per-field mode still
// matches by combining predicates from different nodes.
func TestFilter_InnerMatchModeScoping(t *testing.T) {
	senderA := sdk.Address{1}
	senderB := sdk.Address{2}

	txn := &sdk.SignedTxnWithAD{
		SignedTxn: sdk.SignedTxn{
			Txn: sdk.Transaction{
				Header: sdk.Header{
					Sender: senderA,
				},
				PaymentTxnFields: sdk.PaymentTxnFields{
					// root satisfies the amount predicate but not the sender predicate
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
									// inner txn satisfies the sender predicate but not the amount predicate
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
	}

	searchers := []*Searcher{
		makeTestSearcher(t, expression.EqualTo, "txn.snd", senderB.String()),
		makeTestSearcher(t, expression.EqualTo, "txn.amt", "500"),
	}

	// Legacy mode: each searcher recurses independently and the predicates
	// are combined across different transaction nodes.
	legacy := Filter{
		Op:             allFieldOperation,
		Searchers:      searchers,
		SearchInner:    true,
		InnerMatchMode: PerFieldInnerMatchMode,
	}
	match, err := legacy.matches(txn)
	require.NoError(t, err)
	assert.True(t, match, "legacy per-field mode combines predicates from different nodes")

	// Whole-expression mode: no single node satisfies both predicates.
	scoped := Filter{
		Op:             allFieldOperation,
		Searchers:      searchers,
		SearchInner:    true,
		InnerMatchMode: WholeExpressionInnerMatchMode,
	}
	match, err = scoped.matches(txn)
	require.NoError(t, err)
	assert.False(t, match, "whole-expression mode must not combine predicates from different nodes")
}

// TestFilter_WholeExpressionMatchesInnerNode verifies that the
// whole-expression mode finds a match when a single inner transaction node
// satisfies the complete expression.
func TestFilter_WholeExpressionMatchesInnerNode(t *testing.T) {
	senderB := sdk.Address{2}

	txn := &sdk.SignedTxnWithAD{
		SignedTxn: sdk.SignedTxn{
			Txn: sdk.Transaction{
				Header: sdk.Header{
					Sender: sdk.Address{1},
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
									// the inner node satisfies both predicates
									Amount: 500,
								},
							},
						},
					},
				},
			},
		},
	}

	searchers := []*Searcher{
		makeTestSearcher(t, expression.EqualTo, "txn.snd", senderB.String()),
		makeTestSearcher(t, expression.EqualTo, "txn.amt", "500"),
	}

	scoped := Filter{
		Op:             allFieldOperation,
		Searchers:      searchers,
		SearchInner:    true,
		InnerMatchMode: WholeExpressionInnerMatchMode,
	}
	match, err := scoped.matches(txn)
	require.NoError(t, err)
	assert.True(t, match, "the inner node satisfies the complete expression")

	// With inner searching disabled nothing matches because the root node
	// does not satisfy the expression.
	scoped.SearchInner = false
	match, err = scoped.matches(txn)
	require.NoError(t, err)
	assert.False(t, match)
}

// TestFilter_WholeExpressionAny verifies "any" semantics under the
// whole-expression mode: a node matches when any sub-expression holds on that
// same node, and recursion only happens after the node fails the full expression.
func TestFilter_WholeExpressionAny(t *testing.T) {
	senderA := sdk.Address{1}
	senderB := sdk.Address{2}

	txn := &sdk.SignedTxnWithAD{
		SignedTxn: sdk.SignedTxn{
			Txn: sdk.Transaction{
				Header: sdk.Header{
					Sender: senderA,
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
							},
						},
					},
				},
			},
		},
	}

	searchers := []*Searcher{
		makeTestSearcher(t, expression.EqualTo, "txn.snd", senderB.String()),
	}

	scoped := Filter{
		Op:             anyFieldOperation,
		Searchers:      searchers,
		SearchInner:    true,
		InnerMatchMode: WholeExpressionInnerMatchMode,
	}
	match, err := scoped.matches(txn)
	require.NoError(t, err)
	assert.True(t, match)
}

// TestFilter_WholeExpressionNone verifies that "none" under the
// whole-expression mode evaluates the complete expression per node and only
// reports a match when the evaluated node satisfies none of the sub-expressions.
func TestFilter_WholeExpressionNone(t *testing.T) {
	senderA := sdk.Address{1}
	senderB := sdk.Address{2}

	// Root matches the sender predicate; the inner node does not.
	txn := &sdk.SignedTxnWithAD{
		SignedTxn: sdk.SignedTxn{
			Txn: sdk.Transaction{
				Header: sdk.Header{
					Sender: senderA,
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
							},
						},
					},
				},
			},
		},
	}

	searchers := []*Searcher{
		makeTestSearcher(t, expression.EqualTo, "txn.snd", senderA.String()),
	}

	scoped := Filter{
		Op:             noneFieldOperation,
		Searchers:      searchers,
		SearchInner:    true,
		InnerMatchMode: WholeExpressionInnerMatchMode,
	}
	// The root node fails the "none" expression (it has senderA), so the
	// filter recurses and finds the inner node, which satisfies "none".
	match, err := scoped.matches(txn)
	require.NoError(t, err)
	assert.True(t, match)

	// A tree in which every node has senderA never satisfies the expression.
	txn.EvalDelta.InnerTxns[0].SignedTxn.Txn.Header.Sender = senderA
	match, err = scoped.matches(txn)
	require.NoError(t, err)
	assert.False(t, match)
}

// TestFilter_WholeExpressionNestedRecursion verifies the whole-expression
// mode recurses through multiple levels of inner transactions, always keeping
// the complete expression within one node.
func TestFilter_WholeExpressionNestedRecursion(t *testing.T) {
	senderB := sdk.Address{2}

	txn := &sdk.SignedTxnWithAD{
		SignedTxn: sdk.SignedTxn{
			Txn: sdk.Transaction{
				Header: sdk.Header{
					Sender: sdk.Address{1},
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
								Header:           sdk.Header{Sender: sdk.Address{3}},
								PaymentTxnFields: sdk.PaymentTxnFields{Amount: 2},
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
													Amount: 777,
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	searchers := []*Searcher{
		makeTestSearcher(t, expression.EqualTo, "txn.snd", senderB.String()),
		makeTestSearcher(t, expression.EqualTo, "txn.amt", "777"),
	}

	scoped := Filter{
		Op:             allFieldOperation,
		Searchers:      searchers,
		SearchInner:    true,
		InnerMatchMode: WholeExpressionInnerMatchMode,
	}
	match, err := scoped.matches(txn)
	require.NoError(t, err)
	assert.True(t, match, "the deeply nested node satisfies the complete expression")

	// Split the predicates across the nested node and its parent: no single
	// node satisfies the complete expression anymore.
	txn.EvalDelta.InnerTxns[0].EvalDelta.InnerTxns[0].SignedTxn.Txn.PaymentTxnFields.Amount = 1
	txn.EvalDelta.InnerTxns[0].SignedTxn.Txn.PaymentTxnFields.Amount = 777
	match, err = scoped.matches(txn)
	require.NoError(t, err)
	assert.False(t, match)
}

func TestValidInnerMatchMode(t *testing.T) {
	assert.True(t, ValidInnerMatchMode(string(PerFieldInnerMatchMode)))
	assert.True(t, ValidInnerMatchMode(string(WholeExpressionInnerMatchMode)))
	assert.False(t, ValidInnerMatchMode("made-up-mode"))
}
