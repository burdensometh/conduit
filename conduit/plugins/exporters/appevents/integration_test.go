package appevents

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdk "github.com/algorand/go-algorand-sdk/v2/types"

	"github.com/algorand/conduit/conduit"
	"github.com/algorand/conduit/conduit/plugins"
)

// pgConnString returns the integration test connection string, or skips the
// test. Set APP_EVENTS_TEST_PG_CONN to run these tests, e.g.
//
//	APP_EVENTS_TEST_PG_CONN="host=localhost port=5432 user=postgres dbname=postgres" go test ./conduit/plugins/exporters/appevents/
func pgConnString(t *testing.T) string {
	t.Helper()
	conn := os.Getenv("APP_EVENTS_TEST_PG_CONN")
	if conn == "" {
		t.Skip("APP_EVENTS_TEST_PG_CONN not set, skipping postgres integration test")
	}
	return conn
}

const integrationConfig = `
connection-string: "%s"
app-ids: [1234]
events:
  - name: transfer
    signature: "Transfer(address,address,uint64)"
    args:
      - name: sender
      - name: receiver
      - name: amount
  - name: note
    signature: "Note(string,bool,byte[])"
    args:
      - name: message
      - name: flag
      - name: blob
`

// resetSchema drops the exporter tables so the test starts from a clean
// slate.
func resetSchema(t *testing.T, ctx context.Context, conn string) {
	t.Helper()
	pool, err := pgxpool.Connect(ctx, conn)
	require.NoError(t, err)
	defer pool.Close()
	for _, table := range []string{metaTableName, unmatchedTableName, "app_event_transfer", "app_event_note"} {
		_, err = pool.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", table))
		require.NoError(t, err)
	}
}

func initRealExporter(t *testing.T, conn string, round uint64) *appEventsExporter {
	t.Helper()
	exp := &appEventsExporter{}
	sdkRound := sdk.Round(round)
	err := exp.Init(context.Background(), conduit.MakePipelineInitProvider(&sdkRound, &sdk.Genesis{}, nil),
		plugins.MakePluginConfig(fmt.Sprintf(integrationConfig, conn)), testLogger)
	require.NoError(t, err)
	return exp
}

