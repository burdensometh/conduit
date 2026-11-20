# Address Anonymizer Processor

This plugin replaces account addresses in block data with deterministic
pseudonymous addresses. It is meant for producing research datasets from real
chain data: the addresses cannot be reversed without the dataset key, but the
equality relationship between addresses is preserved.

Two addresses that are equal in the input are equal in the output, regardless
of which field or role they appear in (sender, receiver, close-to, rekey,
auth-addr, asset fields, application accounts, inner transactions, the ledger
state delta, and so on). Two different addresses remain different.

## How addresses are derived

Each address is mapped with a keyed HMAC-SHA-256 over its raw 32 bytes and the
plugin's domain separator. The output is a normal 32-byte Algorand address with
a valid checksum. Consequences:

* The same input address always maps to the same output address for a given
  `dataset-key`, across transactions, transaction roles, blocks and runs.
* The mapping is one-way: the key cannot be recovered from the output, and the
  output does not reveal the input.
* Only fields the Go SDK types as an address (`types.Address`, or a slice of it)
  are rewritten. Opaque 32-byte values that merely have the same width -
  transaction IDs, group hashes, block hashes, leases, state proof commitments,
  application global/local state - are never treated as addresses and are
  preserved exactly.
* The zero address is never rewritten. It is a sentinel meaning "field not
  set", so rewriting it would change the meaning of optional fields.
* Signatures are preserved as-is (they are not addresses), so the original
  signer identity is not altered. The block header's round, genesis ID and
  genesis hash are also preserved, recording where the data came from.

## Output is marked as transformed data

The processor sets the `Anonymized` flag on every block it emits. The flag is
carried into files written by the `file_writer` exporter and back in by the
`file_reader` importer, so transformed data is always identifiable.

Because the addresses no longer match the ledger, signatures in an anonymized
block do not verify and the ledger state delta no longer describes the real
ledger. Exporters that need either (for example the `postgresql` exporter, which
rebuilds ledger state) refuse anonymized data with an error instead of silently
corrupting their state. Anonymized files can be replayed with the `file_reader`
exporter and analyzed with tools that do not depend on signature validity or an
exact delta.

## Configuration

```yml @sample.yaml
name: anonymizer
config:
    # A fixed secret used to derive stable pseudonymous addresses. Every run
    # with the same key maps the same address to the same pseudonym.
    # Either dataset-key or dataset-key-file must be set, not both.
    dataset-key: "change-me-to-a-long-random-secret"

    # Alternative to dataset-key: read the key from a file (recommended so the
    # secret does not live in the config file).
    #dataset-key-file: "/path/to/dataset.key"

    # Restrict which address-typed fields are anonymized. When omitted, every
    # address field is anonymized.
    #roles:
    #    - sender
    #    - receiver
```

`dataset-key` and `dataset-key-file` are mutually exclusive; exactly one is
required. Using a file avoids storing the secret in the configuration.

`roles` optionally restricts which address fields are rewritten. The default is
every address field. Available roles:

| Role | Fields |
| --- | --- |
| `sender` | transaction `snd` |
| `receiver` | payment `rcv` |
| `close-remainder-to` | payment `close` |
| `auth-addr` | signed transaction `sgnr` |
| `rekey-to` | transaction `rekey` |
| `asset-sender` | asset transfer `asnd` |
| `asset-receiver` | asset transfer `arcv` |
| `asset-close-to` | asset transfer `aclose` |
| `freeze-account` | asset freeze `fadd` |
| `asset-config-manager` | asset params `m` |
| `asset-config-reserve` | asset params `r` |
| `asset-config-freeze` | asset params `f` |
| `asset-config-clawback` | asset params `c` |
| `application-accounts` | application call `apat` |
| `application-access` | application call `al` (address entries) |
| `heartbeat-address` | heartbeat `hb.a` |
| `shared-accounts` | eval delta `sa` |
| `block-proposer` | block header `prp` |
| `fee-sink` | block header `fees`, genesis `fees` |
| `rewards-pool` | block header `rwd`, genesis `rwd` |
| `participation-accounts` | block header participation updates |
| `ledger-accounts` | ledger state delta balance records |
| `ledger-app-resources` | ledger state delta app resources |
| `ledger-asset-resources` | ledger state delta asset resources |
| `ledger-tx-leases` | ledger state delta transaction leases |
| `ledger-creatables` | ledger state delta creatable creators |
| `genesis-allocations` | genesis allocation entries |

Selecting a subset is not recommended when the goal is to hide an account
everywhere: an address left in one field is still visible, and its equality with
the anonymized occurrences is lost.

## Example

```yaml
processors:
  - name: anonymizer
    config:
      dataset-key-file: "/etc/conduit/dataset.key"

exporter:
  name: file_writer
  config:
    block-dir: "/tmp/anonymized-blocks"
```
