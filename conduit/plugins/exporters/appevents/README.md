# Application Events Export Plugin

Decode application logs into queryable business event tables in PostgreSQL.

The exporter watches the logs emitted by the configured applications — on
top-level application calls and on any nested inner transactions — and
decodes them with the Algorand SDK according to the configured event markers
(ARC-28 style 4 byte selectors) and ABI types. Decoded events land in one
table per event with properly typed columns, so SQL queries return real
values (integers, booleans, text, byte arrays, JSON) instead of unparsed log
strings.

## Tables

For an event named `transfer` the exporter creates `app_event_transfer`:

| column | type | contents |
| --- | --- | --- |
| `round` | `BIGINT` | round the log was emitted in |
| `app_id` | `BIGINT` | application that emitted the log |
| `txn_index` | `INTEGER` | index of the top-level transaction in the block |
| `intra_path` | `INTEGER[]` | inner transaction path, `{}` for top-level transactions |
| `log_index` | `INTEGER` | index of the log within the transaction |
| `tx_id` | `TEXT` | id of the top-level transaction |
| `log_bytes` | `BYTEA` | the original log bytes |
| `arg_<name>` | event specific | one typed column per event argument |

ABI types map to SQL types as follows: `bool` → `BOOLEAN`, `byte`/`uint8` →
`SMALLINT`, `uint16` → `INTEGER`, `uint32` → `BIGINT`, `uint64` →
`NUMERIC(20,0)`, bigger `uint<N>` and `ufixed<N>x<M>` → `NUMERIC`, `string`
and `address` → `TEXT`, `byte[]`/`byte[N]` → `BYTEA`, and all other arrays
and tuples → `JSONB`.

Logs kept by a `keep` policy are stored in `app_events_unmatched` with the
same provenance columns plus `marker` and `reason`
(`unknown_marker`, `log_too_short` or `decode_error: ...`).

## Progress and replay

Each round's events and the exporter's progress (table `app_events_meta`)
commit in a single transaction, and rounds without matching logs still
advance the progress. Before writing, the round's previous rows are deleted
inside the same transaction, so replaying a round — for example after
rewinding `app_events_meta.next_round` — replaces its events instead of
duplicating them.

## Configuration

```yml @sample.yaml
name: app_events
config:
    # Pgsql connection string
    # See https://github.com/jackc/pgconn for more details
    connection-string: "host= port=5432 user= password= dbname="

    # Maximum connection number for the connection pool.
    # 0 uses the driver default.
    max-conn: 10

    # The application ids whose logs are decoded. Logs from top-level and
    # inner application call transactions of these applications are inspected.
    app-ids: [1234]

    # The events to decode. Each event gets a table named
    # "app_event_<name>" with one typed "arg_<name>" column per argument
    # plus provenance columns (round, app_id, txn_index, intra_path,
    # log_index, tx_id, log_bytes).
    events:
        # An ARC-28 event declared by its signature. The 4 byte marker and
        # the argument types are derived from the signature.
        - name: transfer
          signature: "Transfer(address,address,uint64)"
          # Optional argument names, used for the "arg_" prefixed columns.
          args:
            - name: sender
            - name: receiver
            - name: amount

        # An event declared with an explicit 4 byte marker (hex or base64)
        # and typed arguments.
        - name: swap
          selector: "0x979289a2"
          args:
            - name: amount_in
              type: uint64
            - name: amount_out
              type: uint64
            - name: pool
              type: address

    # Policy for logs of watched applications whose 4 byte marker matches no
    # configured event: "keep" (default) stores them in the
    # "app_events_unmatched" table, "drop" discards them, "fail" rejects the
    # round.
    unknown-logs: keep

    # Policy for logs whose marker matches an event but whose payload cannot
    # be ABI decoded: "keep" (default), "drop" or "fail".
    invalid-logs: keep
```
