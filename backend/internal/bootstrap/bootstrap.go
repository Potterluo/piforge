// Package bootstrap makes an automated deployment usable on its first boot.
//
// A fresh PiForge has accounts but no model account, so every send answers 400
// `no_model` and the composer is dead until somebody opens Settings and types a
// provider by hand. That is a fine second step and a terrible first one: an
// unattended deploy (CI, a demo box, a customer's trial) never gets there.
//
// PIFORGE_BOOTSTRAP_PROVIDER=name|base_url|model plus PIFORGE_BOOTSTRAP_PROVIDER_KEY
// turns that step into startup configuration. The rules are deliberately
// conservative: create only when no shared provider exists, never overwrite one,
// and never write the key anywhere a log can see it.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/crypto"
	"github.com/piforge/piforge/backend/internal/store"
)

// EnvProvider is the documented deployment variable:
//
//	PIFORGE_BOOTSTRAP_PROVIDER=volc|https://ark.cn-beijing.volces.com/api/v3|doubao-seed-1-6
//
// The name is required, the base URL may be empty when the provider API has a
// default endpoint, and the model id is required.
const EnvProvider = "PIFORGE_BOOTSTRAP_PROVIDER"

// EnvKey is the API key for the bootstrap provider. It is optional: a provider
// without a key is still created, and the UI then asks for one. It is never
// logged, never echoed in an error, and only ever stored sealed.
const EnvKey = "PIFORGE_BOOTSTRAP_PROVIDER_KEY"

// Defaults is the parsed PIFORGE_BOOTSTRAP_PROVIDER value.
type Defaults struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url,omitempty"`
	Model   string `json:"model"`
}

// Empty reports whether no bootstrap provider was configured.
func (d Defaults) Empty() bool { return strings.TrimSpace(d.Name) == "" }

// Parse reads `name|base_url|model`.
//
// A malformed value is an error rather than a silent skip: an operator who
// typed the variable expects it to do something, and "the deployment stayed
// dead and the log said nothing" is the failure this feature exists to remove.
func Parse(spec string) (Defaults, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Defaults{}, nil
	}
	parts := strings.SplitN(spec, "|", 3)
	if len(parts) != 3 {
		return Defaults{}, fmt.Errorf("bootstrap: %s must be name|base_url|model (got %d field(s))", EnvProvider, len(parts))
	}
	d := Defaults{
		Name:    strings.TrimSpace(parts[0]),
		BaseURL: strings.TrimSpace(parts[1]),
		Model:   strings.TrimSpace(parts[2]),
	}
	if d.Name == "" {
		return Defaults{}, fmt.Errorf("bootstrap: %s: provider name is required", EnvProvider)
	}
	if d.Model == "" {
		return Defaults{}, fmt.Errorf("bootstrap: %s: model id is required", EnvProvider)
	}
	return d, nil
}

// FromEnv parses the deployment environment. An absent variable is not an
// error; an invalid one is returned so startup can say so once.
func FromEnv() (Defaults, error) {
	return Parse(os.Getenv(EnvProvider))
}

// API is the provider protocol a bootstrap provider speaks. The env format has
// no field for it, and the overwhelmingly common deployment is an
// OpenAI-compatible endpoint; a deployment that needs another shape adds the
// provider in the UI.
const API = "openai-compat"

// Ensure creates the shared provider described by defaults when the deployment
// has none.
//
// It returns the provider and whether this call created it. The key is sealed
// before it reaches the store, and the log line names the provider, endpoint and
// model only — never the key, not even a prefix.
func Ensure(ctx context.Context, st *store.Store, box *crypto.Box, logger *slog.Logger, defaults Defaults, key string) (*contracts.Provider, bool, error) {
	if defaults.Empty() {
		return nil, false, nil
	}
	var sealed []byte
	if strings.TrimSpace(key) != "" {
		if box == nil {
			return nil, false, errors.New("bootstrap: secret storage is not configured; cannot store the provider key")
		}
		value, err := box.SealString(key)
		if err != nil {
			return nil, false, fmt.Errorf("bootstrap: seal the provider key: %w", err)
		}
		sealed = value
	}

	provider, created, err := st.EnsureBootstrapProvider(ctx, store.ProviderInput{
		Name:      defaults.Name,
		API:       API,
		BaseURL:   defaults.BaseURL,
		Models:    []contracts.ModelSpec{{ID: defaults.Model, Name: defaults.Model}},
		KeyCipher: sealed,
	})
	if err != nil {
		return nil, false, err
	}
	if logger != nil {
		if created {
			logger.Info("bootstrap: created the shared model provider",
				"provider", provider.Name,
				"base_url", provider.BaseURL,
				"model", defaults.Model,
				"has_key", provider.HasKey)
		} else {
			logger.Info("bootstrap: a shared model provider already exists; leaving it alone",
				"provider", provider.Name,
				"detail", "remove "+EnvProvider+" from the deployment, or edit the provider in the admin UI")
		}
	}
	return provider, created, nil
}
