package pipeline

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/algorand/go-algorand-sdk/v2/types"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/algorand/conduit/conduit/data"
	"github.com/algorand/conduit/conduit/plugins/exporters/filewriter"
	_ "github.com/algorand/conduit/conduit/plugins/exporters/filewriter"
	_ "github.com/algorand/conduit/conduit/plugins/importers/filereader"
	"github.com/algorand/conduit/conduit/plugins/processors"
)

// waitForPipeline waits for the pipeline's goroutines to exit, failing the
// test if they do not finish within the timeout.
func waitForPipeline(t *testing.T, p Pipeline) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		p.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("pipeline did not terminate")
	}
}

// makeFinitePipeline builds a pipelineImpl with mock plugins configured for
// finite range mode over the closed interval [startRound, finalRound].
func makeFinitePipeline(t *testing.T, startRound uint64, finalRound uint64, retryCount uint64) (*pipelineImpl, *mockImporter, *mockProcessor, *mockExporter, context.CancelCauseFunc) {
	t.Helper()

	mImporter := &mockImporter{genesis: sdk.Genesis{Network: "test"}}
	mImporter.On("GetBlock", mock.Anything).Return(mock.Anything, nil)
	mImporter.On("OnComplete", mock.Anything).Return(nil)
	mProcessor := &mockProcessor{}
	mProcessor.On("Process", mock.Anything).Return(mock.Anything)
	mProcessor.On("OnComplete", mock.Anything).Return(nil)
	mExporter := &mockExporter{}
	mExporter.On("Receive", mock.Anything).Return(nil)
	mExporter.On("OnComplete", mock.Anything).Return(nil)

	ctx, ccf := context.WithCancelCause(context.Background())
	l, _ := test.NewNullLogger()
	pImpl := &pipelineImpl{
		ctx:        ctx,
		ccf:        ccf,
		logger:     l,
		importer:   mImporter,
		processors: []processors.Processor{mProcessor},
		exporter:   mExporter,
		cfg: &data.Config{
			RetryDelay: 0 * time.Second,
			RetryCount: retryCount,
			Importer:   data.NameConfigPair{Name: "mockImporter", Config: map[string]interface{}{}},
			Processors: []data.NameConfigPair{{Name: "mockProcessor", Config: map[string]interface{}{}}},
			Exporter:   data.NameConfigPair{Name: "mockExporter", Config: map[string]interface{}{}},
			ConduitArgs: &data.Args{
				ConduitDataDir:    t.TempDir(),
				NextRoundOverride: startRound,
				FinalRound:        finalRound,
			},
		},
	}
	return pImpl, mImporter, mProcessor, mExporter, ccf
}

// TestPipelineFiniteRangeCompletes verifies that a finite range run processes
// exactly the closed interval [start, final], runs all completion callbacks,
// persists NextRound past the final round, and reports success without any
// external cancellation.
func TestPipelineFiniteRangeCompletes(t *testing.T) {
	const startRound = uint64(3)
	const finalRound = uint64(7)
	const numRounds = finalRound - startRound + 1

	pImpl, mImporter, mProcessor, mExporter, ccf := makeFinitePipeline(t, startRound, finalRound, 3)
	defer ccf(nil)

	require.NoError(t, pImpl.Init())
	pImpl.Start()
	waitForPipeline(t, pImpl)

	// The run reports success with no cancellation.
	require.NoError(t, pImpl.Error())

	// Exactly the closed interval was fetched, processed and exported.
	assert.Equal(t, numRounds, uint64(mImporter.getBlockCalls))
	assert.Equal(t, numRounds, uint64(mProcessor.processCalls))
	assert.Equal(t, numRounds, uint64(mExporter.receiveCalls))
	assert.Equal(t, []uint64{3, 4, 5, 6, 7}, mExporter.receivedRnds)

	// All completion callbacks ran for every round before success was reported.
	assert.Equal(t, numRounds, uint64(mImporter.onCompleteCalls))
	assert.Equal(t, numRounds, uint64(mProcessor.onCompleteCalls))
	assert.Equal(t, numRounds, uint64(mExporter.onCompleteCalls))
	assert.Equal(t, sdk.Round(finalRound), mImporter.finalRound)
	assert.Equal(t, sdk.Round(finalRound), mProcessor.finalRound)
	assert.Equal(t, sdk.Round(finalRound), mExporter.finalRound)

	// NextRound corresponds to the last truly completed round.
	assert.Equal(t, finalRound+1, pImpl.pipelineMetadata.NextRound)
	status, err := pImpl.Status()
	require.NoError(t, err)
	assert.Equal(t, finalRound+1, status.Round)
	meta, err := readBlockMetadata(pImpl.cfg.ConduitArgs.ConduitDataDir)
	require.NoError(t, err)
	assert.Equal(t, finalRound+1, meta.NextRound)
}

