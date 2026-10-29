package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// The runner provider is only HTTP: no Docker, no image, no daemon. These tests
// use a fake runner, so they run everywhere, and they pin the two things that can
// silently rot: the exact requests the control plane sends, and the mapping from
// HTTP status back to the errors the store branches on.

type recordedRequest struct {
	Method        string
	Path          string
	Authorization string
	ContentType   string
	Body          string
}

type requestLog struct {
	mu       sync.Mutex
	requests []recordedRequest
}

func (l *requestLog) add(request recordedRequest) {
	l.mu.Lock()
	l.requests = append(l.requests, request)
	l.mu.Unlock()
}

func (l *requestLog) all() []recordedRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]recordedRequest(nil), l.requests...)
}

// startTestRunner serves a fake runner and returns a provider pointed at it.
func startTestRunner(t *testing.T, dataRoot string, respond http.HandlerFunc) (*Runner, *requestLog) {
	t.Helper()
	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		log.add(recordedRequest{
			Method:        r.Method,
			Path:          r.URL.Path,
			Authorization: r.Header.Get("Authorization"),
			ContentType:   r.Header.Get("Content-Type"),
			Body:          string(body),
		})
		respond(w, r)
	}))
	t.Cleanup(server.Close)

	provider, err := NewRunner(RunnerConfig{
		BaseURL:  server.URL,
		Token:    "test-token",
		DataRoot: dataRoot,
		Image:    "sandbox:test",
		APIPort:  8080,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return provider, log
}

// writeRecord answers one lifecycle call the way the runner does.
func writeRecord(t *testing.T, w http.ResponseWriter, record RunnerRecord) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(record); err != nil {
		t.Errorf("encode record: %v", err)
	}
}

// A create has to carry the whole request, because the runner is stateless and
// this is the only description of the sandbox it gets.
func TestRunnerEnsureSendsTheContainerRequest(t *testing.T) {
	dataRoot := t.TempDir()
	wantRoot := filepath.Join(dataRoot, "usr_1", "prj_1")
	provider, log := startTestRunner(t, dataRoot, func(w http.ResponseWriter, r *http.Request) {
		writeRecord(t, w, RunnerRecord{
			ID:                "piforge-0123456789abcdef",
			State:             contracts.SandboxReady,
			RootDir:           wantRoot,
			ContainerPathRoot: ContainerWorkspace,
			Endpoint:          "http://127.0.0.1:49153",
		})
	})

	sb, err := provider.Ensure(context.Background(), "usr_1", "prj_1", "https://git.example/x.git", "feature/x")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if sb.ID != "piforge-0123456789abcdef" {
		t.Fatalf("id = %q", sb.ID)
	}
	if sb.State != contracts.SandboxReady {
		t.Fatalf("state = %q", sb.State)
	}
	if sb.Provider != "runner" {
		t.Fatalf("provider = %q, want runner", sb.Provider)
	}
	if sb.Image != "sandbox:test" {
		t.Fatalf("image = %q", sb.Image)
	}
	if sb.RootDir != wantRoot {
		t.Fatalf("root dir = %q, want %q", sb.RootDir, wantRoot)
	}
	if sb.UserID != "usr_1" || sb.ProjectID != "prj_1" {
		t.Fatalf("pair = %q/%q", sb.UserID, sb.ProjectID)
	}
	if sb.CreatedAt.IsZero() || sb.LastActiveAt.IsZero() {
		t.Fatal("Ensure left the timestamps zero")
	}

	requests := log.all()
	if len(requests) != 1 {
		t.Fatalf("%d requests, want 1: %+v", len(requests), requests)
	}
	request := requests[0]
	if request.Method != http.MethodPost || request.Path != "/v1/sandboxes" {
		t.Fatalf("request = %s %s, want POST /v1/sandboxes", request.Method, request.Path)
	}
	if request.Authorization != "Bearer test-token" {
		t.Fatalf("Authorization = %q", request.Authorization)
	}
	if request.ContentType != "application/json" {
		t.Fatalf("Content-Type = %q", request.ContentType)
	}
	var body RunnerRequest
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		t.Fatalf("request body is not JSON: %v (%s)", err, request.Body)
	}
	if body.UserID != "usr_1" || body.ProjectID != "prj_1" {
		t.Fatalf("body pair = %q/%q", body.UserID, body.ProjectID)
	}
	if body.Image != "sandbox:test" || body.APIPort != 8080 {
		t.Fatalf("body image/port = %q/%d", body.Image, body.APIPort)
	}
	if body.RepoURL != "https://git.example/x.git" || body.Branch != "feature/x" {
		t.Fatalf("body repo/branch = %q/%q", body.RepoURL, body.Branch)
	}
}

