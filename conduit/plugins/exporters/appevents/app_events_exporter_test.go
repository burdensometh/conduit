package appevents

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgtype"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdk "github.com/algorand/go-algorand-sdk/v2/types"

	"github.com/algorand/conduit/conduit"
	"github.com/algorand/conduit/conduit/data"
	"github.com/algorand/conduit/conduit/plugins"
	"github.com/algorand/conduit/conduit/plugins/exporters"
)

var testLogger *logrus.Logger

func init() {
	testLogger, _ = test.NewNullLogger()
}

// fakeDB is an in-memory dbRunner recording everything the exporter does.
type fakeDB struct {
	mu             sync.Mutex
	ddl            []string
	batches        [][]statement
	nextRoundVal   uint64
	nextRoundFound bool
	runInTxErr     error
	execErr        error
}

func (f *fakeDB) runInTx(_ context.Context, stmts []statement) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runInTxErr != nil {
		return f.runInTxErr
	}
	batch := make([]statement, len(stmts))
	copy(batch, stmts)
	f.batches = append(f.batches, batch)
	// Simulate the progress table.
	for _, stmt := range stmts {
		if strings.HasPrefix(stmt.sql, "INSERT INTO "+metaTableName) {
			f.nextRoundVal = uint64(stmt.args[0].(int64)) //nolint:errcheck
			f.nextRoundFound = true
		}
	}
	return nil
}

func (f *fakeDB) exec(_ context.Context, stmts ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.execErr != nil {
		return f.execErr
	}
	f.ddl = append(f.ddl, stmts...)
	return nil
}

func (f *fakeDB) nextRound(_ context.Context) (uint64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nextRoundVal, f.nextRoundFound, nil
}

func (f *fakeDB) initNextRound(_ context.Context, round uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextRoundVal = round
	f.nextRoundFound = true
	return nil
}

func (f *fakeDB) close() {}

// allStatements flattens the recorded batches.
func (f *fakeDB) allStatements() []statement {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []statement
	for _, batch := range f.batches {
		out = append(out, batch...)
	}
	return out
}

const testConfig = `
connection-string: "fake"
app-ids: [1234]
events:
  - name: transfer
    signature: "Transfer(address,address,uint64)"
    args:
      - name: sender
      - name: receiver
      - name: amount
`

// newTestExporter builds an exporter wired to a fresh fake DB.
func newTestExporter(t *testing.T, cfg string, round uint64) (*appEventsExporter, *fakeDB) {
	t.Helper()
	db := &fakeDB{}
	exp := &appEventsExporter{
		connect: func(_ context.Context, _ string, _ uint32) (dbRunner, error) {
			return db, nil
		},
	}
	sdkRound := sdk.Round(round)
	err := exp.Init(context.Background(), conduit.MakePipelineInitProvider(&sdkRound, &sdk.Genesis{}, nil), plugins.MakePluginConfig(cfg), testLogger)
	require.NoError(t, err)
	return exp, db
}

// transferLog builds a log for the configured transfer event.
func transferLog(t *testing.T, sender, receiver []byte, amount uint64) string {
	t.Helper()
	payload := encodeArgs(t, []string{"address", "address", "uint64"}, sender, receiver, amount)
	def, err := compileEvent(&EventConfig{Name: "transfer", Signature: "Transfer(address,address,uint64)"})
	require.NoError(t, err)
	return string(append(def.selector[:], payload...))
}

func TestExporterMetadata(t *testing.T) {
	exp := exporters.ExporterConstructorFunc(func() exporters.Exporter {
		return &appEventsExporter{}
	}).New()
	meta := exp.Metadata()
	assert.Equal(t, PluginName, meta.Name)
	assert.Equal(t, metadata.Description, meta.Description)
	assert.False(t, meta.Deprecated)
	assert.Contains(t, meta.SampleConfig, "app-ids")
}

func TestExporterRegistered(t *testing.T) {
	// The package's init function must register the plugin.
	constructor, err := exporters.ExporterConstructorByName(PluginName)
	require.NoError(t, err)
	assert.NotNil(t, constructor.New())
}

