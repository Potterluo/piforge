package sandboxrunner

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/sandbox"
)

// The runner is where a sandbox's key comes from, so it is also where a careless
// log line would put it on disk forever. Two properties:
//
//   - the create answer carries the key once (the control plane has no other way
//     to learn it, and the channel is the runner's bearer-token-protected API);
//   - nothing the runner logs — request lines, error lines, a rejected body —
//     ever contains it.
func TestRunnerReturnsTheKeyOnceAndNeverLogsIt(t *testing.T) {
	const secret = "9f8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a39281706f5e4d3c2b1a0"
	provider := &fakeProvider{record: testRecord()}
	provider.record.APIKey = secret

	logs := &lockedBuffer{}
	server, err := New(Config{
		Token:    testToken,
		Provider: provider,
		Logger:   slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatalf("sandboxrunner.New: %v", err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	body := `{"user_id":"usr_1","project_id":"prj_1","api_key":"` + secret + `"}`
	response, payload := doRequest(t, httpServer, http.MethodPost, "/v1/sandboxes", body, testToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.StatusCode, payload)
	}
	var record sandbox.RunnerRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		t.Fatalf("decode record: %v (body %s)", err, payload)
	}
	if record.APIKey != secret {
		t.Fatalf("create answer carried api_key %q, want the generated key", record.APIKey)
	}

	// A rejected request is logged too; its body is not, and the error line must
	// not echo a key either.
	bad, _ := doRequest(t, httpServer, http.MethodPost, "/v1/sandboxes",
		`{"user_id":"","project_id":"prj_1","api_key":"`+secret+`"}`, testToken)
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("an invalid create answered %d, want 400", bad.StatusCode)
	}
	if wrong, _ := doRequestWithHeader(t, httpServer, http.MethodPost, "/v1/sandboxes",
		`{"user_id":"u","project_id":"p","api_key":"`+secret+`"}`, "Bearer wrong"); wrong.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a wrong runner token answered %d, want 401", wrong.StatusCode)
	}

	// The log line is written after the handler returns, which can be a moment
	// after the client has the response.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logs.String(), "sandbox runner request") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := logs.String(); !strings.Contains(got, "sandbox runner request") {
		t.Fatalf("no request was logged; the assertion below would be vacuous:\n%s", got)
	}
	if got := logs.String(); strings.Contains(got, secret) {
		t.Fatalf("the sandbox API key was logged:\n%s", got)
	}
}

// Failure paths are where an error string tends to grow context. The runner must
// report a failure without repeating the key from the request body.
func TestRunnerErrorsDoNotEchoTheSandboxAPIKey(t *testing.T) {
	const secret = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	provider := &fakeProvider{err: errors.New("docker run failed")}
	logs := &lockedBuffer{}
	server, err := New(Config{
		Token:    testToken,
		Provider: provider,
		Logger:   slog.New(slog.NewTextHandler(logs, nil)),
	})
	if err != nil {
		t.Fatalf("sandboxrunner.New: %v", err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	body := `{"user_id":"usr_1","project_id":"prj_1","api_key":"` + secret + `"}`
	response, payload := doRequest(t, httpServer, http.MethodPost, "/v1/sandboxes", body, testToken)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", response.StatusCode, payload)
	}
	if strings.Contains(string(payload), secret) {
		t.Fatalf("the error response echoed the API key: %s", payload)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logs.String(), "sandbox runner request failed") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := logs.String(); !strings.Contains(got, "sandbox runner request failed") {
		t.Fatalf("the failure was not logged; the assertion below would be vacuous:\n%s", got)
	}
	if got := logs.String(); strings.Contains(got, secret) {
		t.Fatalf("the sandbox API key was logged:\n%s", got)
	}
}

// The provider's answer is the one place the control plane can learn a key it did
// not supply, so the adapter has to copy it into the record.
func TestDockerProviderRecordCarriesTheCreateKey(t *testing.T) {
	record := sandbox.RunnerRecord{APIKey: "from-the-provider"}
	// The adapter copies contracts.Sandbox.SandboxAPIKey; this pins the field name
	// on the wire so a rename cannot silently drop the key.
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["api_key"] != "from-the-provider" {
		t.Fatalf("record JSON = %s, want api_key on the wire", raw)
	}
	// And a record with no key (Get, Resume) must not invent one.
	raw, err = json.Marshal(sandbox.RunnerRecord{ID: "piforge-abc", State: contracts.SandboxReady})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "api_key") {
		t.Fatalf("a non-create record carries an api_key field: %s", raw)
	}
}

// lockedBuffer is a bytes.Buffer safe for the httptest server's goroutine and the
// test's reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var _ io.Writer = (*lockedBuffer)(nil)
