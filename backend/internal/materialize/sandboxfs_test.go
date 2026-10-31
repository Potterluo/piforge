package materialize

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/piforge/piforge/backend/internal/aio"
)

// recordingSandbox is a stand-in for the sandbox's HTTP API: it records what the
// filesystem asks for and answers in the shapes the image uses.
//
// The point is not to simulate a shell. It is to pin the *request*: a container
// path is POSIX, every write names the full path, and the read-only mechanism is
// one chmod plus one probe whose answer decides the badge. A test against a fake
// shell would only prove that the fake agrees with this file.
type recordingSandbox struct {
	mu       sync.Mutex
	writes   map[string]string
	commands []string
	reads    []string
	lists    []string
	// probe is what the shell answers to the read-only probe.
	probe string
}

func (s *recordingSandbox) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	// Exec names a pinned shell session (aio.Client.Exec), so the client creates
	// one before its first command.
	mux.HandleFunc("/v1/shell/sessions/create", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(t, w, map[string]any{
			"data": map[string]any{"session_id": body.ID, "working_dir": "/workspace"},
		})
	})
	mux.HandleFunc("/v1/shell/exec", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Command string `json:"command"`
			Cwd     string `json:"cwd"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.commands = append(s.commands, body.Command)
		probe := s.probe
		s.mu.Unlock()
		output := ""
		if strings.Contains(body.Command, "writable") {
			output = probe
		}
		if strings.Contains(body.Command, "then printf file") {
			// Lookup: the fake is asked about a directory that exists.
			output = "dir"
		}
		writeJSON(t, w, map[string]any{"data": map[string]any{"output": output, "exit_code": 0}})
	})
	mux.HandleFunc("/v1/file/write", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			File     string `json:"file"`
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		if s.writes == nil {
			s.writes = map[string]string{}
		}
		s.writes[body.File] = body.Content
		s.mu.Unlock()
		writeJSON(t, w, map[string]any{"success": true, "data": map[string]any{}})
	})
	mux.HandleFunc("/v1/file/download", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reads = append(s.reads, r.URL.Query().Get("path"))
		s.mu.Unlock()
		w.Header().Set("Accept-Encoding", "identity")
		_, _ = w.Write([]byte("SKILL BODY"))
	})
	mux.HandleFunc("/v1/file/list", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.lists = append(s.lists, body.Path)
		s.mu.Unlock()
		writeJSON(t, w, map[string]any{
			"success": true,
			"data": map[string]any{"files": []map[string]any{
				{"name": "SKILL.md", "path": body.Path + "/SKILL.md", "is_directory": false, "size": 10, "modified_time": "0"},
			}},
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		t.Logf("unexpected request: %s %s %s", r.Method, r.URL.Path, body)
		w.WriteHeader(http.StatusNotFound)
	})
	return mux
}

func writeJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

// The sandbox filesystem has to speak the API the panels speak: absolute
// container paths, one write per file, and a read-only step that is a chmod plus a
// probe run *in the sandbox* (the account the agent runs as is the only one whose
// answer means anything).
func TestSandboxFSSpeaksTheFileAPI(t *testing.T) {
	fake := &recordingSandbox{probe: "readonly"}
	server := httptest.NewServer(fake.handler(t))
	defer server.Close()

	client, err := aio.New(aio.Config{BaseURL: server.URL, Root: "/workspace"})
	if err != nil {
		t.Fatalf("aio.New: %v", err)
	}
	fs, err := NewSandboxFS(SandboxConfig{Client: client, Root: "/workspace"})
	if err != nil {
		t.Fatalf("NewSandboxFS: %v", err)
	}
	ctx := context.Background()
	_ = ctx

	if fs.CanBind() {
		t.Fatal("a file API claimed it can bind-mount")
	}
	if !fs.IsAbs("/workspace/repo") || fs.IsAbs("repo") {
		t.Fatal("IsAbs does not answer for container paths")
	}
	if got := fs.Join("/workspace", ".pi/skills/x"); got != "/workspace/.pi/skills/x" {
		t.Fatalf("Join = %q", got)
	}
	if err := fs.MkdirAll("/workspace/repo/.pi/skills/pdf"); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := fs.WriteFile("/workspace/repo/.pi/skills/pdf/SKILL.md", []byte("body"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if fake.writes["/workspace/repo/.pi/skills/pdf/SKILL.md"] != "body" {
		t.Fatalf("writes = %v", fake.writes)
	}
	if kind, err := fs.Lookup("/workspace/repo/.pi/skills/pdf"); err != nil || kind != KindDir {
		t.Fatalf("Lookup = %v, %v", kind, err)
	}
	writable, err := fs.MakeReadOnly("/workspace/repo/.pi/skills/pdf")
	if err != nil {
		t.Fatalf("MakeReadOnly: %v", err)
	}
	if writable {
		t.Fatal("MakeReadOnly reported writable for a readonly answer")
	}
	if _, err := fs.ReadFile("/workspace/repo/.pi/skills/pdf/SKILL.md"); err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(fake.reads) != 1 || fake.reads[0] != "/workspace/repo/.pi/skills/pdf/SKILL.md" {
		t.Fatalf("reads = %v: the download endpoint takes the container path", fake.reads)
	}
	if _, err := fs.List("/workspace/repo/.pi/skills/pdf"); err != nil {
		t.Fatalf("List: %v", err)
	}
	if err := fs.RemoveAll("/workspace/repo/.pi/skills/pdf"); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	joined := strings.Join(fake.commands, "\n")
	for _, want := range []string{
		// Every command runs with the workspace as its working directory, as the
		// sandbox user, because the client prefixes `cd -- <root> &&`.
		"cd -- '/workspace' && mkdir -p -- '/workspace/repo/.pi/skills/pdf'",
		// The read-only step is one chmod over the tree plus one probe, and the
		// probe prefers SKILL.md (the file an agent would edit).
		"chmod -R a-w -- '/workspace/repo/.pi/skills/pdf'",
		`[ -w "$probe" ]`,
		// Removing a frozen tree clears the write bits it does not need first.
		"chmod -R u+w -- '/workspace/repo/.pi/skills/pdf'",
		"rm -rf -- '/workspace/repo/.pi/skills/pdf'",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("commands did not contain %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "exit ") {
		t.Fatalf("a command used `exit`, which wedges the image's shell session:\n%s", joined)
	}
}

// A sandbox whose account can still write the tree (root) must report the truth:
// the mechanism is a frozen copy, not a read-only one.
func TestSandboxFSReportsAWritableTree(t *testing.T) {
	fake := &recordingSandbox{probe: "writable"}
	server := httptest.NewServer(fake.handler(t))
	defer server.Close()
	client, err := aio.New(aio.Config{BaseURL: server.URL, Root: "/workspace"})
	if err != nil {
		t.Fatalf("aio.New: %v", err)
	}
	fs, err := NewSandboxFS(SandboxConfig{Client: client, Root: "/workspace"})
	if err != nil {
		t.Fatalf("NewSandboxFS: %v", err)
	}
	writable, err := fs.MakeReadOnly("/workspace/repo/.pi/skills/x")
	if err != nil || !writable {
		t.Fatalf("MakeReadOnly = %v, %v; want writable", writable, err)
	}
}

// A missing client or root is a wiring mistake, and it is reported at
// construction rather than on the first write.
func TestNewSandboxFSValidatesItsConfig(t *testing.T) {
	if _, err := NewSandboxFS(SandboxConfig{}); err == nil {
		t.Fatal("a filesystem without a client was accepted")
	}
	client, err := aio.New(aio.Config{BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("aio.New: %v", err)
	}
	if _, err := NewSandboxFS(SandboxConfig{Client: client}); err == nil || !strings.Contains(err.Error(), "workspace root") {
		t.Fatalf("NewSandboxFS = %v, want an error naming the missing root", err)
	}
}