func TestInitConfigErrors(t *testing.T) {
	testCases := []struct {
		name    string
		cfg     string
		errPart string
	}{
		{"bad yaml", "'", "unmarshalConfig"},
		{"no connection string", "app-ids: [1]", "connection-string"},
		{"no app ids", "connection-string: 'x'", "app-ids"},
		{"duplicate app ids", "connection-string: 'x'\napp-ids: [1, 1]", "duplicate application id"},
		{"bad unknown policy", "connection-string: 'x'\napp-ids: [1]\nunknown-logs: bogus", "unknown-logs"},
		{"bad invalid policy", "connection-string: 'x'\napp-ids: [1]\ninvalid-logs: bogus", "invalid-logs"},
		{"bad event", "connection-string: 'x'\napp-ids: [1]\nevents: [{name: e}]", "invalid event definition"},
		{"duplicate marker", "connection-string: 'x'\napp-ids: [1]\nevents:\n  - name: a\n    signature: 'E(uint64)'\n  - name: b\n    signature: 'E(uint64)'", "same 4 byte marker"},
		{"duplicate table", "connection-string: 'x'\napp-ids: [1]\nevents:\n  - name: Foo\n    signature: 'A(uint64)'\n  - name: foo\n    signature: 'B(uint64)'", "same table"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			exp := &appEventsExporter{
				connect: func(_ context.Context, _ string, _ uint32) (dbRunner, error) {
					return &fakeDB{}, nil
				},
			}
			sdkRound := sdk.Round(0)
			err := exp.Init(context.Background(), conduit.MakePipelineInitProvider(&sdkRound, &sdk.Genesis{}, nil), plugins.MakePluginConfig(tc.cfg), testLogger)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errPart)
		})
	}
}

func TestInitCreatesSchemaAndProgress(t *testing.T) {
	exp, db := newTestExporter(t, testConfig, 10)
	defer func() { assert.NoError(t, exp.Close()) }()

	// Schema: meta table, unmatched table and the event table.
	require.Len(t, db.ddl, 3)
	assert.Contains(t, db.ddl[0], metaTableName)
	assert.Contains(t, db.ddl[1], unmatchedTableName)
	assert.Contains(t, db.ddl[2], `"app_event_transfer"`)
	assert.Contains(t, db.ddl[2], `"arg_sender" TEXT`)
	assert.Contains(t, db.ddl[2], `"arg_amount" NUMERIC(20,0)`)

	// Progress initialized to the pipeline round.
	assert.True(t, db.nextRoundFound)
	assert.Equal(t, uint64(10), db.nextRoundVal)
	assert.Equal(t, uint64(10), exp.round)
}

func TestInitExistingProgress(t *testing.T) {
	// Matching progress: ok.
	db := &fakeDB{nextRoundVal: 7, nextRoundFound: true}
	exp := &appEventsExporter{
		connect: func(_ context.Context, _ string, _ uint32) (dbRunner, error) { return db, nil },
	}
	sdkRound := sdk.Round(7)
	require.NoError(t, exp.Init(context.Background(), conduit.MakePipelineInitProvider(&sdkRound, &sdk.Genesis{}, nil), plugins.MakePluginConfig(testConfig), testLogger))
	assert.Equal(t, uint64(7), exp.round)

	// Pipeline ahead of the db: gap, must fail.
	expGap := &appEventsExporter{
		connect: func(_ context.Context, _ string, _ uint32) (dbRunner, error) {
			return &fakeDB{nextRoundVal: 3, nextRoundFound: true}, nil
		},
	}
	sdkRound = sdk.Round(5)
	err := expGap.Init(context.Background(), conduit.MakePipelineInitProvider(&sdkRound, &sdk.Genesis{}, nil), plugins.MakePluginConfig(testConfig), testLogger)
	require.ErrorContains(t, err, "initializing block round 5 but next round in db is 3")

	// Pipeline behind the db: replay, allowed.
	expReplay := &appEventsExporter{
		connect: func(_ context.Context, _ string, _ uint32) (dbRunner, error) {
			return &fakeDB{nextRoundVal: 9, nextRoundFound: true}, nil
		},
	}
	sdkRound = sdk.Round(5)
	require.NoError(t, expReplay.Init(context.Background(), conduit.MakePipelineInitProvider(&sdkRound, &sdk.Genesis{}, nil), plugins.MakePluginConfig(testConfig), testLogger))
	assert.Equal(t, uint64(5), expReplay.round)
}