// TestPipelineFiniteRangeEmptyInterval verifies that a final round before the
// next round is a clear, successful no-op: nothing is processed and NextRound
// is left unchanged.
func TestPipelineFiniteRangeEmptyInterval(t *testing.T) {
	const startRound = uint64(10)
	const finalRound = uint64(7)

	pImpl, mImporter, mProcessor, mExporter, ccf := makeFinitePipeline(t, startRound, finalRound, 3)
	defer ccf(nil)

	require.NoError(t, pImpl.Init())
	pImpl.Start()
	waitForPipeline(t, pImpl)

	require.NoError(t, pImpl.Error())
	assert.Equal(t, 0, mImporter.getBlockCalls)
	assert.Equal(t, 0, mProcessor.processCalls)
	assert.Equal(t, 0, mExporter.receiveCalls)
	assert.Equal(t, 0, mExporter.onCompleteCalls)

	// NextRound is unchanged.
	assert.Equal(t, startRound, pImpl.pipelineMetadata.NextRound)
	meta, err := readBlockMetadata(pImpl.cfg.ConduitArgs.ConduitDataDir)
	require.NoError(t, err)
	assert.Equal(t, startRound, meta.NextRound)
}

// TestPipelineFiniteRangeSingleRound verifies the degenerate closed interval
// [r, r] processes exactly one round.
func TestPipelineFiniteRangeSingleRound(t *testing.T) {
	const theRound = uint64(42)

	pImpl, mImporter, _, mExporter, ccf := makeFinitePipeline(t, theRound, theRound, 3)
	defer ccf(nil)

	require.NoError(t, pImpl.Init())
	pImpl.Start()
	waitForPipeline(t, pImpl)

	require.NoError(t, pImpl.Error())
	assert.Equal(t, 1, mImporter.getBlockCalls)
	assert.Equal(t, 1, mExporter.receiveCalls)
	assert.Equal(t, []uint64{theRound}, mExporter.receivedRnds)
	assert.Equal(t, theRound+1, pImpl.pipelineMetadata.NextRound)
}

// TestPipelineFiniteRangeImporterError verifies that a block which cannot be
// imported inside the interval fails the run instead of being treated as a
// normal end of the source.
func TestPipelineFiniteRangeImporterError(t *testing.T) {
	pImpl, mImporter, _, mExporter, ccf := makeFinitePipeline(t, 3, 7, 2)
	defer ccf(nil)
	mImporter.failOnRound = 5

	require.NoError(t, pImpl.Init())
	pImpl.Start()
	waitForPipeline(t, pImpl)

	err := pImpl.Error()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "importer")
	// The run did not complete: round 5 never entered the pipeline, so at
	// most rounds 3 and 4 could have been exported.
	assert.LessOrEqual(t, mExporter.receiveCalls, 2)
	meta, metaErr := readBlockMetadata(pImpl.cfg.ConduitArgs.ConduitDataDir)
	require.NoError(t, metaErr)
	assert.LessOrEqual(t, meta.NextRound, uint64(5))
}

// TestPipelineFiniteRangeProcessorError verifies that a processing failure
// inside the interval fails the run.
func TestPipelineFiniteRangeProcessorError(t *testing.T) {
	pImpl, _, mProcessor, _, ccf := makeFinitePipeline(t, 3, 7, 2)
	defer ccf(nil)
	mProcessor.returnError = true

	require.NoError(t, pImpl.Init())
	pImpl.Start()
	waitForPipeline(t, pImpl)

	err := pImpl.Error()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "process")
}

