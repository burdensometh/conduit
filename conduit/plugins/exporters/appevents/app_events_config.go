package appevents

//go:generate go run ../../../../cmd/conduit-docs/main.go ../../../../conduit-docs/
//go:generate go run ../../../../cmd/readme_config_includer/generator.go

import (
	"regexp"
)

//Name: conduit_exporters_appevents

// LogPolicy describes what the exporter does with an application log that
// cannot be written to a configured event table.
type LogPolicy string

const (
	// PolicyKeep stores the log, undecoded, in the unmatched log table along
	// with the reason it was not decoded.
	PolicyKeep LogPolicy = "keep"
	// PolicyDrop silently discards the log.
	PolicyDrop LogPolicy = "drop"
	// PolicyFail rejects the whole round: Receive returns an error and the
	// pipeline retries according to its retry configuration.
	PolicyFail LogPolicy = "fail"
)

// EventArgConfig describes a single event argument. Arguments become typed
// columns named "arg_<name>" in the event table.
type EventArgConfig struct {
	// <code>name</code> is the argument name. It is used as the event table
	// column name with an "arg_" prefix. Optional: defaults to the argument
	// index, producing columns "arg_0", "arg_1", ...
	Name string `yaml:"name"`
	// <code>type</code> is the ARC-4 ABI type of the argument, for example
	// "uint64", "address", "string" or "(uint64,byte[])". Required when the
	// event is declared with a raw selector, optional (and must match) when a
	// signature is used.
	Type string `yaml:"type"`
}

// EventConfig describes a single application event. Each event gets its own
// table named "app_event_<name>".
type EventConfig struct {
	// <code>name</code> is the event name. The event table is named
	// "app_event_&lt;name&gt;".
	Name string `yaml:"name"`
	// <code>signature</code> is the ARC-28 event signature, for example
	// "Transfer(address,address,uint64)". The 4 byte event marker is derived
	// from the signature and the argument types are taken from it.
	Signature string `yaml:"signature"`
	// <code>selector</code> is the explicit 4 byte event marker, hex encoded
	// (with an optional "0x" prefix) or base64 encoded. Use it instead of
	// "signature" together with an explicit "args" list. If both are given
	// they must agree.
	Selector string `yaml:"selector"`
	// <code>args</code> names (and, for selector-only events, types) the event
	// arguments in order.
	Args []EventArgConfig `yaml:"args"`
}

// ExporterConfig specific to the app_events exporter
type ExporterConfig struct {
	/* <code>connection-string</code> is the Postgresql connection string<br/>
	See https://github.com/jackc/pgconn for more details.
	*/
	ConnectionString string `yaml:"connection-string"`
	// <code>max-conn</code> specifies the maximum connection number for the
	// connection pool. 0 uses the driver default.
	MaxConn uint32 `yaml:"max-conn"`
	// <code>app-ids</code> lists the application ids whose logs are decoded.
	// Logs emitted by any other application are ignored entirely.
	AppIDs []uint64 `yaml:"app-ids"`
	// <code>events</code> lists the events to decode into their own tables.
	Events []EventConfig `yaml:"events"`
	// <code>unknown-logs</code> is the policy for logs emitted by a watched
	// application whose 4 byte marker matches no configured event:
	// "keep" (default), "drop" or "fail".
	UnknownLogs LogPolicy `yaml:"unknown-logs"`
	// <code>invalid-logs</code> is the policy for logs whose marker matches a
	// configured event but whose payload cannot be ABI decoded:
	// "keep" (default), "drop" or "fail".
	InvalidLogs LogPolicy `yaml:"invalid-logs"`
}

var identifierRegexp = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

func validPolicy(p LogPolicy) bool {
	switch p {
	case PolicyKeep, PolicyDrop, PolicyFail:
		return true
	}
	return false
}
