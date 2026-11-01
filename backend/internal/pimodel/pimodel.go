// Package pimodel renders Pi's model configuration for one session.
//
// Pi learns about models from `models.json` inside its agent directory
// (`PI_CODING_AGENT_DIR`). The control plane therefore has to hand each session
// a file that points at the model gateway rather than at a provider, which is
// what keeps a real API key out of the sandbox (PRD §5.7.2).
//
// This is deliberately a pure file writer: it takes a description and produces
// a directory, so it can be tested without a sandbox, a database or a model.
package pimodel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GatewayProvider is the provider id every session sees. It is stable so the
// default-model argument can name it.
const GatewayProvider = "piforge"

// Spec describes one session's model configuration.
type Spec struct {
	// GatewayURL is the base the session should call, already including the
	// per-session token, e.g. http://127.0.0.1:8080/gw/llm/<token>.
	GatewayURL string
	// Token is the short-lived credential the gateway accepts.
	Token string
	// Models are the model ids the session may use. The first is the default.
	Models []string
	// APIKind is Pi's api name: openai-completions, openai-responses or
	// anthropic-messages.
	APIKind string
	// ProviderName is only a display name.
	ProviderName string
	// Headers are request headers the provider entry sends with every call.
	//
	// They exist for one purpose: the value is a *reference* to the agent's
	// environment ("$PIFORGE_SANDBOX_PROOF"), which Pi resolves at request time.
	// The proof that binds a model token to its sandbox therefore is not in this
	// file — and this file is the one an attacker reads out of
	// /workspace/.piforge/agents/<session>/models.json
	// (docs/EXECUTION-BOUNDARY.md §16.3).
	Headers map[string]string
}

// Result is what the caller needs to launch Pi.
type Result struct {
	// Dir is the agent directory to pass as PI_CODING_AGENT_DIR.
	Dir string
	// Args are the extra argv entries that select the model.
	Args []string
	// Model is the provider-qualified model id, e.g. piforge/deepseek-v4-flash.
	Model string
}

type modelsFile struct {
	Schema    string                  `json:"$schema,omitempty"`
	Providers map[string]providerFile `json:"providers"`
}

type providerFile struct {
	Name       string      `json:"name,omitempty"`
	BaseURL    string      `json:"baseUrl"`
	API        string      `json:"api"`
	APIKey     string      `json:"apiKey"`
	AuthHeader bool        `json:"authHeader,omitempty"`
	// Headers are Pi's per-provider request headers. Values resolve like apiKey
	// ("$NAME" is environment interpolation), which is what lets this file name a
	// secret it does not contain.
	Headers map[string]string `json:"headers,omitempty"`
	Models  []modelFile       `json:"models"`
}

type modelFile struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Write renders the agent directory for a session and returns how to launch it.
//
// The directory is created next to the session's other state, so deleting a
// session deletes its model configuration with it.
func Write(dir string, spec Spec) (Result, error) {
	if strings.TrimSpace(dir) == "" {
		return Result{}, fmt.Errorf("pimodel: agent dir is required")
	}
	if strings.TrimSpace(spec.GatewayURL) == "" {
		return Result{}, fmt.Errorf("pimodel: gateway url is required")
	}
	if len(spec.Models) == 0 {
		return Result{}, fmt.Errorf("pimodel: at least one model is required")
	}
	api := spec.APIKind
	if api == "" {
		api = "openai-completions"
	}

	models := make([]modelFile, 0, len(spec.Models))
	for _, id := range spec.Models {
		if strings.TrimSpace(id) == "" {
			continue
		}
		models = append(models, modelFile{ID: id, Name: id})
	}
	if len(models) == 0 {
		return Result{}, fmt.Errorf("pimodel: a model id is empty")
	}

	name := spec.ProviderName
	if name == "" {
		name = "PiForge"
	}
	file := modelsFile{
		Schema: "https://pi.dev/schemas/models.schema.json",
		Providers: map[string]providerFile{
			GatewayProvider: {
				Name: name,
				// The gateway speaks the OpenAI wire format for openai-* providers
				// and the Anthropic one for anthropic-messages; the base URL is
				// the same either way because Pi appends the path itself.
				BaseURL:    strings.TrimRight(spec.GatewayURL, "/") + "/v1",
				API:        api,
				APIKey:     spec.Token,
				AuthHeader: true,
				Headers:    spec.Headers,
				Models:     models,
			},
		},
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, fmt.Errorf("pimodel: create agent dir: %w", err)
	}
	payload, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return Result{}, fmt.Errorf("pimodel: encode models.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.json"), append(payload, '\n'), 0o600); err != nil {
		return Result{}, fmt.Errorf("pimodel: write models.json: %w", err)
	}

	model := GatewayProvider + "/" + models[0].ID
	return Result{
		Dir:   dir,
		Args:  []string{"--model", model},
		Model: model,
	}, nil
}