// TestIntegrationPostgres exercises the exporter against a real postgres
// database and verifies the exported data with actual SQL queries.
func TestIntegrationPostgres(t *testing.T) {
	conn := pgConnString(t)
	ctx := context.Background()
	resetSchema(t, ctx, conn)

	pool, err := pgxpool.Connect(ctx, conn)
	require.NoError(t, err)
	defer pool.Close()

	exp := initRealExporter(t, conn, 100)
	defer func() { assert.NoError(t, exp.Close()) }()

	// Round 100: a top-level transfer event, an inner transfer event, a note
	// event, an unknown marker, a bad payload and a log from an unwatched app.
	sender := make([]byte, 32)
	sender[0] = 1
	receiver := make([]byte, 32)
	receiver[0] = 2
	topLog := transferLog(t, sender, receiver, 18446744073709551615) // uint64 max

	innerSender := make([]byte, 32)
	innerSender[0] = 3
	innerLog := transferLog(t, innerSender, receiver, 42)

	noteDef, err := compileEvent(&EventConfig{Name: "note", Signature: "Note(string,bool,byte[])"})
	require.NoError(t, err)
	notePayload := encodeArgs(t, []string{"string", "bool", "byte[]"}, "hello world", true, []byte{0xca, 0xfe})
	noteLog := string(append(noteDef.selector[:], notePayload...))

	transferDef, err := compileEvent(&EventConfig{Name: "transfer", Signature: "Transfer(address,address,uint64)"})
	require.NoError(t, err)
	badLog := string(append(transferDef.selector[:], 0x00, 0x01))

	inner := makeApplTxn(testAppID, []string{innerLog})
	top := makeApplTxn(testAppID, []string{topLog, noteLog, string([]byte{0xde, 0xad, 0xbe, 0xef}), badLog}, inner)
	other := makeApplTxn(otherAppID, []string{"ignored"})
	require.NoError(t, exp.Receive(makeBlock(100, wrapInBlock(top), wrapInBlock(other))))

	// Round 101: no logs at all, progress must still advance.
	require.NoError(t, exp.Receive(makeBlock(101)))

	// --- schema: columns exist with the correct SQL types ---
	columnTypes := make(map[string]string)
	rows, err := pool.Query(ctx, `SELECT column_name, data_type FROM information_schema.columns WHERE table_name = 'app_event_transfer'`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var name, dataType string
		require.NoError(t, rows.Scan(&name, &dataType))
		columnTypes[name] = dataType
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, map[string]string{
		"round":        "bigint",
		"app_id":       "bigint",
		"txn_index":    "integer",
		"intra_path":   "ARRAY",
		"log_index":    "integer",
		"tx_id":        "text",
		"log_bytes":    "bytea",
		"arg_sender":   "text",
		"arg_receiver": "text",
		"arg_amount":   "numeric",
	}, columnTypes)

	// --- events: typed values and provenance come back from real queries ---
	var (
		round, appID             int64
		txnIndex, logIndex       int32
		intraPath                []int32
		txID, argSender, argRecv string
		logBytes                 []byte
		argAmount                string
	)
	err = pool.QueryRow(ctx, `
		SELECT round, app_id, txn_index, intra_path, log_index, tx_id, log_bytes,
		       arg_sender, arg_receiver, arg_amount::text
		FROM app_event_transfer
		WHERE round = 100 AND txn_index = 0 AND intra_path = '{}'`).Scan(
		&round, &appID, &txnIndex, &intraPath, &logIndex, &txID, &logBytes, &argSender, &argRecv, &argAmount)
	require.NoError(t, err)
	assert.Equal(t, int64(100), round)
	assert.Equal(t, int64(testAppID), appID)
	assert.Equal(t, int32(0), txnIndex)
	assert.Empty(t, intraPath)
	assert.Equal(t, int32(0), logIndex)
	assert.Len(t, txID, 52) // canonical transaction id
	assert.Equal(t, []byte(topLog), logBytes)
	var expectedSender, expectedReceiver sdk.Address
	copy(expectedSender[:], sender)
	copy(expectedReceiver[:], receiver)
	assert.Equal(t, expectedSender.String(), argSender)
	assert.Equal(t, expectedReceiver.String(), argRecv)
	// uint64 max survives exactly through the NUMERIC column.
	assert.Equal(t, "18446744073709551615", argAmount)

	// The amount column behaves like a number in SQL.
	var numericType string
	require.NoError(t, pool.QueryRow(ctx, `SELECT pg_typeof(arg_amount) FROM app_event_transfer LIMIT 1`).Scan(&numericType))
	assert.Equal(t, "numeric", numericType)
	var sum string
	require.NoError(t, pool.QueryRow(ctx, `SELECT sum(arg_amount)::text FROM app_event_transfer`).Scan(&sum))
	assert.Equal(t, "18446744073709551657", sum) // max + 42

	// The inner transaction event carries its inner path and the top-level
	// transaction id.
	var innerAmount uint64
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT arg_amount FROM app_event_transfer
		WHERE round = 100 AND intra_path = '{0}' AND tx_id = $1`, txID).Scan(&innerAmount))
	assert.Equal(t, uint64(42), innerAmount)

	// The note event decodes string, bool and bytea columns.
	var (
		message string
		flag    bool
		blob    []byte
	)
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT arg_message, arg_flag, arg_blob FROM app_event_note WHERE round = 100`).Scan(&message, &flag, &blob))
	assert.Equal(t, "hello world", message)
	assert.True(t, flag)
	assert.Equal(t, []byte{0xca, 0xfe}, blob)

	// --- unmatched logs: kept with their reason and provenance ---
	type unmatchedRow struct {
		logIndex int32
		marker   []byte
		reason   string
	}
	var unmatched []unmatchedRow
	urows, err := pool.Query(ctx, `SELECT log_index, marker, reason FROM app_events_unmatched WHERE round = 100 ORDER BY log_index`)
	require.NoError(t, err)
	defer urows.Close()
	for urows.Next() {
		var row unmatchedRow
		require.NoError(t, urows.Scan(&row.logIndex, &row.marker, &row.reason))
		unmatched = append(unmatched, row)
	}
	require.NoError(t, urows.Err())
	require.Len(t, unmatched, 2)
	assert.Equal(t, int32(2), unmatched[0].logIndex)
	assert.Equal(t, []byte{0xde, 0xad, 0xbe, 0xef}, unmatched[0].marker)
	assert.Equal(t, "unknown_marker", unmatched[0].reason)
	assert.Equal(t, int32(3), unmatched[1].logIndex)
	assert.Contains(t, unmatched[1].reason, "decode_error")

	// The unwatched app's log was ignored entirely.
	var totalRows int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM app_event_transfer`).Scan(&totalRows))
	assert.Equal(t, 2, totalRows)

	// --- progress: the empty round advanced the next round ---
	var nextRound uint64
	require.NoError(t, pool.QueryRow(ctx, `SELECT next_round FROM app_events_meta WHERE id = 1`).Scan(&nextRound))
	assert.Equal(t, uint64(102), nextRound)

	// RoundRequest agrees with the stored progress.
	rr, err := (&appEventsExporter{}).RoundRequest(plugins.MakePluginConfig(fmt.Sprintf(integrationConfig, conn)))
	require.NoError(t, err)
	assert.Equal(t, uint64(102), rr)

	// --- replay: rewinding and re-exporting a round does not duplicate events ---
	_, err = pool.Exec(ctx, `UPDATE app_events_meta SET next_round = 100 WHERE id = 1`)
	require.NoError(t, err)
	replayExp := initRealExporter(t, conn, 100)
	require.NoError(t, replayExp.Receive(makeBlock(100, wrapInBlock(top), wrapInBlock(other))))
	require.NoError(t, replayExp.Receive(makeBlock(101)))
	assert.NoError(t, replayExp.Close())

	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM app_event_transfer`).Scan(&totalRows))
	assert.Equal(t, 2, totalRows)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM app_event_note`).Scan(&totalRows))
	assert.Equal(t, 1, totalRows)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM app_events_unmatched`).Scan(&totalRows))
	assert.Equal(t, 2, totalRows)
	require.NoError(t, pool.QueryRow(ctx, `SELECT next_round FROM app_events_meta WHERE id = 1`).Scan(&nextRound))
	assert.Equal(t, uint64(102), nextRound)
}
