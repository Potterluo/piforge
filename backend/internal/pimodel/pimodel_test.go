package pimodel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteRendersAModelsFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent")
	res, err := Write(dir, Spec{
		GatewayURL:   "http://127.0.0.1:8080/gw/llm/tok123",
		Token:        "tok123",
		Models:       []string{"deepseek-v4-flash", "gpt-4o-mini"},
		APIKind:      "openai-completions",
		ProviderName: "volc",
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res.Model != "piforge/deepseek-v4-flash" {
		t.Fatalf("model = %q", res.Model)
	}
	if strings.Join(res.Args, " ") != "--model piforge/deepseek-v4-flash" {
		t.Fatalf("args = %v", res.Args)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatalf("read models.json: %v", err)
	}
	var file struct {
		Providers map[string]struct {
			BaseURL    string `json:"baseUrl"`
			API        string `json:"api"`
			APIKey     string `json:"apiKey"`
			AuthHeader bool   `json:"authHeader"`
			Models     []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("models.json is not JSON: %v", err)
	}
	provider, ok := file.Providers[GatewayProvider]
	if !ok {
		t.Fatalf("provider %q missing: %s", GatewayProvider, raw)
	}
	// The base URL must point at the gateway and keep the /v1 the SDK expects.
	if provider.BaseURL != "http://127.0.0.1:8080/gw/llm/tok123/v1" {
		t.Fatalf("baseUrl = %q", provider.BaseURL)
	}
	if provider.API != "openai-completions" {
		t.Fatalf("api = %q", provider.API)
	}
	if provider.APIKey != "tok123" || !provider.AuthHeader {
		t.Fatalf("credential = %q authHeader=%v", provider.APIKey, provider.AuthHeader)
	}
	if len(provider.Models) != 2 || provider.Models[0].ID != "deepseek-v4-flash" {
		t.Fatalf("models = %+v", provider.Models)
	}
}

func TestWriteRejectsUnusableSpecs(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]Spec{
		"no dir":      {GatewayURL: "http://x", Models: []string{"m"}},
		"no gateway":  {Models: []string{"m"}},
		"no models":   {GatewayURL: "http://x"},
		"blank model": {GatewayURL: "http://x", Models: []string{"  "}},
	}
	for name, spec := range cases {
		target := dir
		if name == "no dir" {
			target = ""
		}
		if _, err := Write(target, spec); err == nil {
			t.Fatalf("%s: Write accepted an unusable spec", name)
		}
	}
}

func TestWriteDefaultsTheAPIKind(t *testing.T) {
	res, err := Write(filepath.Join(t.TempDir(), "a"), Spec{
		GatewayURL: "http://x/gw/llm/t", Token: "t", Models: []string{"m"},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(res.Dir, "models.json"))
	if !strings.Contains(string(raw), `"api": "openai-completions"`) {
		t.Fatalf("default api missing:\n%s", raw)
	}
}

// The model token's proof header must be a *reference* to the agent's
// environment, never the value: models.json is the file an attacker copies
// (docs/EXECUTION-BOUNDARY.md §16.3), so a proof written here would be the same
// portable credential the binding exists to end.
func TestWriteEmitsTheProofHeaderAsAnEnvironmentReference(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent")
	if _, err := Write(dir, Spec{
		GatewayURL: "http://piforge:7700/gw/llm/tok123",
		Token:      "tok123",
		Models:     []string{"deepseek-v4-flash"},
		Headers:    map[string]string{"X-PF-Sandbox-Proof": "$PIFORGE_SANDBOX_PROOF"},
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatalf("read models.json: %v", err)
	}
	var file struct {
		Providers map[string]struct {
			Headers map[string]string `json:"headers"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("models.json is not JSON: %v", err)
	}
	got := file.Providers[GatewayProvider].Headers["X-PF-Sandbox-Proof"]
	if got != "$PIFORGE_SANDBOX_PROOF" {
		t.Fatalf("header = %q, want the environment reference", got)
	}
	// And nothing that looks like a proof (64 hex characters) is in the file.
	if strings.Contains(strings.ToLower(string(raw)), strings.Repeat("0123456789abcdef", 4)) {
		t.Fatalf("models.json carries a literal proof:\n%s", raw)
	}
	if strings.Contains(string(raw), "piforged") {
		t.Fatalf("models.json leaks a server-side detail:\n%s", raw)
	}
}
