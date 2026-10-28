package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
)

// providerProbeTimeout bounds the test call. A provider that cannot answer a
// one-token request in ten seconds is not going to serve a run.
const providerProbeTimeout = 10 * time.Second

type providerTestResult struct {
	OK        bool  `json:"ok"`
	Status    int   `json:"status"`
	LatencyMS int64 `json:"latency_ms"`
	// Message is the upstream's own words, truncated. It is what turns "your key
	// is wrong" into "your key is wrong, and here is the provider saying so".
	Message string `json:"message,omitempty"`
}

// handleTestProvider sends one minimal request to prove the endpoint and key
// work (PRD FR-13.7).
//
// The value of this endpoint is that it fails *before* a user spends twenty
// minutes on a run that cannot work. It therefore reports what the provider
// said, not just a boolean — and it never echoes the key.
func (s *server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	provider, err := s.deps.Store.Provider(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if s.deps.Secrets == nil {
		s.fail(w, r, apiErr(contracts.ErrInternal, "secret storage is not configured on this server"))
		return
	}
	sealed, err := s.deps.Store.ProviderSecret(r.Context(), user.ID, provider.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(sealed) == 0 {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "this provider has no API key yet"))
		return
	}
	key, err := s.deps.Secrets.OpenString(sealed)
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrInternal, "the stored key cannot be decrypted"))
		return
	}

	result := probeProvider(r.Context(), provider, key)
	writeJSON(w, http.StatusOK, result)
}

// probeProvider performs the provider-specific minimal call.
func probeProvider(ctx context.Context, provider *contracts.Provider, key string) providerTestResult {
	ctx, cancel := context.WithTimeout(ctx, providerProbeTimeout)
	defer cancel()

	target, body, headers, err := probeRequest(provider, key)
	if err != nil {
		return providerTestResult{OK: false, Message: err.Error()}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return providerTestResult{OK: false, Message: "cannot build the test request"}
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	started := time.Now()
	resp, err := http.DefaultClient.Do(req)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return providerTestResult{OK: false, LatencyMS: latency, Message: truncate(err.Error(), 300)}
	}
	defer func() { _ = resp.Body.Close() }()

	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	result := providerTestResult{
		OK:        resp.StatusCode >= 200 && resp.StatusCode < 300,
		Status:    resp.StatusCode,
		LatencyMS: latency,
	}
	if !result.OK {
		result.Message = truncate(upstreamMessage(payload), 300)
	}
	return result
}

// apiBase normalises a provider's base URL so that appending a path never
// produces `/v1/v1/...`.
//
// Providers document their base both ways — with and without the `/v1` — and the
// probe used to append `/v1` unconditionally. That made the connectivity test
// lie in both directions: a working endpoint failed, and an endpoint whose base
// already ended in /v1 was tested against a URL that does not exist while the
// real gateway path (which dedupes) worked.
func apiBase(provider *contracts.Provider, fallback string) string {
	base := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	if base == "" {
		return fallback
	}
	return strings.TrimSuffix(base, "/v1")
}

// probeRequest builds the smallest request each API kind accepts.
func probeRequest(provider *contracts.Provider, key string) (string, []byte, map[string]string, error) {
	base := apiBase(provider, "")
	headers := map[string]string{"Content-Type": "application/json"}

	switch provider.API {
	case "anthropic-messages":
		base = apiBase(provider, "https://api.anthropic.com")
		headers["x-api-key"] = key
		headers["anthropic-version"] = "2023-06-01"
		body := map[string]any{
			"model":      firstModel(provider, "claude-3-5-haiku-20241022"),
			"max_tokens": 1,
			"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		}
		encoded, err := json.Marshal(body)
		return base + "/v1/messages", encoded, headers, err
	case "openai-responses":
		base = apiBase(provider, "https://api.openai.com")
		headers["Authorization"] = "Bearer " + key
		body := map[string]any{
			"model":             firstModel(provider, "gpt-4o-mini"),
			"input":             "ping",
			"max_output_tokens": 16,
		}
		encoded, err := json.Marshal(body)
		return base + "/v1/responses", encoded, headers, err
	case "openai-compat", "":
		if base == "" {
			return "", nil, nil, fmt.Errorf("this provider has no base URL to test")
		}
		headers["Authorization"] = "Bearer " + key
		body := map[string]any{
			"model":      firstModel(provider, "gpt-4o-mini"),
			"max_tokens": 1,
			"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		}
		encoded, err := json.Marshal(body)
		return base + "/v1/chat/completions", encoded, headers, err
	default:
		return "", nil, nil, fmt.Errorf("cannot test provider api %q", provider.API)
	}
}

func firstModel(provider *contracts.Provider, fallback string) string {
	for _, model := range provider.Models {
		if strings.TrimSpace(model.ID) != "" {
			return model.ID
		}
	}
	return fallback
}

// upstreamMessage digs the provider's own error text out of the response, which
// is what a user needs to see ("invalid x-api-key" is actionable).
func upstreamMessage(payload []byte) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(payload, &envelope) == nil {
		if envelope.Error.Message != "" {
			return envelope.Error.Message
		}
		if envelope.Message != "" {
			return envelope.Message
		}
	}
	return strings.TrimSpace(string(payload))
}

func truncate(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