// TestPipelineFiniteRangeExporterError verifies that an export failure inside
// the interval fails the run and that NextRound reflects only the rounds that
// were truly completed.
func TestPipelineFiniteRangeExporterError(t *testing.T) {
	pImpl, _, _, mExporter, ccf := makeFinitePipeline(t, 3, 7, 2)
	defer ccf(nil)
	mExporter.failOnRound = 5

	require.NoError(t, pImpl.Init())
	pImpl.Start()
	waitForPipeline(t, pImpl)

	err := pImpl.Error()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exporter")

	// Rounds 3 and 4 completed; round 5 failed all of its attempts
	// (1 initial + 2 retries).
	assert.Equal(t, 2+3, mExporter.receiveCalls)
	assert.Equal(t, []uint64{3, 4, 5, 5, 5}, mExporter.receivedRnds)
	assert.Equal(t, 2, mExporter.onCompleteCalls)
	assert.Equal(t, sdk.Round(4), mExporter.finalRound)

	// NextRound corresponds to the last truly completed round.
	assert.Equal(t, uint64(5), pImpl.pipelineMetadata.NextRound)
	meta, metaErr := readBlockMetadata(pImpl.cfg.ConduitArgs.ConduitDataDir)
	require.NoError(t, metaErr)
	assert.Equal(t, uint64(5), meta.NextRound)
}

// TestPipelineFiniteRangeCancel verifies that cancelling a finite range run
// before it completes produces a clear failure result.
func TestPipelineFiniteRangeCancel(t *testing.T) {
	pImpl, _, _, _, ccf := makeFinitePipeline(t, 3, math.MaxUint32, math.MaxUint64)

	require.NoError(t, pImpl.Init())
	pImpl.Start()

	errTestCancellation := errors.New("test cancellation")
	time.Sleep(100 * time.Millisecond)
	ccf(errTestCancellation)
	waitForPipeline(t, pImpl)

	err := pImpl.Error()
	require.Error(t, err)
	assert.ErrorIs(t, err, errTestCancellation)
}

// TestPipelineFiniteRangeFileRoundTrip is an end-to-end test over real file
// plugins: the file_reader importer serves rounds 0-9 from disk, and the
// pipeline is configured with final round 5. It verifies the pipeline drains
// and exits successfully, and that the file_writer output precisely covers
// the configured interval.
func TestPipelineFiniteRangeFileRoundTrip(t *testing.T) {
	srcDir, err := filepath.Abs(filepath.Join("..", "plugins", "importers", "filereader", "test_resources", "filereader_blocks"))
	require.NoError(t, err)
	dataDir := t.TempDir()

	const finalRound = 5
	cfgContents := fmt.Sprintf(`retry-count: 2
retry-delay: "0s"
hide-banner: true
importer:
  name: file_reader
  config:
    block-dir: "%s"
    filename-pattern: "%%[1]d_block.msgp.gz"
exporter:
  name: file_writer
  config:
    filename-pattern: "%%[1]d_block.msgp.gz"
    drop-certificate: false
`, srcDir)
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "conduit.yml"), []byte(cfgContents), 0644))

	pCfg, err := data.MakePipelineConfig(&data.Args{
		ConduitDataDir: dataDir,
		FinalRound:     finalRound,
	})
	require.NoError(t, err)

	logger, _ := test.NewNullLogger()
	pline, err := MakePipeline(context.Background(), pCfg, logger)
	require.NoError(t, err)
	require.NoError(t, pline.Init())
	pline.Start()
	waitForPipeline(t, pline)
	require.NoError(t, pline.Error())
	pline.Stop()

	// The output precisely covers rounds [0, 5]: every file exists, its
	// contents match the source, and no later rounds were written.
	outDir := filepath.Join(dataDir, "exporter_file_writer")
	for rnd := uint64(0); rnd <= finalRound; rnd++ {
		fname := fmt.Sprintf(filewriter.FilePattern, rnd)
		var srcBlk, outBlk data.BlockData
		require.NoError(t, filewriter.DecodeFromFile(filepath.Join(srcDir, fname), &srcBlk, filewriter.MessagepackFormat, true))
		require.NoError(t, filewriter.DecodeFromFile(filepath.Join(outDir, fname), &outBlk, filewriter.MessagepackFormat, true))
		require.Equal(t, srcBlk, outBlk, "round %d output differs from source", rnd)
	}
	_, err = os.Stat(filepath.Join(outDir, fmt.Sprintf(filewriter.FilePattern, finalRound+1)))
	require.ErrorIs(t, err, os.ErrNotExist)

	// NextRound corresponds to the last truly completed round.
	meta, err := readBlockMetadata(dataDir)
	require.NoError(t, err)
	assert.Equal(t, uint64(finalRound+1), meta.NextRound)
	status, err := pline.Status()
	require.NoError(t, err)
	assert.Equal(t, uint64(finalRound+1), status.Round)
}

