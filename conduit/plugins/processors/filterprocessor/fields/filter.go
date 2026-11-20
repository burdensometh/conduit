package fields

import (
	"fmt"

	sdk "github.com/algorand/go-algorand-sdk/v2/types"
)

// Operation an operation like "any" or "all" for boolean logic
type Operation string

const anyFieldOperation Operation = "any"
const allFieldOperation Operation = "all"
const noneFieldOperation Operation = "none"

// ValidFieldOperation returns true if the input is a valid operation
func ValidFieldOperation(input string) bool {
	if input != string(anyFieldOperation) && input != string(allFieldOperation) && input != string(noneFieldOperation) {
		return false
	}

	return true
}

// InnerMatchMode controls the predicate scope used when a filter evaluates
// inner transactions.
type InnerMatchMode string

const (
	// PerFieldInnerMatchMode is the legacy behavior. Each searcher recurses
	// into the inner transaction tree on its own. With this mode the
	// individual sub-expressions of an any/all/none filter can be satisfied by
	// different transaction nodes, e.g. one sub-expression matches an inner
	// transaction while another matches a different inner transaction (or the
	// outer transaction), and the filter as a whole still reports a match.
	PerFieldInnerMatchMode InnerMatchMode = "per-field"

	// WholeExpressionInnerMatchMode evaluates the complete any/all/none
	// expression against a single transaction node at a time. The full set of
	// sub-expressions must hold for the same transaction node; only when the
	// node does not satisfy the whole expression does the filter recurse into
	// that node's inner transactions and re-evaluate the complete expression
	// there. This prevents predicates spread across different transactions from
	// being combined into a false positive.
	WholeExpressionInnerMatchMode InnerMatchMode = "whole-expression"
)

// ValidInnerMatchMode returns true if the input is a valid inner match mode.
func ValidInnerMatchMode(input string) bool {
	switch InnerMatchMode(input) {
	case PerFieldInnerMatchMode, WholeExpressionInnerMatchMode:
		return true
	default:
		return false
	}
}

// Filter an object that combines field searches with a boolean operator
type Filter struct {
	Op          Operation
	Searchers   []*Searcher
	OmitGroup   bool
	SearchInner bool
	// InnerMatchMode determines the predicate scope used while searching
	// inner transactions. The zero value defaults to PerFieldInnerMatchMode,
	// which preserves the historical per-field recursive behavior.
	InnerMatchMode InnerMatchMode
}

// matchSearchersAtNode evaluates every searcher against the same transaction
// node without recursing into its inner transactions. The any/all/none
// operation is then applied to the per-searcher results.
func (f Filter) matchSearchersAtNode(txn *sdk.SignedTxnWithAD) (bool, error) {
	numMatches := 0
	for _, fs := range f.Searchers {
		b, err := fs.searchAtNode(txn)
		if err != nil {
			return false, err
		}
		if b {
			numMatches++
		}
	}

	switch f.Op {
	case noneFieldOperation:
		return numMatches == 0, nil
	case anyFieldOperation:
		return numMatches > 0, nil
	case allFieldOperation:
		return numMatches == len(f.Searchers), nil
	default:
		return false, fmt.Errorf("unknown operation: %s", f.Op)
	}
}

// matches evaluates the filter against a transaction and, depending on the
// configured InnerMatchMode, its inner transaction tree.
func (f Filter) matches(txn *sdk.SignedTxnWithAD) (bool, error) {
	if f.InnerMatchMode == WholeExpressionInnerMatchMode {
		return f.matchesWholeExpression(txn)
	}
	return f.matchesPerField(txn)
}

// matchesPerField implements the legacy behavior where each searcher performs
// its own independent recursive search through the inner transaction tree.
// The sub-expressions of the filter may therefore be satisfied by different
// transaction nodes.
func (f Filter) matchesPerField(txn *sdk.SignedTxnWithAD) (bool, error) {
	numMatches := 0
	for _, fs := range f.Searchers {
		b, err := fs.search(txn)
		if err != nil {
			return false, err
		}
		if b {
			numMatches++
		}
	}

	switch f.Op {
	case noneFieldOperation:
		return numMatches == 0, nil
	case anyFieldOperation:
		return numMatches > 0, nil
	case allFieldOperation:
		return numMatches == len(f.Searchers), nil
	default:
		return false, fmt.Errorf("unknown operation: %s", f.Op)
	}
}

// matchesWholeExpression evaluates the complete any/all/none expression against
// a single transaction node. Only when the node fails to satisfy the whole
// expression does it recurse into the node's inner transactions, re-evaluating
// the complete expression against each inner transaction in turn. This keeps
// the predicate scope confined to one transaction node and prevents matches
// assembled from predicates that hold on different transactions.
func (f Filter) matchesWholeExpression(txn *sdk.SignedTxnWithAD) (bool, error) {
	match, err := f.matchSearchersAtNode(txn)
	if err != nil {
		return false, err
	}
	if match || !f.SearchInner {
		return match, nil
	}

	for i := range txn.EvalDelta.InnerTxns {
		innerMatch, err := f.matchesWholeExpression(&txn.EvalDelta.InnerTxns[i])
		if err != nil {
			return false, err
		}
		if innerMatch {
			return true, nil
		}
	}

	return false, nil
}

// SearchAndFilter searches through the block data and applies the operation to the results
func (f Filter) SearchAndFilter(payset []sdk.SignedTxnInBlock) ([]sdk.SignedTxnInBlock, error) {
	var result []sdk.SignedTxnInBlock
	firstGroupIdx := 0
	for i := 0; i < len(payset); i++ {
		if payset[firstGroupIdx].Txn.Group != payset[i].Txn.Group {
			firstGroupIdx = i
		}
		match, err := f.matches(&payset[i].SignedTxnWithAD)
		if err != nil {
			return nil, err
		}
		if match {
			// if txn.Group is set and omit group is false
			if payset[i].Txn.Group != (sdk.Digest{}) && !f.OmitGroup {
				j := firstGroupIdx
				// append all txns with same group ID
				for ; j < len(payset) && payset[j].Txn.Group == payset[firstGroupIdx].Txn.Group; j++ {
					result = append(result, payset[j])
				}
				// skip txns that are already added, set i to the index of the last added txn
				i = j - 1
			} else {
				result = append(result, payset[i])
			}
		}
	}

	return result, nil
}
