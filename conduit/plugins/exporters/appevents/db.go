package appevents

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgconn"
	"github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/pgxpool"
)

const (
	// metaTableName tracks the exporter's progress: the next round to export.
	metaTableName = "app_events_meta"
	// unmatchedTableName holds logs kept by the keep policies.
	unmatchedTableName = "app_events_unmatched"
	// eventTablePrefix prefixes every per-event table name.
	eventTablePrefix = "app_event_"
)

// pgUndefinedTable is the postgres error code for a missing relation.
const pgUndefinedTable = "42P01"

// statement is a single parameterized SQL statement.
type statement struct {
	sql  string
	args []interface{}
}

// dbRunner is the database handle used by the exporter. It is an interface
// so tests can substitute a fake.
type dbRunner interface {
	// runInTx executes all statements atomically in a single transaction.
	runInTx(ctx context.Context, stmts []statement) error
	// exec runs each statement in autocommit mode (used for DDL).
	exec(ctx context.Context, stmts ...string) error
	// nextRound returns the stored next round; found is false when no
	// progress has been recorded yet.
	nextRound(ctx context.Context) (round uint64, found bool, err error)
	// initNextRound records the initial next round.
	initNextRound(ctx context.Context, round uint64) error
	// close releases the database resources.
	close()
}

// pgxRunner is the postgres implementation of dbRunner.
type pgxRunner struct {
	pool *pgxpool.Pool
}

// connectPostgres opens a pgx connection pool.
func connectPostgres(ctx context.Context, connString string, maxConn uint32) (dbRunner, error) {
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("unable to parse connection string: %w", err)
	}
	if maxConn > 0 {
		cfg.MaxConns = int32(maxConn)
	}
	pool, err := pgxpool.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to postgres: %w", err)
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("unable to ping postgres: %w", err)
	}
	return &pgxRunner{pool: pool}, nil
}

func (db *pgxRunner) runInTx(ctx context.Context, stmts []statement) error {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("unable to begin transaction: %w", err)
	}
	defer func() {
		// Rollback is a no-op once the transaction has been committed.
		_ = tx.Rollback(ctx)
	}()
	for _, stmt := range stmts {
		if _, err = tx.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			return fmt.Errorf("unable to execute statement %q: %w", stmt.sql, err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("unable to commit transaction: %w", err)
	}
	return nil
}

func (db *pgxRunner) exec(ctx context.Context, stmts ...string) error {
	for _, sql := range stmts {
		if _, err := db.pool.Exec(ctx, sql); err != nil {
			return fmt.Errorf("unable to execute statement %q: %w", sql, err)
		}
	}
	return nil
}

func (db *pgxRunner) nextRound(ctx context.Context) (uint64, bool, error) {
	var round uint64
	query := fmt.Sprintf(`SELECT next_round FROM %s WHERE id = 1`, metaTableName)
	err := db.pool.QueryRow(ctx, query).Scan(&round)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUndefinedTable {
			// The schema has not been created yet.
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("unable to query next round: %w", err)
	}
	return round, true, nil
}

func (db *pgxRunner) initNextRound(ctx context.Context, round uint64) error {
	return db.runInTx(ctx, []statement{upsertProgressStatement(round)})
}

func (db *pgxRunner) close() {
	db.pool.Close()
}

// metaTableDDL creates the singleton progress table.
func metaTableDDL() string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
	id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
	next_round BIGINT NOT NULL
)`, metaTableName)
}

// unmatchedTableDDL creates the table holding logs kept by the keep policies.
func unmatchedTableDDL() string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
	round BIGINT NOT NULL,
	app_id BIGINT NOT NULL,
	txn_index INTEGER NOT NULL,
	intra_path INTEGER[] NOT NULL,
	log_index INTEGER NOT NULL,
	tx_id TEXT NOT NULL,
	log_bytes BYTEA NOT NULL,
	marker BYTEA,
	reason TEXT NOT NULL,
	PRIMARY KEY (round, txn_index, intra_path, log_index)
)`, unmatchedTableName)
}

