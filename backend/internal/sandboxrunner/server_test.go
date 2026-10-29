package sandboxrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/sandbox"
)

// These tests exercise the HTTP surface through httptest, with an in-memory
// provider: no Docker, no image, no network. What they pin is the contract the
// control plane's provider is written against — methods, paths, status codes,
// auth and the error mapping — because that contract is the only thing standing
// between a deployment and a control plane that cannot create sandboxes.

const testToken = "test-runner-token"

// fakeProvider records what it was asked and answers from one canned record or
// error. It is deliberately dumber than sandbox.Docker: the handler must not
// depend on provider behaviour beyond the six methods.
type fakeProvider struct {
	mu sync.Mutex

	record sandbox.RunnerRecord
	err    error

	ensured   []sandbox.RunnerRequest
	gets      []string
	pauses    []string
	resumes   []string
	destroys  []string
	worktrees []struct {
		ID      string
		Request sandbox.RunnerWorktreeRequest
	}
}

func (p *fakeProvider) Ensure(ctx context.Context, request sandbox.RunnerRequest) (sandbox.RunnerRecord, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensured = append(p.ensured, request)
	return p.record, p.err
}

func (p *fakeProvider) Get(ctx context.Context, sandboxID string) (sandbox.RunnerRecord, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gets = append(p.gets, sandboxID)
	return p.record, p.err
}

func (p *fakeProvider) Pause(ctx context.Context, sandboxID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pauses = append(p.pauses, sandboxID)
	return p.err
}

func (p *fakeProvider) Resume(ctx context.Context, sandboxID string) (sandbox.RunnerRecord, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resumes = append(p.resumes, sandboxID)
	return p.record, p.err
}

func (p *fakeProvider) Destroy(ctx context.Context, sandboxID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.destroys = append(p.destroys, sandboxID)
	return p.err
}

func (p *fakeProvider) AddWorktree(ctx context.Context, sandboxID string, request sandbox.RunnerWorktreeRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.worktrees = append(p.worktrees, struct {
		ID      string
		Request sandbox.RunnerWorktreeRequest
	}{ID: sandboxID, Request: request})
	return p.err
}

func (p *fakeProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.ensured) + len(p.gets) + len(p.pauses) + len(p.resumes) + len(p.destroys) + len(p.worktrees)
}

func newTestServer(t *testing.T, provider Provider) *httptest.Server {
	t.Helper()
	server, err := New(Config{
		Token:    testToken,
		Provider: provider,
		// Discard the request log: the assertions are about responses, and a test
		// that prints one line per request is noise on failure.
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("sandboxrunner.New: %v", err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer
}

func doRequest(t *testing.T, server *httptest.Server, method, path, body, token string) (*http.Response, []byte) {
	t.Helper()
	return doRequestWithHeader(t, server, method, path, body, "Bearer "+token)
}

// doRequestWithHeader sends the literal Authorization header, so a test can
// present a scheme or a value the helper would never build.
func doRequestWithHeader(t *testing.T, server *httptest.Server, method, path, body, authorization string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, path, err)
	}
	return response, payload
}

func testRecord() sandbox.RunnerRecord {
	return sandbox.RunnerRecord{
		ID:                "piforge-0123456789abcdef",
		State:             contracts.SandboxReady,
		RootDir:           "/data/sandboxes/usr_1/prj_1",
		ContainerPathRoot: sandbox.ContainerWorkspace,
		Endpoint:          "http://127.0.0.1:49153",
	}
}

func TestServerCreateEndpoint(t *testing.T) {
	provider := &fakeProvider{record: testRecord()}
	server := newTestServer(t, provider)

	body := `{
		"user_id": "usr_1",
		"project_id": "prj_1",
		"image": "sandbox:1.11.0",
		"repo_url": "https://git.example/x.git",
		"branch": "main",
		"api_port": 8080,
		"api_key": "sandbox-key",
		"cpus": "2",
		"memory": "4g",
		"network": "piforge-sandboxes"
	}`
	response, payload := doRequest(t, server, http.MethodPost, "/v1/sandboxes", body, testToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.StatusCode, payload)
	}
	if got := response.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	var record sandbox.RunnerRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		t.Fatalf("response is not a record: %v (%s)", err, payload)
	}
	if record.ID != testRecord().ID || record.Endpoint != testRecord().Endpoint {
		t.Fatalf("record = %+v", record)
	}

	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.ensured) != 1 {
		t.Fatalf("%d Ensure calls, want 1", len(provider.ensured))
	}
	got := provider.ensured[0]
	if got.UserID != "usr_1" || got.ProjectID != "prj_1" || got.Image != "sandbox:1.11.0" {
		t.Fatalf("request = %+v", got)
	}
	if got.RepoURL != "https://git.example/x.git" || got.Branch != "main" {
		t.Fatalf("repo/branch = %q/%q", got.RepoURL, got.Branch)
	}
	if got.APIPort != 8080 || got.APIKey != "sandbox-key" {
		t.Fatalf("api port/key = %d/%q", got.APIPort, got.APIKey)
	}
	if got.CPUs != "2" || got.Memory != "4g" || got.Network != "piforge-sandboxes" {
		t.Fatalf("limits/network = %q/%q/%q", got.CPUs, got.Memory, got.Network)
	}
}

