# Transaction Filter Processor

This plugin allows you to filter transactions based on their contents. This is useful for filtering out transactions
to the specific ones required for your application.

## Filters

Filters have two top level logical operators, `any` and `all`. These are used to match
"sub-expressions" specified in the filters. For any set of expressions, e1, e2, e3, ... `any(e1,e2,e3,...eN)` will return
`true` if there exists `eX` for `1 >= X <= N` where `eX` evaluates to `true`,
and `all(e1,e2,e3,...eN)` will return true if for every `X` from `1..N`, `eX` evaluates to `true`.

In simpler terms, `any` matches the transaction if at least one sub-expression matches, and `all` matches only if every
sub-expression matches.

If there are multiple top level filters defined in the configuration, the transaction match is combined with an AND operation. Meaning the transaction must be matched by all defined filters in order to pass through the filter.

## Sub-Expressions
So, what defines a sub-expression?

The sub-expression consists of 3 components.
### `tag`
The tag identifies the field to attempt to match. The fields derive their tags according to the
[official reference docs](https://developer.algorand.org/docs/get-details/transactions/transactions/).
You can also attempt to match against the `ApplyData`. A complete list of supported tags can be found [here](Filter_tags.md).


For now, we programmatically generate these fields into a map located in the
[filter package](https://github.com/algorand/indexer/blob/develop/conduit/plugins/processors/filterprocessor/fields/generated_signed_txn_map.go),
though this is not guaranteed to be the case.


Example:
```yaml
- tag: 'txn.snd' # Matches the Transaction Sender
- tag: 'txn.apar.c' # Matches the Clawback address of the asset params
- tag: 'txn.amt' # Matches the amount of a payment transaction
```

### `expression-type`
The expression type is a selection of one of the available methods for evaluating the expression. The current list of
types is
* `equal`: exact match for string and numeric values.
* `regex`:  applies regex rules to the matching.
* `less-than` applies numerical less than expression.
* `less-than-equal` applies numerical less than or equal expression.
* `greater-than` applies numerical greater than expression.
* `greater-than-equal` applies numerical greater than or equal expression.
* `not-equal` applies numerical not equal expression.

You must use the proper expression type for the field your tag identifies based on the type of data stored in that field.
For example, do not use a numerical expression type on a string field such as address.


### `expression`
The expression is the data against which each field will be compared. This must be compatible with the data type of
the expected field. For string fields you can also use the `regex` expression type to interpret the input of the
expression as a regex.


## Configuration
```yml @sample.yaml
name: filter_processor
config:
    # Whether the expression searches inner transactions for matches.
    search-inner: true

    # Predicate scope used when searching inner transactions. Only has an
    # effect when search-inner is enabled.
    # - "per-field" (default): each sub-expression recurses through the inner
    #   transaction tree on its own, so different sub-expressions can be
    #   satisfied by different transactions.
    # - "whole-expression": the complete any/all/none expression is evaluated
    #   against a single transaction node at a time and recursion into inner
    #   transactions only happens after the node fails the whole expression.
    inner-match-mode: per-field

    # Whether to include the entire transaction group when the filter
    # conditions are met.
    omit-group-transactions: true

    # The list of filter expressions to use when matching transactions.
    filters:
      - any:
          - tag: "txn.rcv"
            expression-type: "equal"
            expression: "ADDRESS"
```

## Predicate scope and inner transactions

With `search-inner: true` the filter walks the inner transaction tree attached
to each top-level transaction. The `inner-match-mode` setting controls the
scope in which an `any`/`all`/`none` expression is evaluated during that walk.

* `per-field` (the default, historical behavior): every sub-expression performs
  its own recursive search. This means two sub-expressions of the same filter
  can be satisfied by **different** transactions. For example, with an
  `all` filter requiring a sender and an amount, the sender could match one
  inner transaction while the amount matches another (or the outer
  transaction), and the filter would still report a match for the top-level
  transaction.
* `whole-expression`: the complete `any`/`all`/`none` expression is evaluated
  against a **single transaction node at a time**. Only when that node fails to
  satisfy the whole expression does the filter recurse into its inner
  transactions and re-evaluate the complete expression there. A match can no
  longer be assembled from predicates that hold on different transactions.

When a match is found (at the top level or anywhere in the inner transaction
tree), the top-level transaction is retained, together with the rest of its
transaction group unless `omit-group-transactions` is set. The block header is
immutable and is never modified; only the resulting payset is exported.


## Examples

Find transactions w/ fee greater than 1000 microalgos
```yaml
filters:
  - any:
    - tag: "txn.fee"
      expression-type: "greater-than"
      expression: "1000"
```

Find state proof transactions
```yaml
filters:
  - any:
    - tag: "txn.type"
      expression-type: "equal"
      expression: "stpf"
```

Find transactions calling app, "MYAPPID"
```yaml
filters:
  - all:
    - tag: "txn.type"
      expression-type: "equal"
      expression: "appl"
    - tag: "txn.apid"
      expression-type: "equal"
      expression: "MYAPPID"
```

Find transactions, including inner transactions, sent by "FOO".
```yaml
search-inner: true
filters:
  - all:
    - tag: "txn.snd"
      expression-type: "equal"
      expression: "FOO"
```

Find transactions calling app, exclude grouped transactions
```yaml
omit-group-transactions: true
filters:
  - all:
    - tag: "txn.type"
      expression-type: "equal"
      expression: "appl"
```
