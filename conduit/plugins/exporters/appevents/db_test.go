package appevents

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func compileTestDef(t *testing.T, cfg *EventConfig) *eventDef {
	t.Helper()
	def, err := compileEvent(cfg)
	require.NoError(t, err)
	return def
}

func TestEventTableDDL(t *testing.T) {
	def := compileTestDef(t, &EventConfig{
		Name:      "Transfer",
		Signature: "Transfer(address,address,uint64)",
		Args:      []EventArgConfig{{Name: "sender"}, {Name: "receiver"}, {Name: "amount"}},
	})
	ddl := eventTableDDL(def)
	assert.Contains(t, ddl, `CREATE TABLE IF NOT EXISTS "app_event_transfer"`)
	// Provenance columns with their types.
	assert.Contains(t, ddl, `round BIGINT NOT NULL`)
	assert.Contains(t, ddl, `app_id BIGINT NOT NULL`)
	assert.Contains(t, ddl, `txn_index INTEGER NOT NULL`)
	assert.Contains(t, ddl, `intra_path INTEGER[] NOT NULL`)
	assert.Contains(t, ddl, `log_index INTEGER NOT NULL`)
	assert.Contains(t, ddl, `tx_id TEXT NOT NULL`)
	assert.Contains(t, ddl, `log_bytes BYTEA NOT NULL`)
	// Typed argument columns.
	assert.Contains(t, ddl, `"arg_sender" TEXT NOT NULL`)
	assert.Contains(t, ddl, `"arg_receiver" TEXT NOT NULL`)
	assert.Contains(t, ddl, `"arg_amount" NUMERIC(20,0) NOT NULL`)
	// Identity for replay safety.
	assert.Contains(t, ddl, `PRIMARY KEY (round, txn_index, intra_path, log_index)`)
}

func TestEventTableDDLNoArgs(t *testing.T) {
	def := compileTestDef(t, &EventConfig{Name: "ping", Signature: "Ping()"})
	ddl := eventTableDDL(def)
	assert.Contains(t, ddl, `"app_event_ping"`)
	assert.NotContains(t, ddl, "arg_")
}

func TestMetaAndUnmatchedDDL(t *testing.T) {
	meta := metaTableDDL()
	assert.Contains(t, meta, metaTableName)
	assert.Contains(t, meta, "next_round BIGINT NOT NULL")

	unmatched := unmatchedTableDDL()
	assert.Contains(t, unmatched, unmatchedTableName)
	assert.Contains(t, unmatched, "log_bytes BYTEA NOT NULL")
	assert.Contains(t, unmatched, "marker BYTEA")
	assert.Contains(t, unmatched, "reason TEXT NOT NULL")
	assert.Contains(t, unmatched, "PRIMARY KEY (round, txn_index, intra_path, log_index)")
}

func TestSchemaStatements(t *testing.T) {
	events := []*eventDef{
		compileTestDef(t, &EventConfig{Name: "a", Signature: "A(uint64)"}),
		compileTestDef(t, &EventConfig{Name: "b", Signature: "B(string)"}),
	}
	stmts := schemaStatements(events)
	require.Len(t, stmts, 4)
	assert.Contains(t, stmts[0], metaTableName)
	assert.Contains(t, stmts[1], unmatchedTableName)
	assert.Contains(t, stmts[2], `"app_event_a"`)
	assert.Contains(t, stmts[3], `"app_event_b"`)
}

func TestDeleteRoundStatements(t *testing.T) {
	stmts := deleteRoundStatements([]string{"app_event_a", unmatchedTableName}, 77)
	require.Len(t, stmts, 2)
	assert.Equal(t, `DELETE FROM "app_event_a" WHERE round = $1`, stmts[0].sql)
	assert.Equal(t, []interface{}{int64(77)}, stmts[0].args)
	assert.Equal(t, `DELETE FROM "app_events_unmatched" WHERE round = $1`, stmts[1].sql)
}

func TestInsertEventStatement(t *testing.T) {
	def := compileTestDef(t, &EventConfig{
		Name:      "e",
		Signature: "E(uint64,string)",
	})
	rec := &logRecord{
		round:     12,
		appID:     34,
		txnIndex:  2,
		intraPath: []int32{1, 0},
		logIndex:  1,
		txID:      "TXID",
		raw:       []byte{1, 2, 3},
	}
	stmt := insertEventStatement(def, rec, []interface{}{int64(9), "hi"})
	assert.Equal(t, `INSERT INTO "app_event_e" (round, app_id, txn_index, intra_path, log_index, tx_id, log_bytes, "arg_0", "arg_1") VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, stmt.sql)
	require.Len(t, stmt.args, 9)
	assert.Equal(t, int64(12), stmt.args[0])
	assert.Equal(t, int64(34), stmt.args[1])
	assert.Equal(t, int32(2), stmt.args[2])
	assert.Equal(t, []int32{1, 0}, stmt.args[3])
	assert.Equal(t, int32(1), stmt.args[4])
	assert.Equal(t, "TXID", stmt.args[5])
	assert.Equal(t, []byte{1, 2, 3}, stmt.args[6])
	assert.Equal(t, int64(9), stmt.args[7])
	assert.Equal(t, "hi", stmt.args[8])
}

func TestInsertEventStatementTopLevelPath(t *testing.T) {
	// The intra path of a top-level transaction is an empty array, not NULL.
	rec := &logRecord{round: 1}
	args := provenanceArgs(rec)
	assert.Equal(t, []int32{}, args[3])
}

func TestInsertUnmatchedStatement(t *testing.T) {
	rec := &logRecord{round: 3, appID: 4, raw: []byte{9}}
	stmt := insertUnmatchedStatement(rec, []byte{1, 2, 3, 4}, "unknown_marker")
	assert.Equal(t, `INSERT INTO app_events_unmatched (round, app_id, txn_index, intra_path, log_index, tx_id, log_bytes, marker, reason) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, stmt.sql)
	require.Len(t, stmt.args, 9)
	assert.Equal(t, []byte{1, 2, 3, 4}, stmt.args[7])
	assert.Equal(t, "unknown_marker", stmt.args[8])
}

func TestUpsertProgressStatement(t *testing.T) {
	stmt := upsertProgressStatement(55)
	assert.Contains(t, stmt.sql, "INSERT INTO "+metaTableName)
	assert.Contains(t, stmt.sql, "ON CONFLICT (id) DO UPDATE")
	assert.Equal(t, []interface{}{int64(55)}, stmt.args)
}

func TestPlaceholders(t *testing.T) {
	assert.Equal(t, "$1, $2, $3", placeholders(1, 3))
	assert.Equal(t, "$4, $5", placeholders(4, 2))
	assert.Equal(t, "", placeholders(1, 0))
}

func TestQuoteIdent(t *testing.T) {
	assert.Equal(t, `"app_event_transfer"`, quoteIdent("app_event_transfer"))
	assert.Equal(t, `"a""b"`, quoteIdent(`a"b`))
}