// eventTableDDL creates the table for a single event. Every configured
// argument becomes a typed column; provenance columns are shared by all
// event tables.
func eventTableDDL(def *eventDef) string {
	columns := []string{
		`round BIGINT NOT NULL`,
		`app_id BIGINT NOT NULL`,
		`txn_index INTEGER NOT NULL`,
		`intra_path INTEGER[] NOT NULL`,
		`log_index INTEGER NOT NULL`,
		`tx_id TEXT NOT NULL`,
		`log_bytes BYTEA NOT NULL`,
	}
	for _, arg := range def.args {
		columns = append(columns, fmt.Sprintf(`%s %s NOT NULL`, quoteIdent(arg.colName), arg.sqlType))
	}
	columns = append(columns, `PRIMARY KEY (round, txn_index, intra_path, log_index)`)
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
	%s
)`, quoteIdent(def.table), strings.Join(columns, ",\n\t"))
}

// schemaStatements returns the DDL creating the full exporter schema.
func schemaStatements(events []*eventDef) []string {
	stmts := make([]string, 0, len(events)+2)
	stmts = append(stmts, metaTableDDL(), unmatchedTableDDL())
	for _, def := range events {
		stmts = append(stmts, eventTableDDL(def))
	}
	return stmts
}

// quoteIdent quotes a SQL identifier. Names are validated against a strict
// identifier regexp when the configuration is compiled, so this only needs
// to double any embedded quotes defensively.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// deleteRoundStatements removes any rows previously written for a round.
// Re-executing a round therefore never duplicates events, whether the round
// is replayed after a rewind or delivered again after a fork.
func deleteRoundStatements(tables []string, round uint64) []statement {
	stmts := make([]statement, 0, len(tables))
	for _, table := range tables {
		stmts = append(stmts, statement{
			sql:  fmt.Sprintf(`DELETE FROM %s WHERE round = $1`, quoteIdent(table)),
			args: []interface{}{int64(round)},
		})
	}
	return stmts
}

// intraPathValue converts the inner transaction path to the value stored in
// the INTEGER[] column. The path is never nil so that top-level transactions
// get an empty array rather than NULL.
func intraPathValue(path []int32) []int32 {
	if path == nil {
		return []int32{}
	}
	return path
}

// provenanceArgs are the leading column values shared by event and
// unmatched log rows.
func provenanceArgs(rec *logRecord) []interface{} {
	return []interface{}{
		int64(rec.round),
		int64(rec.appID),
		int32(rec.txnIndex),
		intraPathValue(rec.intraPath),
		int32(rec.logIndex),
		rec.txID,
		rec.raw,
	}
}

// provenanceColumns are the leading columns shared by event and unmatched
// log tables.
const provenanceColumns = `round, app_id, txn_index, intra_path, log_index, tx_id, log_bytes`

// placeholders returns "$n, $n+1, ..." style placeholders for count values
// starting at the 1-based index start.
func placeholders(start, count int) string {
	parts := make([]string, count)
	for i := range parts {
		parts[i] = fmt.Sprintf("$%d", start+i)
	}
	return strings.Join(parts, ", ")
}

// insertEventStatement builds the INSERT for a decoded event row.
func insertEventStatement(def *eventDef, rec *logRecord, argValues []interface{}) statement {
	columns := make([]string, 0, len(def.args)+1)
	columns = append(columns, provenanceColumns)
	for _, arg := range def.args {
		columns = append(columns, quoteIdent(arg.colName))
	}
	args := append(provenanceArgs(rec), argValues...)
	return statement{
		sql: fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`,
			quoteIdent(def.table), strings.Join(columns, ", "), placeholders(1, len(args))),
		args: args,
	}
}

// insertUnmatchedStatement builds the INSERT for a kept but undecoded log.
func insertUnmatchedStatement(rec *logRecord, marker []byte, reason string) statement {
	args := append(provenanceArgs(rec), marker, reason)
	return statement{
		sql: fmt.Sprintf(`INSERT INTO %s (%s, marker, reason) VALUES (%s)`,
			unmatchedTableName, provenanceColumns, placeholders(1, len(args))),
		args: args,
	}
}

// upsertProgressStatement advances the stored next round. It runs in the
// same transaction as the round's event rows.
func upsertProgressStatement(nextRound uint64) statement {
	return statement{
		sql: fmt.Sprintf(`INSERT INTO %s (id, next_round) VALUES (1, $1)
	ON CONFLICT (id) DO UPDATE SET next_round = EXCLUDED.next_round`, metaTableName),
		args: []interface{}{int64(nextRound)},
	}
}
