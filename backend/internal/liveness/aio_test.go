package liveness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeSandbox is a stand-in for an AIO sandbox: it answers /v1/shell/exec the
// way the image does (a `data` envelope), and it evaluates the marker command
// against a real temporary workspace instead of pretending to. That keeps the
// command this package builds — quoting, tokens, stat — under test, rather than
// testing a stub that returns the answer the test wanted.
type fakeSandbox struct {
	root     string // host directory mounted at /workspace
	failures int    // number of upcoming requests to fail with HTTP 500
	garbage  bool   // answer an unexpected token
	commands []string
	keys     []string
	server   *httptest.Server
}

var markerPathPattern = regexp.MustCompile(`f='([^']*)'`)

func newFakeSandbox(t *testing.T) *fakeSandbox {
	t.Helper()
	fs := &fakeSandbox{root: t.TempDir()}
	mux := http.NewServeMux()
	// Exec names a pinned shell session, so the client creates one first.
	mux.HandleFunc("/v1/shell/sessions/create", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"session_id": body.ID, "working_dir": "/workspace"},
		})
	})
	mux.HandleFunc("/v1/shell/exec", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Command string `json:"command"`
			Cwd     string `json:"cwd"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "body", http.StatusBadRequest)
			return
		}
		fs.commands = append(fs.commands, body.Command)
		fs.keys = append(fs.keys, r.Header.Get("X-AIO-API-Key"))
		if fs.failures > 0 {
			fs.failures--
			http.Error(w, "sandbox is on fire", http.StatusInternalServerError)
			return
		}
		output := fs.evaluate(body.Command)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"output": output, "exit_code": 0},
		})
	})
	fs.server = httptest.NewServer(mux)
	t.Cleanup(fs.server.Close)
	return fs
}

// evaluate runs the marker command's intent against the fake workspace.
func (f *fakeSandbox) evaluate(command string) string {
	if f.garbage {
		return "surprise"
	}
	match := markerPathPattern.FindStringSubmatch(command)
	if match == nil {
		return tokenNoStat
	}
	marker := strings.TrimPrefix(match[1], "/workspace")
	hostPath := filepath.Join(f.root, filepath.FromSlash(strings.TrimPrefix(marker, "/")))
	dir := path.Dir(marker)
	hostDir := filepath.Join(f.root, filepath.FromSlash(strings.TrimPrefix(dir, "/")))
	if _, err := os.Stat(hostDir); err != nil {
		return tokenNoDir
	}
	info, err := os.Stat(hostPath)
	if err != nil {
		return tokenNoMarker
	}
	return strconv.FormatInt(info.ModTime().Unix(), 10)
}

// touch writes the marker the launcher would have written.
func (f *fakeSandbox) touch(t *testing.T, sessionID string, at time.Time) string {
	t.Helper()
	rel, err := MarkerRelPath(sessionID)
	if err != nil {
		t.Fatalf("MarkerRelPath: %v", err)
	}
	full := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte("beat\n"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := os.Chtimes(full, at, at); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return full
}

func (f *fakeSandbox) reader(t *testing.T) *AIO {
	t.Helper()
	reader, err := NewAIO(AIOConfig{
		Locate: func(context.Context, string) (string, error) { return "sbx_test", nil },
		Endpoint: func(context.Context, string) (string, error) {
			return f.server.URL, nil
		},
		APIKey: func(context.Context, string) (string, error) { return "sandbox-key", nil },
	})
	if err != nil {
		t.Fatalf("NewAIO: %v", err)
	}
	return reader
}

// TestSandboxProbeReadsTheMarkerThroughAFakeSandbox is requirement (1) for the
// container providers, exercised through the real wire path: the marker is
// present and fresh → alive; removed → dead; the sandbox errors → unknown.
func TestSandboxProbeReadsTheMarkerThroughAFakeSandbox(t *testing.T) {
	ctx := context.Background()
	sandbox := newFakeSandbox(t)
	now := time.Now()
	probe := NewSandbox(SandboxConfig{Reader: sandbox.reader(t), Now: func() time.Time { return now }})

	// No marker yet: no launcher-run agent in this sandbox.
	alive, err := probe.Alive(ctx, "ses_remote")
	if err != nil {
		t.Fatalf("Alive (absent): %v", err)
	}
	if alive {
		t.Fatal("a session with no marker was reported alive")
	}

	// The launcher wrote a beat: alive.
	sandbox.touch(t, "ses_remote", now.Add(-2*time.Second))
	alive, err = probe.Alive(ctx, "ses_remote")
	if err != nil {
		t.Fatalf("Alive (fresh): %v", err)
	}
	if !alive {
		t.Fatal("a fresh marker was reported dead")
	}

	// A marker older than the lease: the writer stopped, so the agent is gone.
	sandbox.touch(t, "ses_remote", now.Add(-3*DefaultTTL))
	alive, err = probe.Alive(ctx, "ses_remote")
	if err != nil {
		t.Fatalf("Alive (stale): %v", err)
	}
	if alive {
		t.Fatal("a stale marker was reported alive")
	}

	// The sandbox is broken: unknown, which is an error, never "dead".
	sandbox.failures = 1
	if _, err := probe.Alive(ctx, "ses_remote"); err == nil {
		t.Fatal("an unreachable sandbox was turned into an answer")
	}

	// The answer cannot be parsed: also unknown.
	sandbox.garbage = true
	if _, err := probe.Alive(ctx, "ses_remote"); err == nil {
		t.Fatal("a garbled sandbox answer was turned into an answer")
	}
	sandbox.garbage = false

	// The key the store holds is the key presented, and the command names the
	// session's marker inside the container workspace.
	last := sandbox.commands[len(sandbox.commands)-1]
	if !strings.Contains(last, "/workspace/.piforge/live/ses_remote") {
		t.Fatalf("the probe asked about the wrong path: %s", last)
	}
	for _, key := range sandbox.keys {
		if key != "sandbox-key" {
			t.Fatalf("the probe presented key %q", key)
		}
	}
}

// TestSandboxProbeNeedsALocator keeps a half-wired reader from answering.
func TestSandboxProbeNeedsALocator(t *testing.T) {
	if _, err := NewAIO(AIOConfig{}); err == nil {
		t.Fatal("NewAIO accepted a config with no locator")
	}
	if _, err := NewAIO(AIOConfig{
		Locate:   func(context.Context, string) (string, error) { return "s", nil },
		Endpoint: func(context.Context, string) (string, error) { return "http://x", nil },
	}); err == nil {
		t.Fatal("NewAIO accepted a config with no API key resolver")
	}
}

// TestCompositeWithABrokenSandboxIsUnknown is the restart-with-a-broken-sandbox
// case end to end: the registry is empty (new process), the sandbox cannot
// answer, so the composite must say unknown and reconciliation must leave the
// session alone.
func TestCompositeWithABrokenSandboxIsUnknown(t *testing.T) {
	ctx := context.Background()
	sandbox := newFakeSandbox(t)
	sandbox.failures = 1
	registry := newFakeRegistry() // empty: nothing spawned in this process

	probe := NewComposite(
		NewProcess(registry),
		NewSandbox(SandboxConfig{Reader: sandbox.reader(t)}),
	)
	alive, err := probe.Alive(ctx, "ses_remote")
	if err == nil {
		t.Fatalf("composite answered %v while the sandbox was broken", alive)
	}
	if alive {
		t.Fatal("composite reported alive from a broken sandbox")
	}
}

// TestCompositeSeesALiveRemoteAgentAfterARestart is the exact defect: a fresh
// process (empty registry) with a remote agent that is still beating.
func TestCompositeSeesALiveRemoteAgentAfterARestart(t *testing.T) {
	ctx := context.Background()
	sandbox := newFakeSandbox(t)
	sandbox.touch(t, "ses_remote", time.Now())
	registry := newFakeRegistry() // a restart: nothing in this process

	probe := NewComposite(
		NewProcess(registry),
		NewSandbox(SandboxConfig{Reader: sandbox.reader(t)}),
	)
	alive, err := probe.Alive(ctx, "ses_remote")
	if err != nil {
		t.Fatalf("Alive: %v", err)
	}
	if !alive {
		t.Fatal("a live remote agent was reported dead after a restart")
	}
}
