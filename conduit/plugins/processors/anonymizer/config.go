// Package anonymizer docs
package anonymizer

//go:generate go run ../../../../cmd/conduit-docs/main.go ../../../../conduit-docs/
//go:generate go run ../../../../cmd/readme_config_includer/generator.go

//Name: conduit_processors_anonymizer

// Config configuration for the anonymizer processor.
type Config struct {
	/* <code>dataset-key</code> is the fixed secret used to derive pseudonymous addresses.<br/>
	Every run of the pipeline that uses the same key produces the same pseudonym for
	the same address, which preserves the equality relation between addresses across
	transactions, blocks, and runs. The key is never written to the output.<br/>
	Either <code>dataset-key</code> or <code>dataset-key-file</code> must be provided.
	*/
	DatasetKey string `yaml:"dataset-key"`
	/* <code>dataset-key-file</code> is a path to a file containing the dataset key.<br/>
	This is an alternative to <code>dataset-key</code> that keeps the secret out of the
	configuration file. Trailing whitespace is ignored. It is an error to set both.
	*/
	DatasetKeyFile string `yaml:"dataset-key-file"`
	/* <code>roles</code> restricts which address-typed fields are anonymized.<br/>
	When omitted, every address field is anonymized. Values are matched against the
	Go SDK address fields; opaque 32-byte values (transaction IDs, group hashes,
	block hashes, leases, state proof commitments) are never treated as addresses.<br/>
	Supported roles:
	<ul>
		<li>sender</li>
		<li>receiver</li>
		<li>close-remainder-to</li>
		<li>auth-addr</li>
		<li>rekey-to</li>
		<li>asset-sender</li>
		<li>asset-receiver</li>
		<li>asset-close-to</li>
		<li>freeze-account</li>
		<li>asset-config-manager</li>
		<li>asset-config-reserve</li>
		<li>asset-config-freeze</li>
		<li>asset-config-clawback</li>
		<li>application-accounts</li>
		<li>application-access</li>
		<li>heartbeat-address</li>
		<li>shared-accounts</li>
		<li>block-proposer</li>
		<li>fee-sink</li>
		<li>rewards-pool</li>
		<li>participation-accounts</li>
		<li>ledger-accounts</li>
		<li>ledger-app-resources</li>
		<li>ledger-asset-resources</li>
		<li>ledger-tx-leases</li>
		<li>ledger-creatables</li>
		<li>genesis-allocations</li>
	</ul>
	*/
	Roles []Role `yaml:"roles"`
}