func TestReceiveWrongRound(t *testing.T) {
	exp, _ := newTestExporter(t, testConfig, 4)
	defer func() { assert.NoError(t, exp.Close()) }()

	err := exp.Receive(makeBlock(6))
	require.ErrorContains(t, err, "received round 6, expected round 4")
}

func TestReceiveEmptyRoundAdvances(t *testing.T) {
	exp, db := newTestExporter(t, testConfig, 10)
	defer func() { assert.NoError(t, exp.Close()) }()

	// A round with no transactions at all must still commit progress.
	require.NoError(t, exp.Receive(makeBlock(10)))
	assert.Equal(t, uint64(11), exp.round)
	assert.Equal(t, uint64(11), db.nextRoundVal)

	// Exactly one transaction batch: deletes for both tables + progress.
	require.Len(t, db.batches, 1)
	batch := db.batches[0]
	require.Len(t, batch, 3)
	assert.Contains(t, batch[0].sql, `DELETE FROM "app_event_transfer"`)
	assert.Contains(t, batch[1].sql, `DELETE FROM "app_events_unmatched"`)
	assert.Contains(t, batch[2].sql, "INSERT INTO "+metaTableName)
	assert.Equal(t, int64(11), batch[2].args[0])
}

func transferBlock(t *testing.T, round uint64) (data.BlockData, []byte, []byte) {
	t.Helper()
	sender := make([]byte, 32)
	sender[0] = 1
	receiver := make([]byte, 32)
	receiver[0] = 2
	log := transferLog(t, sender, receiver, 123456789)
	txn := makeApplTxn(testAppID, []string{log})
	return makeBlock(round, wrapInBlock(txn)), sender, receiver
}

func TestReceiveDecodesEvent(t *testing.T) {
	exp, db := newTestExporter(t, testConfig, 3)
	defer func() { assert.NoError(t, exp.Close()) }()

	blk, sender, receiver := transferBlock(t, 3)
	require.NoError(t, exp.Receive(blk))
	assert.Equal(t, uint64(4), exp.round)

	require.Len(t, db.batches, 1)
	batch := db.batches[1-1]
	// deletes (2) + event insert + progress
	require.Len(t, batch, 4)
	insert := batch[2]
	assert.Contains(t, insert.sql, `INSERT INTO "app_event_transfer"`)
	assert.Contains(t, insert.sql, `"arg_sender"`)
	assert.Contains(t, insert.sql, `"arg_receiver"`)
	assert.Contains(t, insert.sql, `"arg_amount"`)
	require.Len(t, insert.args, 10)

	// Provenance columns.
	assert.Equal(t, int64(3), insert.args[0])                                // round
	assert.Equal(t, int64(testAppID), insert.args[1])                        // app_id
	assert.Equal(t, int32(0), insert.args[2])                                // txn_index
	assert.Equal(t, []int32{}, insert.args[3])                               // intra_path (top-level)
	assert.Equal(t, int32(0), insert.args[4])                                // log_index
	assert.NotEmpty(t, insert.args[5])                                       // tx_id
	assert.Equal(t, []byte(blk.Payset[0].EvalDelta.Logs[0]), insert.args[6]) // log_bytes

	// Typed argument columns.
	var expectedSender, expectedReceiver sdk.Address
	copy(expectedSender[:], sender)
	copy(expectedReceiver[:], receiver)
	assert.Equal(t, expectedSender.String(), insert.args[7])
	assert.Equal(t, expectedReceiver.String(), insert.args[8])
	amount, ok := insert.args[9].(pgtype.Numeric)
	require.True(t, ok)
	assert.Equal(t, "123456789", amount.Int.String())

	// Progress commits in the same transaction, after the event insert.
	assert.Contains(t, batch[3].sql, "INSERT INTO "+metaTableName)
	assert.Equal(t, int64(4), batch[3].args[0])
}