func TestServerGetEndpoint(t *testing.T) {
	provider := &fakeProvider{record: testRecord()}
	server := newTestServer(t, provider)

	response, payload := doRequest(t, server, http.MethodGet, "/v1/sandboxes/piforge-0123456789abcdef", "", testToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.StatusCode, payload)
	}
	var record sandbox.RunnerRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if record.State != contracts.SandboxReady || record.RootDir != testRecord().RootDir {
		t.Fatalf("record = %+v", record)
	}
	if len(provider.gets) != 1 || provider.gets[0] != "piforge-0123456789abcdef" {
		t.Fatalf("Get calls = %v", provider.gets)
	}
}

func TestServerPauseEndpoint(t *testing.T) {
	provider := &fakeProvider{record: testRecord()}
	server := newTestServer(t, provider)

	response, payload := doRequest(t, server, http.MethodPost, "/v1/sandboxes/piforge-abc/pause", "", testToken)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", response.StatusCode, payload)
	}
	if len(payload) != 0 {
		t.Fatalf("204 carried a body: %s", payload)
	}
	if len(provider.pauses) != 1 || provider.pauses[0] != "piforge-abc" {
		t.Fatalf("Pause calls = %v", provider.pauses)
	}
}

func TestServerResumeEndpoint(t *testing.T) {
	provider := &fakeProvider{record: testRecord()}
	server := newTestServer(t, provider)

	response, payload := doRequest(t, server, http.MethodPost, "/v1/sandboxes/piforge-abc/resume", "", testToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.StatusCode, payload)
	}
	var record sandbox.RunnerRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if record.ID != testRecord().ID {
		t.Fatalf("record = %+v", record)
	}
	if len(provider.resumes) != 1 || provider.resumes[0] != "piforge-abc" {
		t.Fatalf("Resume calls = %v", provider.resumes)
	}
}

func TestServerDestroyEndpoint(t *testing.T) {
	provider := &fakeProvider{record: testRecord()}
	server := newTestServer(t, provider)

	response, payload := doRequest(t, server, http.MethodDelete, "/v1/sandboxes/piforge-abc", "", testToken)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", response.StatusCode, payload)
	}
	if len(payload) != 0 {
		t.Fatalf("204 carried a body: %s", payload)
	}
	if len(provider.destroys) != 1 || provider.destroys[0] != "piforge-abc" {
		t.Fatalf("Destroy calls = %v", provider.destroys)
	}
}

