package appevents

import (
	"context"
	_ "embed" // used to embed config
	"fmt"
	"io"

	"github.com/sirupsen/logrus"

	"github.com/algorand/conduit/conduit"
	"github.com/algorand/conduit/conduit/data"
	"github.com/algorand/conduit/conduit/plugins"
	"github.com/algorand/conduit/conduit/plugins/exporters"
)

// PluginName to use when configuring.
const PluginName = "app_events"

// dbConnector opens a database handle. It is a field so tests can inject a
// fake database.
type dbConnector func(ctx context.Context, connString string, maxConn uint32) (dbRunner, error)

type appEventsExporter struct {
	round  uint64
	cfg    *compiledConfig
	db     dbRunner
	logger *logrus.Logger
	ctx    context.Context
	cf     context.CancelFunc
	// connect creates the database handle; nil uses connectPostgres.
	connect dbConnector
}

//go:embed sample.yaml
var sampleConfig string

var metadata = plugins.Metadata{
	Name:         PluginName,
	Description:  "Exporter for decoding application logs into queryable event tables in a postgresql instance.",
	Deprecated:   false,
	SampleConfig: sampleConfig,
}

func (exp *appEventsExporter) Metadata() plugins.Metadata {
	return metadata
}

// connectDB unmarshals and compiles the configuration and opens the database.
func connectDB(ctx context.Context, connector dbConnector, cfg plugins.PluginConfig) (dbRunner, *compiledConfig, error) {
	var eCfg ExporterConfig
	if err := cfg.UnmarshalConfig(&eCfg); err != nil {
		return nil, nil, fmt.Errorf("connect failure in unmarshalConfig: %w", err)
	}
	compiled, err := compileConfig(&eCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("connect failure in compileConfig: %w", err)
	}
	if connector == nil {
		connector = connectPostgres
	}
	db, err := connector(ctx, eCfg.ConnectionString, eCfg.MaxConn)
	if err != nil {
		return nil, nil, fmt.Errorf("connect failure constructing db: %w", err)
	}
	return db, compiled, nil
}

// RoundRequest implements the RoundRequestor interface to provide the next
// round stored in the database. It connects to the database, queries the
// round, and closes the connection. If there is a problem with the
// configuration, an error will be returned in the Init phase and this
// function will return 0.
func (exp *appEventsExporter) RoundRequest(cfg plugins.PluginConfig) (uint64, error) {
	nullLogger := logrus.New()
	nullLogger.Out = io.Discard // no logging

	db, _, err := connectDB(context.Background(), exp.connect, cfg)
	if err != nil {
		// Assume the error is related to an uninitialized DB.
		// If it is something more serious, the failure will be detected during Init.
		return 0, nil
	}
	defer db.close()

	round, found, err := db.nextRound(context.Background())
	if err != nil {
		return 0, fmt.Errorf("%s.RoundRequest(): failed to get next round: %w", PluginName, err)
	}
	if !found {
		return 0, nil
	}
	return round, nil
}

func (exp *appEventsExporter) Init(ctx context.Context, initProvider data.InitProvider, cfg plugins.PluginConfig, logger *logrus.Logger) error {
	exp.ctx, exp.cf = context.WithCancel(ctx)
	exp.logger = logger

	db, compiled, err := connectDB(exp.ctx, exp.connect, cfg)
	if err != nil {
		return err
	}
	exp.cfg = compiled

	// Create the schema: one table per event, the unmatched log table and
	// the progress table.
	if err = db.exec(exp.ctx, schemaStatements(compiled.events)...); err != nil {
		db.close()
		return fmt.Errorf("error creating schema: %w", err)
	}

	target := uint64(initProvider.NextDBRound())
	stored, found, err := db.nextRound(exp.ctx)
	if err != nil {
		db.close()
		return fmt.Errorf("error getting next db round: %w", err)
	}
	switch {
	case !found:
		if err = db.initNextRound(exp.ctx, target); err != nil {
			db.close()
			return fmt.Errorf("error initializing next db round: %w", err)
		}
	case target > stored:
		db.close()
		return fmt.Errorf("initializing block round %d but next round in db is %d", target, stored)
	case target < stored:
		// Replaying already exported rounds is safe: each round replaces its
		// own rows inside the same transaction that advances the progress.
		logger.Warnf("replaying rounds [%d, %d): events for these rounds will be replaced, not duplicated", target, stored)
	}

	exp.db = db
	exp.round = target
	return nil
}

