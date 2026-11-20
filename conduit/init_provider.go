package conduit

import (
	sdk "github.com/algorand/go-algorand-sdk/v2/types"

	"github.com/algorand/conduit/conduit/telemetry"
)

// PipelineInitProvider algod based init provider
type PipelineInitProvider struct {
	currentRound    *sdk.Round
	genesis         *sdk.Genesis
	telemetryClient telemetry.Client
	anonymized      bool
}

// MakePipelineInitProvider constructs an init provider.
func MakePipelineInitProvider(currentRound *sdk.Round, genesis *sdk.Genesis, client telemetry.Client) *PipelineInitProvider {
	return &PipelineInitProvider{
		currentRound:    currentRound,
		genesis:         genesis,
		telemetryClient: client,
	}
}

// SetGenesis updates the genesis block in the init provider
func (a *PipelineInitProvider) SetGenesis(genesis *sdk.Genesis) {
	a.genesis = genesis
}

// GetGenesis produces genesis pointer
func (a *PipelineInitProvider) GetGenesis() *sdk.Genesis {
	return a.genesis
}

// NextDBRound provides next database round
func (a *PipelineInitProvider) NextDBRound() sdk.Round {
	return *a.currentRound
}

// GetTelemetryClient gets the telemetry state in the init provider
func (a *PipelineInitProvider) GetTelemetryClient() telemetry.Client {
	return a.telemetryClient
}

// IsAnonymized reports whether the data entering the pipeline is already
// transformed (see data.AnonymizedProvider).
func (a *PipelineInitProvider) IsAnonymized() bool {
	return a.anonymized
}

// SetAnonymized records whether the data entering the pipeline is already
// transformed (see data.AnonymizedSetter).
func (a *PipelineInitProvider) SetAnonymized(anonymized bool) {
	a.anonymized = anonymized
}
