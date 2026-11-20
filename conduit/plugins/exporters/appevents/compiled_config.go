package appevents

import (
	"fmt"
)

// compiledConfig is the validated, ready to use form of ExporterConfig.
type compiledConfig struct {
	apps          map[uint64]struct{}
	events        []*eventDef
	bySelector    map[[selectorLength]byte]*eventDef
	unknownPolicy LogPolicy
	invalidPolicy LogPolicy
}

// compileConfig validates the exporter configuration and compiles the event
// definitions (selectors, ABI types, SQL column types).
func compileConfig(cfg *ExporterConfig) (*compiledConfig, error) {
	if cfg.ConnectionString == "" {
		return nil, fmt.Errorf("connection-string is empty")
	}
	if len(cfg.AppIDs) == 0 {
		return nil, fmt.Errorf("app-ids must list at least one application id")
	}
	apps := make(map[uint64]struct{}, len(cfg.AppIDs))
	for _, id := range cfg.AppIDs {
		if _, ok := apps[id]; ok {
			return nil, fmt.Errorf("app-ids contains duplicate application id %d", id)
		}
		apps[id] = struct{}{}
	}

	unknown := cfg.UnknownLogs
	if unknown == "" {
		unknown = PolicyKeep
	}
	if !validPolicy(unknown) {
		return nil, fmt.Errorf("unknown-logs policy %q is invalid, must be one of keep, drop, fail", cfg.UnknownLogs)
	}
	invalid := cfg.InvalidLogs
	if invalid == "" {
		invalid = PolicyKeep
	}
	if !validPolicy(invalid) {
		return nil, fmt.Errorf("invalid-logs policy %q is invalid, must be one of keep, drop, fail", cfg.InvalidLogs)
	}

	compiled := &compiledConfig{
		apps:          apps,
		bySelector:    make(map[[selectorLength]byte]*eventDef),
		unknownPolicy: unknown,
		invalidPolicy: invalid,
	}
	tables := make(map[string]string)
	for i := range cfg.Events {
		def, err := compileEvent(&cfg.Events[i])
		if err != nil {
			return nil, fmt.Errorf("invalid event definition at index %d: %w", i, err)
		}
		if prev, ok := tables[def.table]; ok {
			return nil, fmt.Errorf("event %q and event %q map to the same table %q", prev, def.name, def.table)
		}
		tables[def.table] = def.name
		if prev, ok := compiled.bySelector[def.selector]; ok {
			return nil, fmt.Errorf("events %q and %q have the same 4 byte marker 0x%x", prev.name, def.name, def.selector)
		}
		compiled.bySelector[def.selector] = def
		compiled.events = append(compiled.events, def)
	}
	return compiled, nil
}

// eventTables returns the names of all per-round tables: one per event plus
// the unmatched log table.
func (c *compiledConfig) eventTables() []string {
	tables := make([]string, 0, len(c.events)+1)
	for _, def := range c.events {
		tables = append(tables, def.table)
	}
	return append(tables, unmatchedTableName)
}
