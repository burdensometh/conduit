package anonymizer

import (
	"context"
	_ "embed" // used to embed config
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/algorand/conduit/conduit/data"
	"github.com/algorand/conduit/conduit/plugins"
	"github.com/algorand/conduit/conduit/plugins/processors"
)

// PluginName to use when configuring.
const PluginName = "anonymizer"

// package-wide init function
func init() {
	processors.Register(PluginName, processors.ProcessorConstructorFunc(func() processors.Processor {
		return &Anonymizer{}
	}))
}

//go:embed sample.yaml
var sampleConfig string

// Anonymizer replaces address values in blocks with deterministic pseudonymous
// addresses. The equality relation between addresses is preserved, so the
// output can be used for research without exposing the original accounts.
type Anonymizer struct {
	logger *logrus.Logger
	cfg    Config

	transformer *transformer
	// alreadyTransformed is set when the importer reports that the incoming
	// data is already anonymized (for example a dataset replayed from files the
	// anonymizer wrote). In that case Process is a pass-through, so pseudonyms
	// cannot be remapped twice.
	alreadyTransformed bool
}

// Metadata returns the plugin metadata.
func (a *Anonymizer) Metadata() plugins.Metadata {
	return plugins.Metadata{
		Name:         PluginName,
		Description:  "Replace address values with deterministic pseudonymous addresses so the data can be shared without exposing the original accounts.",
		Deprecated:   false,
		SampleConfig: sampleConfig,
	}
}

// Init validates the configuration and builds the dataset mapping.
func (a *Anonymizer) Init(_ context.Context, initProvider data.InitProvider, cfg plugins.PluginConfig, logger *logrus.Logger) error {
	a.logger = logger

	if err := cfg.UnmarshalConfig(&a.cfg); err != nil {
		return fmt.Errorf("anonymizer init error: %w", err)
	}

	key, err := a.loadKey()
	if err != nil {
		return err
	}

	roles, err := resolveRoles(a.cfg.Roles)
	if err != nil {
		return err
	}

	a.transformer = &transformer{
		mapper: newAddressMapper(key),
		roles:  roles,
	}

	if provider, ok := initProvider.(data.AnonymizedProvider); ok && provider.IsAnonymized() {
		// The importer already read transformed data. Do not transform it
		// again, and leave the (already anonymized) genesis alone.
		a.alreadyTransformed = true
		a.logger.Infof("Input is already anonymized; anonymizer will pass data through unchanged")
		return nil
	}

	// The genesis file describes the same accounts as the blocks. Anonymize it
	// in place so a downstream file_writer exports a consistent genesis.
	if genesis := initProvider.GetGenesis(); genesis != nil {
		anonymized := a.transformer.transformGenesis(*genesis)
		initProvider.SetGenesis(&anonymized)
	}

	a.logger.Infof("Anonymizer initialized with %d address roles", len(roles))
	return nil
}

func (a *Anonymizer) loadKey() ([]byte, error) {
	hasInline := a.cfg.DatasetKey != ""
	hasFile := a.cfg.DatasetKeyFile != ""

	switch {
	case hasInline && hasFile:
		return nil, errors.New("anonymizer init error: set only one of 'dataset-key' or 'dataset-key-file'")
	case !hasInline && !hasFile:
		return nil, errors.New("anonymizer init error: a fixed 'dataset-key' or 'dataset-key-file' is required to derive stable addresses")
	case hasFile:
		contents, err := os.ReadFile(a.cfg.DatasetKeyFile)
		if err != nil {
			return nil, fmt.Errorf("anonymizer init error: could not read 'dataset-key-file': %w", err)
		}
		key := strings.TrimSpace(string(contents))
		if key == "" {
			return nil, errors.New("anonymizer init error: 'dataset-key-file' is empty")
		}
		return []byte(key), nil
	default:
		return []byte(a.cfg.DatasetKey), nil
	}
}

// resolveRoles returns the configured roles, or all roles when none are set.
func resolveRoles(configured []Role) (roleSet, error) {
	valid := make(map[Role]bool, len(AllRoles))
	for _, role := range AllRoles {
		valid[role] = true
	}

	if len(configured) == 0 {
		roles := make(roleSet, len(AllRoles))
		for _, role := range AllRoles {
			roles[role] = true
		}
		return roles, nil
	}

	roles := make(roleSet, len(configured))
	for _, role := range configured {
		if !valid[role] {
			return nil, fmt.Errorf("anonymizer init error: unknown address role %q", role)
		}
		roles[role] = true
	}
	return roles, nil
}

// Close is a no-op.
func (a *Anonymizer) Close() error {
	return nil
}

// Process anonymizes the addresses in the block and marks the result as
// transformed data. Data that is already marked as transformed is returned
// unchanged, so replaying an anonymized dataset through this processor cannot
// remap the pseudonyms a second time.
func (a *Anonymizer) Process(input data.BlockData) (data.BlockData, error) {
	if a.transformer == nil {
		return data.BlockData{}, errors.New("anonymizer not initialized")
	}

	if a.alreadyTransformed {
		return input, nil
	}

	if input.Anonymized {
		return input, nil
	}

	out := a.transformer.transformBlock(input)
	out.Anonymized = true
	return out, nil
}