// State, endpoint and the branch worktree are the three calls the rest of the
// product makes outside Ensure. Their method and path are the contract with the
// runner, so they are pinned literally.
func TestRunnerLifecycleRequests(t *testing.T) {
	dataRoot := t.TempDir()
	record := RunnerRecord{
		ID:                "piforge-abc",
		State:             contracts.SandboxReady,
		RootDir:           filepath.Join(dataRoot, "usr_1", "prj_1"),
		ContainerPathRoot: ContainerWorkspace,
		Endpoint:          "http://127.0.0.1:49153",
	}
	provider, log := startTestRunner(t, dataRoot, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			writeRecord(t, w, record)
		case r.URL.Path == "/v1/sandboxes/piforge-abc/pause":
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/v1/sandboxes/piforge-abc/resume":
			record.State = contracts.SandboxReady
			writeRecord(t, w, record)
		case r.URL.Path == "/v1/sandboxes/piforge-abc/worktrees":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	ctx := context.Background()

	got, err := provider.Get(ctx, "piforge-abc")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "piforge-abc" || got.State != contracts.SandboxReady {
		t.Fatalf("Get = %+v", got)
	}
	if got.RootDir != record.RootDir {
		t.Fatalf("Get root dir = %q, want %q", got.RootDir, record.RootDir)
	}

	endpoint, err := provider.Endpoint(ctx, "piforge-abc")
	if err != nil || endpoint != "http://127.0.0.1:49153" {
		t.Fatalf("Endpoint = %q, %v", endpoint, err)
	}

	if err := provider.Pause(ctx, "piforge-abc"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	resumed, err := provider.Resume(ctx, "piforge-abc")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.State != contracts.SandboxReady {
		t.Fatalf("Resume state = %q", resumed.State)
	}

	if err := provider.AddWorktree(ctx, "piforge-abc", record.RootDir+"/repo", record.RootDir+"/worktrees/fix", "piforge/fix"); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if err := provider.Destroy(ctx, "piforge-abc"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	requests := log.all()
	want := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/sandboxes/piforge-abc"},
		{http.MethodGet, "/v1/sandboxes/piforge-abc"},
		{http.MethodPost, "/v1/sandboxes/piforge-abc/pause"},
		{http.MethodPost, "/v1/sandboxes/piforge-abc/resume"},
		{http.MethodPost, "/v1/sandboxes/piforge-abc/worktrees"},
		{http.MethodDelete, "/v1/sandboxes/piforge-abc"},
	}
	if len(requests) != len(want) {
		t.Fatalf("%d requests, want %d: %+v", len(requests), len(want), requests)
	}
	for index, expected := range want {
		if requests[index].Method != expected.method || requests[index].Path != expected.path {
			t.Fatalf("request %d = %s %s, want %s %s",
				index, requests[index].Method, requests[index].Path, expected.method, expected.path)
		}
	}

	// The worktree body is the one place the host paths cross the wire, plus the
	// container key the runner needs to run git inside it.
	var worktree RunnerWorktreeRequest
	if err := json.Unmarshal([]byte(requests[4].Body), &worktree); err != nil {
		t.Fatalf("worktree body is not JSON: %v (%s)", err, requests[4].Body)
	}
	if worktree.RepoDir != record.RootDir+"/repo" ||
		worktree.Path != record.RootDir+"/worktrees/fix" ||
		worktree.Branch != "piforge/fix" {
		t.Fatalf("worktree body = %+v", worktree)
	}
}

// The store decides "rebuild or not" by errors.Is(err, sandbox.ErrNotFound), so a
// 404 has to survive the wrapping and everything else must not look like it.
func TestRunnerMapsHTTPErrors(t *testing.T) {
	dataRoot := t.TempDir()
	cases := []struct {
		name       string
		status     int
		body       string
		wantNotFnd bool
		wantInMsg  string
	}{
		{name: "404 is not found", status: http.StatusNotFound, body: `{"error":"no such container"}`, wantNotFnd: true},
		{name: "500 is an error", status: http.StatusInternalServerError, body: `{"error":"docker run failed"}`, wantInMsg: "docker run failed"},
		{name: "400 is a rejected request", status: http.StatusBadRequest, body: `{"error":"user_id is required"}`, wantInMsg: "user_id is required"},
		{name: "401 names the token", status: http.StatusUnauthorized, body: `{"error":"the bearer token does not match"}`, wantInMsg: "refused the token"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, _ := startTestRunner(t, dataRoot, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := provider.Get(context.Background(), "piforge-missing")
			if err == nil {
				t.Fatal("Get returned no error")
			}
			if errors.Is(err, ErrNotFound) != tc.wantNotFnd {
				t.Fatalf("errors.Is(ErrNotFound) = %v, want %v (err: %v)", errors.Is(err, ErrNotFound), tc.wantNotFnd, err)
			}
			if tc.wantInMsg != "" && !strings.Contains(err.Error(), tc.wantInMsg) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.wantInMsg)
			}
		})
	}
}

