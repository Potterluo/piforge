package llmgw

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/crypto"
)

// fakeProvider mimics the store's resolver.
type fakeProvider struct {
	providers map[string]*contracts.Provider
	sealed    map[string][]byte
	err       error
	// calls records how many times the resolver was hit.
	calls int
}

func (f *fakeProvider) ResolveProvider(_ context.Context, userID, name string) (*contracts.Provider, []byte, error) {
	f.calls++
	if f.err != nil {
		return nil, nil, f.err
	}
	provider, ok := f.providers[name]
	if !ok {
		return nil, nil, contracts.NewError(contracts.ErrNotFound, "no provider")
	}
	return provider, f.sealed[name], nil
}

type harness struct {
	gateway  *Handler
	signer   *Signer
	box      *crypto.Box
	store    *fakeProvider
	upstream *httptest.Server
	// captured is the last request the upstream saw.
	captured *http.Request
	body     string
	header   http.Header
	sse      bool
}

const testKey = "sk-ant-api03-realkey-abcdef"

func newHarness(t *testing.T) *harness {
	t.Helper()

	h := &harness{}
	h.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.captured = r
		h.header = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		h.body = string(body)

		if h.sse {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			for _, chunk := range []string{"data: {\"delta\":\"Hel\"}\n\n", "data: {\"delta\":\"lo\"}\n\n"} {
				_, _ = io.WriteString(w, chunk)
				if flusher != nil {
					flusher.Flush()
				}
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(h.upstream.Close)

	box, err := crypto.New(bytes.Repeat([]byte{5}, crypto.KeySize))
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	h.box = box
	sealed, err := box.SealString(testKey)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	h.store = &fakeProvider{
		providers: map[string]*contracts.Provider{
			"anthropic-shared": {
				Name: "anthropic-shared", API: "anthropic-messages",
				BaseURL: h.upstream.URL, Enabled: true, HasKey: true,
			},
			"openai-mine": {
				Name: "openai-mine", API: "openai-compat",
				BaseURL: h.upstream.URL, Enabled: true, HasKey: true,
			},
			"no-key": {
				Name: "no-key", API: "anthropic-messages",
				BaseURL: h.upstream.URL, Enabled: true, HasKey: false,
			},
		},
		sealed: map[string][]byte{
			"anthropic-shared": sealed,
			"openai-mine":      sealed,
		},
	}

	signer, err := NewSigner([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	h.signer = signer

	gw, err := New(Config{Store: h.store, Box: box, Signer: signer})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.gateway = gw
	return h
}

func (h *harness) do(t *testing.T, token, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.gateway.ServeHTTP(rec, req)
	return rec
}

func TestGatewayInjectsAnthropicKeyAndStripsTheEphemeralOne(t *testing.T) {
	h := newHarness(t)
	token, err := h.signer.Issue("ses_1", "usr_1", "anthropic-shared", "claude-sonnet-4-5", time.Now())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	rec := h.do(t, "", http.MethodPost, "/gw/llm/"+token+"/v1/messages",
		`{"model":"claude-sonnet-4-5","stream":true}`, map[string]string{
			"Content-Type": "application/json",
		})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := h.header.Get("x-api-key"); got != testKey {
		t.Fatalf("upstream x-api-key = %q, want the real key", got)
	}
	if got := h.header.Get("Authorization"); got != "" {
		t.Fatalf("ephemeral Authorization leaked upstream: %q", got)
	}
	if got := h.header.Get("X-PiForge-Session"); got != "ses_1" {
		t.Fatalf("session attribution header = %q", got)
	}
	if !strings.Contains(h.captured.URL.Path, "/v1/messages") {
		t.Fatalf("upstream path = %q, want it to keep /v1/messages", h.captured.URL.Path)
	}
	if !strings.Contains(h.body, "claude-sonnet-4-5") {
		t.Fatalf("request body was not forwarded: %q", h.body)
	}
}

func TestGatewayUsesBearerForOpenAICompatibleProviders(t *testing.T) {
	h := newHarness(t)
	token, _ := h.signer.Issue("ses_1", "usr_1", "openai-mine", "", time.Now())

	rec := h.do(t, "", http.MethodPost, "/gw/llm/"+token+"/v1/chat/completions", `{"model":"gpt"}`,
		map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := h.header.Get("Authorization"); got != "Bearer "+testKey {
		t.Fatalf("upstream Authorization = %q", got)
	}
	if got := h.header.Get("x-api-key"); got != "" {
		t.Fatalf("x-api-key should not be set for an OpenAI-compatible provider: %q", got)
	}
}

func TestGatewayAcceptsTheTokenAsABearerHeader(t *testing.T) {
	h := newHarness(t)
	token, _ := h.signer.Issue("ses_1", "usr_1", "anthropic-shared", "", time.Now())

	rec := h.do(t, token, http.MethodPost, "/gw/llm/v1/messages", `{}`,
		map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if h.header.Get("x-api-key") != testKey {
		t.Fatal("the real key was not injected")
	}
}

func TestGatewayStreamsWithoutBuffering(t *testing.T) {
	h := newHarness(t)
	h.sse = true
	token, _ := h.signer.Issue("ses_1", "usr_1", "anthropic-shared", "", time.Now())

	rec := h.do(t, "", http.MethodPost, "/gw/llm/"+token+"/v1/messages", `{"stream":true}`, nil)
	body := rec.Body.String()
	if !strings.Contains(body, `"Hel"`) || !strings.Contains(body, `"lo"`) {
		t.Fatalf("streamed chunks were lost: %q", body)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q", got)
	}
}

func TestGatewayRejectsBadTokens(t *testing.T) {
	h := newHarness(t)
	good, _ := h.signer.Issue("ses_1", "usr_1", "anthropic-shared", "", time.Now())
	tampered := good[:len(good)-2] + "xy"

	expiredSigner, _ := NewSigner([]byte("0123456789abcdef0123456789abcdef"), time.Second)
	expired, _ := expiredSigner.Issue("ses_1", "usr_1", "anthropic-shared", "", time.Now().Add(-time.Hour))

	other, _ := NewSigner([]byte("ffffffffffffffffffffffffffffffff"), time.Hour)
	foreign, _ := other.Issue("ses_1", "usr_1", "anthropic-shared", "", time.Now())

	cases := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"garbage", "not-a-token"},
		{"tampered", tampered},
		{"expired", expired},
		{"signed by someone else", foreign},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(t, "", http.MethodPost, "/gw/llm/"+tc.token+"/v1/messages", `{}`, nil)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("error body is not JSON: %v", err)
			}
			if body.Error.Code != "invalid_token" {
				t.Fatalf("error code = %q", body.Error.Code)
			}
		})
	}
}

// A missing key is a configuration error, not an upstream outage: it answers a
// 4xx so the agent does not retry it (Pi retries 5xx/429, not 4xx). The code
// stays provider_unavailable so the UI can map it to one sentence.
func TestGatewayRefusesProvidersWithoutAKey(t *testing.T) {
	h := newHarness(t)
	token, _ := h.signer.Issue("ses_1", "usr_1", "no-key", "", time.Now())

	rec := h.do(t, "", http.MethodPost, "/gw/llm/"+token+"/v1/messages", `{}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (a 502 is retried by the agent)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "provider_unavailable") {
		t.Fatalf("error code missing: %s", rec.Body.String())
	}
	if h.captured != nil {
		t.Fatal("the request reached the upstream despite there being no key")
	}
}

func TestGatewayRefusesUnknownProviders(t *testing.T) {
	h := newHarness(t)
	token, _ := h.signer.Issue("ses_1", "usr_1", "does-not-exist", "", time.Now())

	rec := h.do(t, "", http.MethodPost, "/gw/llm/"+token+"/v1/messages", `{}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "provider_unavailable") {
		t.Fatalf("error code missing: %s", rec.Body.String())
	}
}

func TestSplitPath(t *testing.T) {
	cases := []struct {
		path, token, rest string
		wantErr           bool
	}{
		{"/gw/llm/abc/v1/messages", "abc", "/v1/messages", false},
		{"/gw/llm/abc", "abc", "", false},
		{"/gw/llm/", "", "", false},
		{"/gw/llm", "", "", true},
		{"/other", "", "", true},
	}
	for _, tc := range cases {
		token, rest, err := splitPath(tc.path)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: err = %v", tc.path, err)
		}
		if err == nil && (token != tc.token || rest != tc.rest) {
			t.Fatalf("%s -> (%q,%q), want (%q,%q)", tc.path, token, rest, tc.token, tc.rest)
		}
	}
}