func TestServerWorktreeEndpoint(t *testing.T) {
	provider := &fakeProvider{record: testRecord()}
	server := newTestServer(t, provider)

	body := `{"repo_dir":"/data/sandboxes/usr_1/prj_1/repo","path":"/data/sandboxes/usr_1/prj_1/worktrees/fix","branch":"piforge/fix","api_key":"sandbox-key"}`
	response, payload := doRequest(t, server, http.MethodPost, "/v1/sandboxes/piforge-abc/worktrees", body, testToken)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", response.StatusCode, payload)
	}
	if len(payload) != 0 {
		t.Fatalf("204 carried a body: %s", payload)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.worktrees) != 1 {
		t.Fatalf("%d AddWorktree calls, want 1", len(provider.worktrees))
	}
	got := provider.worktrees[0]
	if got.ID != "piforge-abc" {
		t.Fatalf("id = %q", got.ID)
	}
	if got.Request.RepoDir != "/data/sandboxes/usr_1/prj_1/repo" ||
		got.Request.Path != "/data/sandboxes/usr_1/prj_1/worktrees/fix" ||
		got.Request.Branch != "piforge/fix" ||
		got.Request.APIKey != "sandbox-key" {
		t.Fatalf("request = %+v", got.Request)
	}
}

// Auth is the only thing between the network and a container-creating API, so
// every shape of "no valid token" has to be a 401 and none of them may reach the
// provider.
func TestServerRejectsUnauthenticatedRequests(t *testing.T) {
	cases := []struct {
		name   string
		header string
	}{
		{name: "no header", header: ""},
		{name: "wrong scheme", header: "Basic test-runner-token"},
		{name: "wrong token", header: "Bearer not-the-token"},
		{name: "empty bearer", header: "Bearer "},
	}
	paths := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/v1/sandboxes", `{"user_id":"u","project_id":"p"}`},
		{http.MethodGet, "/v1/sandboxes/piforge-abc", ""},
		{http.MethodPost, "/v1/sandboxes/piforge-abc/pause", ""},
		{http.MethodPost, "/v1/sandboxes/piforge-abc/resume", ""},
		{http.MethodDelete, "/v1/sandboxes/piforge-abc", ""},
		{http.MethodPost, "/v1/sandboxes/piforge-abc/worktrees", `{"repo_dir":"/a","path":"/b","branch":"c"}`},
		{http.MethodGet, "/v1/healthz", ""},
	}

	for _, authCase := range cases {
		t.Run(authCase.name, func(t *testing.T) {
			provider := &fakeProvider{record: testRecord()}
			server := newTestServer(t, provider)
			for _, endpoint := range paths {
				response, payload := doRequestWithHeader(t, server, endpoint.method, endpoint.path, endpoint.body, authCase.header)
				if response.StatusCode != http.StatusUnauthorized {
					t.Fatalf("%s %s with %q = %d, want 401: %s", endpoint.method, endpoint.path, authCase.header, response.StatusCode, payload)
				}
				var envelope struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Error == "" {
					t.Fatalf("401 body is not {\"error\":...}: %s", payload)
				}
			}
			if calls := provider.calls(); calls != 0 {
				t.Fatalf("the provider was called %d times without a valid token", calls)
			}
		})
	}
}