func TestReceiveInnerTxnEvent(t *testing.T) {
	exp, db := newTestExporter(t, testConfig, 1)
	defer func() { assert.NoError(t, exp.Close()) }()

	sender := make([]byte, 32)
	receiver := make([]byte, 32)
	log := transferLog(t, sender, receiver, 5)
	inner := makeApplTxn(testAppID, []string{log})
	top := makeApplTxn(otherAppID, nil, inner)
	require.NoError(t, exp.Receive(makeBlock(1, wrapInBlock(top))))

	require.Len(t, db.batches, 1)
	batch := db.batches[0]
	require.Len(t, batch, 4)
	insert := batch[2]
	assert.Equal(t, []int32{0}, insert.args[3]) // intra_path: first inner txn
}

func TestReceiveUnknownMarkerKeep(t *testing.T) {
	exp, db := newTestExporter(t, testConfig, 1)
	defer func() { assert.NoError(t, exp.Close()) }()

	txn := makeApplTxn(testAppID, []string{string([]byte{0xde, 0xad, 0xbe, 0xef, 0x00}), "abc"})
	require.NoError(t, exp.Receive(makeBlock(1, wrapInBlock(txn))))

	batch := db.batches[0]
	// deletes (2) + 2 unmatched inserts + progress
	require.Len(t, batch, 5)

	first := batch[2]
	assert.Contains(t, first.sql, "INSERT INTO "+unmatchedTableName)
	assert.Equal(t, []byte{0xde, 0xad, 0xbe, 0xef}, first.args[7]) // marker
	assert.Equal(t, "unknown_marker", first.args[8])               // reason
	assert.Equal(t, []byte{0xde, 0xad, 0xbe, 0xef, 0x00}, first.args[6])

	second := batch[3]
	assert.Nil(t, second.args[7]) // too short for a marker
	assert.Equal(t, "log_too_short", second.args[8])
}

func TestReceiveInvalidLogKeep(t *testing.T) {
	exp, db := newTestExporter(t, testConfig, 1)
	defer func() { assert.NoError(t, exp.Close()) }()

	// Correct marker, garbage payload.
	def, err := compileEvent(&EventConfig{Name: "transfer", Signature: "Transfer(address,address,uint64)"})
	require.NoError(t, err)
	badLog := string(append(def.selector[:], 0x01, 0x02))
	txn := makeApplTxn(testAppID, []string{badLog})
	require.NoError(t, exp.Receive(makeBlock(1, wrapInBlock(txn))))

	batch := db.batches[0]
	require.Len(t, batch, 4)
	insert := batch[2]
	assert.Contains(t, insert.sql, "INSERT INTO "+unmatchedTableName)
	assert.Equal(t, def.selector[:], insert.args[7])
	assert.Contains(t, insert.args[8], "decode_error")
}

func TestReceivePoliciesDropAndFail(t *testing.T) {
	// drop: no rows anywhere, but the round still commits.
	cfg := testConfig + "unknown-logs: drop\ninvalid-logs: drop\n"
	exp, db := newTestExporter(t, cfg, 1)
	defer func() { assert.NoError(t, exp.Close()) }()

	def, err := compileEvent(&EventConfig{Name: "transfer", Signature: "Transfer(address,address,uint64)"})
	require.NoError(t, err)
	badLog := string(append(def.selector[:], 0x01))
	txn := makeApplTxn(testAppID, []string{"unknown-marker-log", badLog})
	require.NoError(t, exp.Receive(makeBlock(1, wrapInBlock(txn))))
	batch := db.batches[0]
	require.Len(t, batch, 3) // deletes + progress only
	assert.Equal(t, uint64(2), db.nextRoundVal)

	// fail: the round is rejected and nothing is written.
	expFail, dbFail := newTestExporter(t, testConfig+"unknown-logs: fail\n", 1)
	defer func() { assert.NoError(t, expFail.Close()) }()
	txn = makeApplTxn(testAppID, []string{"unknown-marker-log"})
	err = expFail.Receive(makeBlock(1, wrapInBlock(txn)))
	require.ErrorContains(t, err, "unknown_marker")
	assert.Empty(t, dbFail.batches)
	assert.Equal(t, uint64(1), expFail.round)

	expFail2, dbFail2 := newTestExporter(t, testConfig+"invalid-logs: fail\n", 1)
	defer func() { assert.NoError(t, expFail2.Close()) }()
	txn = makeApplTxn(testAppID, []string{badLog})
	err = expFail2.Receive(makeBlock(1, wrapInBlock(txn)))
	require.ErrorContains(t, err, "unable to decode log")
	assert.Empty(t, dbFail2.batches)
}