func TestUpstreamURLKeepsTheBasePath(t *testing.T) {
	h := &Handler{}
	target, err := h.upstreamURL(&contracts.Provider{
		Name: "corp", API: "openai-compat", BaseURL: "https://gw.internal/openai/",
	}, "/v1/chat/completions", "x=1")
	if err != nil {
		t.Fatalf("upstreamURL: %v", err)
	}
	if target.String() != "https://gw.internal/openai/v1/chat/completions?x=1" {
		t.Fatalf("target = %s", target)
	}
}

func TestUpstreamURLRejectsNonHTTP(t *testing.T) {
	h := &Handler{}
	if _, err := h.upstreamURL(&contracts.Provider{Name: "x", BaseURL: "file:///etc/passwd"}, "/v1", ""); err == nil {
		t.Fatal("accepted a non-http base URL")
	}
}
func TestUpstreamURLDoesNotDoubleV1(t *testing.T) {
	h := &Handler{}
	// A provider configured with the URL its docs give, which already ends in
	// /v1, while the client appends /v1/... itself.
	target, err := h.upstreamURL(&contracts.Provider{
		Name: "volc", API: "openai-compat",
		BaseURL: "https://gw.example/api/v1",
	}, "/v1/chat/completions", "")
	if err != nil {
		t.Fatalf("upstreamURL: %v", err)
	}
	if got, want := target.String(), "https://gw.example/api/v1/chat/completions"; got != want {
		t.Fatalf("target = %s, want %s", got, want)
	}
}