func (exp *appEventsExporter) Close() error {
	if exp.db != nil {
		exp.db.close()
	}
	if exp.cf != nil {
		exp.cf()
	}
	return nil
}

func (exp *appEventsExporter) Receive(exportData data.BlockData) error {
	if exp.db == nil {
		return fmt.Errorf("exporter not initialized")
	}
	round := exportData.Round()
	if round != exp.round {
		return fmt.Errorf("Receive(): wrong block: received round %d, expected round %d", round, exp.round)
	}

	stmts, err := exp.buildRoundStatements(exportData)
	if err != nil {
		return err
	}
	// The round's events and the progress update commit atomically: a round
	// is either fully exported or not exported at all. Rounds without logs
	// still advance the progress.
	if err = exp.db.runInTx(exp.ctx, stmts); err != nil {
		return fmt.Errorf("Receive(): failed to export round %d: %w", round, err)
	}
	exp.round++
	return nil
}

// buildRoundStatements computes the statements exporting one round: first
// delete anything previously written for the round, then insert the decoded
// events (and kept unmatched logs), then advance the progress.
func (exp *appEventsExporter) buildRoundStatements(blk data.BlockData) ([]statement, error) {
	records := extractLogs(blk, exp.cfg.apps)
	stmts := deleteRoundStatements(exp.cfg.eventTables(), blk.Round())
	for i := range records {
		recStmts, err := exp.recordStatements(&records[i])
		if err != nil {
			return nil, err
		}
		stmts = append(stmts, recStmts...)
	}
	stmts = append(stmts, upsertProgressStatement(blk.Round()+1))
	return stmts, nil
}

// recordStatements classifies a single log and returns its insert
// statements, applying the configured policies for unknown markers and
// undecodable payloads.
func (exp *appEventsExporter) recordStatements(rec *logRecord) ([]statement, error) {
	var selector [selectorLength]byte
	var def *eventDef
	matched := false
	if len(rec.raw) >= selectorLength {
		copy(selector[:], rec.raw[:selectorLength])
		def, matched = exp.cfg.bySelector[selector]
	}

	if matched {
		values, err := def.decodeArgs(rec.raw[selectorLength:])
		if err == nil {
			return []statement{insertEventStatement(def, rec, values)}, nil
		}
		reason := fmt.Sprintf("decode_error: %s", err.Error())
		switch exp.cfg.invalidPolicy {
		case PolicyDrop:
			exp.logger.Debugf("dropping undecodable log for event %q at round %d txn %d: %s", def.name, rec.round, rec.txnIndex, reason)
			return nil, nil
		case PolicyFail:
			return nil, fmt.Errorf("unable to decode log for event %q at round %d txn %d log %d: %w", def.name, rec.round, rec.txnIndex, rec.logIndex, err)
		default: // PolicyKeep
			return []statement{insertUnmatchedStatement(rec, selector[:], reason)}, nil
		}
	}

	reason := "unknown_marker"
	var marker []byte
	if len(rec.raw) < selectorLength {
		reason = "log_too_short"
	} else {
		marker = selector[:]
	}
	switch exp.cfg.unknownPolicy {
	case PolicyDrop:
		exp.logger.Debugf("dropping log with %s at round %d txn %d", reason, rec.round, rec.txnIndex)
		return nil, nil
	case PolicyFail:
		return nil, fmt.Errorf("log with %s at round %d txn %d log %d", reason, rec.round, rec.txnIndex, rec.logIndex)
	default: // PolicyKeep
		return []statement{insertUnmatchedStatement(rec, marker, reason)}, nil
	}
}

func init() {
	exporters.Register(PluginName, exporters.ExporterConstructorFunc(func() exporters.Exporter {
		return &appEventsExporter{}
	}))
}

// interface guards
var (
	_ exporters.Exporter     = (*appEventsExporter)(nil)
	_ conduit.RoundRequestor = (*appEventsExporter)(nil)
)