// TestPipelineFiniteRangeFileMissingBlock verifies that when the source runs
// out of blocks before the final round, the run fails instead of treating the
// gap as a normal EOF.
func TestPipelineFiniteRangeFileMissingBlock(t *testing.T) {
	srcDir, err := filepath.Abs(filepath.Join("..", "plugins", "importers", "filereader", "test_resources", "filereader_blocks"))
	require.NoError(t, err)
	dataDir := t.TempDir()

	// The source directory holds rounds 0-9; ask for rounds up to 12.
	cfgContents := fmt.Sprintf(`retry-count: 2
retry-delay: "1s"
hide-banner: true
importer:
  name: file_reader
  config:
    block-dir: "%s"
    filename-pattern: "%%[1]d_block.msgp.gz"
exporter:
  name: file_writer
  config:
    filename-pattern: "%%[1]d_block.msgp.gz"
    drop-certificate: false
`, srcDir)
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "conduit.yml"), []byte(cfgContents), 0644))

	pCfg, err := data.MakePipelineConfig(&data.Args{
		ConduitDataDir: dataDir,
		FinalRound:     12,
	})
	require.NoError(t, err)

	logger, _ := test.NewNullLogger()
	pline, err := MakePipeline(context.Background(), pCfg, logger)
	require.NoError(t, err)
	require.NoError(t, pline.Init())
	pline.Start()
	waitForPipeline(t, pline)

	// The missing rounds are a failure, not a normal end of input.
	require.Error(t, pline.Error())
	pline.Stop()

	// Rounds 0-9 were exported before the failure; nothing beyond them.
	outDir := filepath.Join(dataDir, "exporter_file_writer")
	for rnd := uint64(0); rnd <= 9; rnd++ {
		_, statErr := os.Stat(filepath.Join(outDir, fmt.Sprintf(filewriter.FilePattern, rnd)))
		require.NoError(t, statErr, "expected round %d to be exported", rnd)
	}
	_, err = os.Stat(filepath.Join(outDir, fmt.Sprintf(filewriter.FilePattern, 10)))
	require.ErrorIs(t, err, os.ErrNotExist)

	// NextRound stopped at the first missing round.
	meta, err := readBlockMetadata(dataDir)
	require.NoError(t, err)
	assert.Equal(t, uint64(10), meta.NextRound)
}

// TestPipelineFiniteRangeContinuousDefault verifies that without a final
// round the pipeline keeps following new rounds (existing behavior), and that
// cancellation is not reported as an error in that mode.
func TestPipelineFiniteRangeContinuousDefault(t *testing.T) {
	pImpl, _, _, mExporter, ccf := makeFinitePipeline(t, 3, 0, math.MaxUint64)
	pImpl.cfg.ConduitArgs.FinalRound = 0

	require.NoError(t, pImpl.Init())
	pImpl.Start()

	time.Sleep(100 * time.Millisecond)
	ccf(errors.New("test cancellation"))
	waitForPipeline(t, pImpl)

	// Follow mode: external cancellation is a clean shutdown.
	assert.NoError(t, pImpl.Error())
	assert.Greater(t, mExporter.receiveCalls, 0)
}