// An unknown sandbox is sandbox.ErrNotFound, and the handler has to turn that into
// 404 for every operation; anything else and the store starts rebuilding live
// sandboxes or hiding real failures.
func TestServerUnknownSandboxIs404(t *testing.T) {
	provider := &fakeProvider{err: fmt.Errorf("sandbox: docker: no such container: %w", sandbox.ErrNotFound)}
	server := newTestServer(t, provider)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"get", http.MethodGet, "/v1/sandboxes/piforge-missing", ""},
		{"pause", http.MethodPost, "/v1/sandboxes/piforge-missing/pause", ""},
		{"resume", http.MethodPost, "/v1/sandboxes/piforge-missing/resume", ""},
		{"destroy", http.MethodDelete, "/v1/sandboxes/piforge-missing", ""},
		{"worktree", http.MethodPost, "/v1/sandboxes/piforge-missing/worktrees", `{"repo_dir":"/a","path":"/b","branch":"c"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response, payload := doRequest(t, server, tc.method, tc.path, tc.body, testToken)
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", response.StatusCode, payload)
			}
			if !strings.Contains(string(payload), "no such container") {
				t.Fatalf("404 body does not carry the reason: %s", payload)
			}
		})
	}
}

// A bad payload is the client's mistake, so it is a 400 that names the field, and
// the provider must not see it.
func TestServerRejectsBadPayloads(t *testing.T) {
	cases := []struct {
		name string
		path string
		body string
		want string
	}{
		{name: "malformed JSON", path: "/v1/sandboxes", body: `{"user_id":`, want: "invalid JSON"},
		{name: "missing user", path: "/v1/sandboxes", body: `{"project_id":"prj_1"}`, want: "user_id"},
		{name: "missing project", path: "/v1/sandboxes", body: `{"user_id":"usr_1"}`, want: "project_id"},
		{name: "two objects", path: "/v1/sandboxes", body: `{"user_id":"u","project_id":"p"}{"user_id":"u2"}`, want: "exactly one JSON object"},
		{name: "worktree without repo", path: "/v1/sandboxes/piforge-abc/worktrees", body: `{"path":"/b","branch":"c"}`, want: "repo_dir"},
		{name: "worktree without path", path: "/v1/sandboxes/piforge-abc/worktrees", body: `{"repo_dir":"/a","branch":"c"}`, want: "path"},
		{name: "worktree without branch", path: "/v1/sandboxes/piforge-abc/worktrees", body: `{"repo_dir":"/a","path":"/b"}`, want: "branch"},
		{name: "worktree with an option-shaped branch", path: "/v1/sandboxes/piforge-abc/worktrees", body: `{"repo_dir":"/a","path":"/b","branch":"--force"}`, want: "dash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeProvider{record: testRecord()}
			server := newTestServer(t, provider)
			response, payload := doRequest(t, server, http.MethodPost, tc.path, tc.body, testToken)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", response.StatusCode, payload)
			}
			if !strings.Contains(string(payload), tc.want) {
				t.Fatalf("400 body %s does not mention %q", payload, tc.want)
			}
			if calls := provider.calls(); calls != 0 {
				t.Fatalf("the provider was called %d times with a bad payload", calls)
			}
		})
	}
}

// "No other endpoints" is a requirement, not an accident of routing: a health
// check, an exec or a metrics path must all be absent.
func TestServerHasNoOtherEndpoints(t *testing.T) {
	provider := &fakeProvider{record: testRecord()}
	server := newTestServer(t, provider)
	cases := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/healthz", http.StatusNotFound},
		{http.MethodGet, "/v1/health", http.StatusNotFound},
		{http.MethodGet, "/v1/sandboxes", http.StatusMethodNotAllowed},
		{http.MethodPost, "/v1/sandboxes/piforge-abc/exec", http.StatusNotFound},
		{http.MethodGet, "/v1/sandboxes/piforge-abc/endpoint", http.StatusNotFound},
		{http.MethodDelete, "/v1/sandboxes", http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		response, payload := doRequest(t, server, tc.method, tc.path, "", testToken)
		if response.StatusCode != tc.want {
			t.Fatalf("%s %s = %d, want %d: %s", tc.method, tc.path, response.StatusCode, tc.want, payload)
		}
	}
	if calls := provider.calls(); calls != 0 {
		t.Fatalf("an unknown endpoint reached the provider %d times", calls)
	}
}

// The token is required at construction: a runner with an empty token would
// accept every request on its network.
func TestNewRefusesAnEmptyToken(t *testing.T) {
	_, err := New(Config{Provider: &fakeProvider{}})
	if err == nil {
		t.Fatal("New accepted an empty token")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Fatalf("error does not name the token: %v", err)
	}
}

// A deadline that fires while the provider works is 504, not 500: a caller that
// retries should treat it differently from a rejected request.
func TestServerMapsDeadlineToGatewayTimeout(t *testing.T) {
	provider := &fakeProvider{err: fmt.Errorf("docker run: %w", context.DeadlineExceeded)}
	server := newTestServer(t, provider)
	response, payload := doRequest(t, server, http.MethodPost, "/v1/sandboxes", `{"user_id":"u","project_id":"p"}`, testToken)
	if response.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504: %s", response.StatusCode, payload)
	}
	if !strings.Contains(string(payload), "deadline exceeded") {
		t.Fatalf("504 body does not carry the reason: %s", payload)
	}
}