// A runner that accepts a connection and never answers must not hold a panel
// request forever: the client's own bound has to fire.
func TestRunnerTimesOutInsteadOfHanging(t *testing.T) {
	dataRoot := t.TempDir()
	provider, _ := startTestRunner(t, dataRoot, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	provider.cfg.EnsureTimeout = 80 * time.Millisecond

	started := time.Now()
	_, err := provider.Ensure(context.Background(), "usr_1", "prj_1", "", "main")
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("Ensure returned no error from a runner that never answered")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error does not wrap context.DeadlineExceeded: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Ensure took %s, the timeout did not fire", elapsed)
	}
}

// The control plane's half of the contract must not look up the Docker CLI. This
// is the property the runner exists for, so it is tested rather than assumed: the
// whole test runs with an empty PATH.
func TestRunnerNeedsNoDockerCLI(t *testing.T) {
	t.Setenv("PATH", "")
	dataRoot := t.TempDir()
	provider, log := startTestRunner(t, dataRoot, func(w http.ResponseWriter, r *http.Request) {
		writeRecord(t, w, RunnerRecord{
			ID:                "piforge-abc",
			State:             contracts.SandboxReady,
			RootDir:           filepath.Join(dataRoot, "usr_1", "prj_1"),
			ContainerPathRoot: ContainerWorkspace,
			Endpoint:          "http://127.0.0.1:49153",
		})
	})
	if _, err := provider.Ensure(context.Background(), "usr_1", "prj_1", "", "main"); err != nil {
		t.Fatalf("Ensure with an empty PATH: %v", err)
	}
	if len(log.all()) != 1 {
		t.Fatalf("the fake runner saw %d requests, want 1", len(log.all()))
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "usr_1")); !os.IsNotExist(err) {
		t.Fatalf("the provider created a workspace directory (stat err: %v); that is the runner's job", err)
	}
}

// ContainerPath is shared with the Docker provider, so the runner answers it
// without a round trip — and without a CLI.
func TestRunnerContainerPath(t *testing.T) {
	dataRoot := t.TempDir()
	provider, err := NewRunner(RunnerConfig{
		BaseURL:  "http://127.0.0.1:1",
		Token:    "test-token",
		DataRoot: dataRoot,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	workspace := filepath.Join(dataRoot, "usr_1", "prj_1")

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "workspace root", input: workspace, want: "/workspace"},
		{name: "main checkout", input: filepath.Join(workspace, "repo"), want: "/workspace/repo"},
		{name: "linked worktree", input: filepath.Join(workspace, "worktrees", "fix"), want: "/workspace/worktrees/fix"},
		{name: "already a container path", input: "/workspace/repo", want: "/workspace/repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := provider.ContainerPath(tc.input)
			if err != nil {
				t.Fatalf("ContainerPath(%q): %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("ContainerPath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
	if _, err := provider.ContainerPath(t.TempDir()); err == nil {
		t.Fatal("ContainerPath accepted a path outside the data root")
	}
}

// The mount root is part of the contract: this build maps every path to
// /workspace, so a runner that mounts somewhere else would make the panel address
// the wrong tree. Say so instead of using the wrong root.
func TestRunnerRejectsAnUnexpectedMountRoot(t *testing.T) {
	dataRoot := t.TempDir()
	provider, _ := startTestRunner(t, dataRoot, func(w http.ResponseWriter, r *http.Request) {
		writeRecord(t, w, RunnerRecord{
			ID:                "piforge-abc",
			State:             contracts.SandboxReady,
			RootDir:           filepath.Join(dataRoot, "usr_1", "prj_1"),
			ContainerPathRoot: "/srv/workspace",
			Endpoint:          "http://127.0.0.1:49153",
		})
	})
	_, err := provider.Ensure(context.Background(), "usr_1", "prj_1", "", "main")
	if err == nil {
		t.Fatal("Ensure accepted a runner with a different mount root")
	}
	if !strings.Contains(err.Error(), "/srv/workspace") || !strings.Contains(err.Error(), ContainerWorkspace) {
		t.Fatalf("error does not name both roots: %v", err)
	}
}

func TestNewRunnerRefusesIncompleteConfiguration(t *testing.T) {
	cases := []struct {
		name string
		cfg  RunnerConfig
		want string
	}{
		{name: "no base url", cfg: RunnerConfig{Token: "t", DataRoot: t.TempDir()}, want: "base URL"},
		{name: "wrong scheme", cfg: RunnerConfig{BaseURL: "tcp://runner:7801", Token: "t", DataRoot: t.TempDir()}, want: "http"},
		{name: "no token", cfg: RunnerConfig{BaseURL: "http://runner:7801", DataRoot: t.TempDir()}, want: "token"},
		{name: "no data root", cfg: RunnerConfig{BaseURL: "http://runner:7801", Token: "t"}, want: "data root"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewRunner(tc.cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewRunner = %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
}