func TestReceiveReplayReplacesEvents(t *testing.T) {
	cfg := testConfig
	exp, db := newTestExporter(t, cfg, 5)

	blk, _, _ := transferBlock(t, 5)
	require.NoError(t, exp.Receive(blk))
	require.Equal(t, uint64(6), db.nextRoundVal)
	require.Len(t, db.batches, 1)
	assert.NoError(t, exp.Close())

	// Rewind the pipeline to round 5 and replay it against the same db.
	exp2 := &appEventsExporter{
		connect: func(_ context.Context, _ string, _ uint32) (dbRunner, error) { return db, nil },
	}
	sdkRound := sdk.Round(5)
	require.NoError(t, exp2.Init(context.Background(), conduit.MakePipelineInitProvider(&sdkRound, &sdk.Genesis{}, nil), plugins.MakePluginConfig(cfg), testLogger))
	defer func() { assert.NoError(t, exp2.Close()) }()
	require.NoError(t, exp2.Receive(blk))

	require.Len(t, db.batches, 2)
	replay := db.batches[1]
	// The replayed round deletes its previous rows before inserting again,
	// all within one transaction: no duplicates.
	require.Len(t, replay, 4)
	assert.Contains(t, replay[0].sql, `DELETE FROM "app_event_transfer"`)
	assert.Equal(t, int64(5), replay[0].args[0])
	assert.Contains(t, replay[1].sql, `DELETE FROM "app_events_unmatched"`)
	assert.Contains(t, replay[2].sql, `INSERT INTO "app_event_transfer"`)
	assert.Contains(t, replay[3].sql, "INSERT INTO "+metaTableName)
	assert.Equal(t, int64(6), replay[3].args[0])
}

func TestReceiveDBError(t *testing.T) {
	exp, db := newTestExporter(t, testConfig, 8)
	defer func() { assert.NoError(t, exp.Close()) }()

	db.runInTxErr = fmt.Errorf("connection lost")
	err := exp.Receive(makeBlock(8))
	require.ErrorContains(t, err, "connection lost")
	// The round is not advanced: the pipeline will retry it.
	assert.Equal(t, uint64(8), exp.round)
}

func TestReceiveNotInitialized(t *testing.T) {
	exp := &appEventsExporter{}
	assert.ErrorContains(t, exp.Receive(makeBlock(0)), "not initialized")
}

func TestRoundRequest(t *testing.T) {
	// Uninitialized db: no override.
	exp := &appEventsExporter{
		connect: func(_ context.Context, _ string, _ uint32) (dbRunner, error) { return &fakeDB{}, nil },
	}
	round, err := exp.RoundRequest(plugins.MakePluginConfig(testConfig))
	require.NoError(t, err)
	assert.Equal(t, uint64(0), round)

	// Initialized db: the stored round is requested.
	exp = &appEventsExporter{
		connect: func(_ context.Context, _ string, _ uint32) (dbRunner, error) {
			return &fakeDB{nextRoundVal: 42, nextRoundFound: true}, nil
		},
	}
	round, err = exp.RoundRequest(plugins.MakePluginConfig(testConfig))
	require.NoError(t, err)
	assert.Equal(t, uint64(42), round)

	// Connection problems are treated as "no override" and surface in Init.
	exp = &appEventsExporter{
		connect: func(_ context.Context, _ string, _ uint32) (dbRunner, error) { return nil, fmt.Errorf("db down") },
	}
	round, err = exp.RoundRequest(plugins.MakePluginConfig(testConfig))
	require.NoError(t, err)
	assert.Equal(t, uint64(0), round)
}

func TestRoundRequestBadConfig(t *testing.T) {
	exp := &appEventsExporter{}
	round, err := exp.RoundRequest(plugins.MakePluginConfig("'"))
	require.NoError(t, err)
	assert.Equal(t, uint64(0), round)
}
