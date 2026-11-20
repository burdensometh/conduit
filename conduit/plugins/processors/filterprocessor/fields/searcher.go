package fields

//go:generate go run ../gen/generate.go fields ./generated_signed_txn_map.go ../Filter_tags.md

import (
	"fmt"

	sdk "github.com/algorand/go-algorand-sdk/v2/types"

	"github.com/algorand/conduit/conduit/plugins/processors/filterprocessor/expression"
)

// Searcher searches the struct with an expression
type Searcher struct {
	Exp         expression.Expression
	Tag         string
	SearchInner bool
}

// searchAtNode evaluates the expression against the supplied transaction node
// only. It never recurses into the node's inner transactions, which lets the
// caller guarantee that every predicate of a filter expression is evaluated
// against the same transaction node.
func (f Searcher) searchAtNode(input *sdk.SignedTxnWithAD) (bool, error) {
	val, err := LookupFieldByTag(f.Tag, input)
	if err != nil {
		return false, err
	}

	return f.Exp.Match(val)
}

// This function is ONLY to be used by the filter.field function.
// The reason being is that without validation of the tag (which is provided by
// MakeFieldSearcher) then this can panic
func (f Searcher) search(input *sdk.SignedTxnWithAD) (bool, error) {

	b, err := f.searchAtNode(input)
	if err != nil {
		return false, err
	}

	if b {
		return true, nil
	} else if f.SearchInner {
		// Search for matches in inner transactions
		var innerMatch bool
		for i := range input.EvalDelta.InnerTxns {
			innerMatch, err = f.search(&input.EvalDelta.InnerTxns[i])
			if err != nil {
				return false, err
			}
			if innerMatch {
				return true, nil
			}
		}
	}

	return b, nil
}

// checks that the supplied tag exists in the struct and recovers from any panics
func checkTagAndExpressionExist(expressionType expression.Type, tag string) (outError error) {
	_, err := LookupFieldByTag(tag, &sdk.SignedTxnWithAD{})

	if err != nil {
		return fmt.Errorf("%s does not exist in transactions.SignedTxnInBlock struct", tag)
	}

	if _, ok := expression.TypeMap[expressionType]; !ok {
		return fmt.Errorf("expression type (%s) is not supported", expressionType)
	}

	return nil
}

// MakeFieldSearcher will check that the field exists and that it contains the necessary "conversion" function
// TODO: Remove expressionType. It is validated when the expression is created.
func MakeFieldSearcher(e expression.Expression, expressionType expression.Type, tag string, searchInner bool) (*Searcher, error) {

	if err := checkTagAndExpressionExist(expressionType, tag); err != nil {
		return nil, err
	}

	return &Searcher{Exp: e, Tag: tag, SearchInner: searchInner}, nil
}
