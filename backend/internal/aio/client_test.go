package aio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// The client is the only thing between PiForge and that image, so its wire format is
// worth pinning: these are the shapes documented in the repository's openapi.json.
func TestClientSpeaksTheSandboxAPI(t *testing.T) {
	var seenAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/browser/info", func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("X-AIO-API-Key")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"cdp_url":   "ws://127.0.0.1:9222/devtools/browser/abc",
			"workspace": "/home/gem",
		})
	})
	mux.HandleFunc("/v1/shell/exec", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["command"] != "echo hi" {
			t.Errorf("command = %v", body["command"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"output": "hi\n", "exit_code": 0},
		})
	})
	// Exec names a pinned session, so the client creates one first (idempotent).
	mux.HandleFunc("/v1/shell/sessions/create", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"session_id": body["id"], "working_dir": "/home/gem"},
		})
	})
	mux.HandleFunc("/v1/file/write", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["file"] != "/home/gem/a.txt" || body["content"] != "x" || body["encoding"] != "utf-8" {
			t.Errorf("write body = %v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "message": "File written successfully",
			"data": map[string]any{"file": "/home/gem/a.txt", "bytes_written": 1},
		})
	})
	mux.HandleFunc("/v1/file/read", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["file"] == "/home/gem/missing.txt" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": false,
				"message": "Failed to read file: no such file",
				"data": map[string]any{
					"path": "/home/gem/missing.txt", "operation": "read",
					"message":    "Failed to read file: no such file",
					"error_type": "not_found", "errno_name": "ENOENT",
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data":    map[string]any{"content": "hello\n", "file": "/home/gem/a.txt"},
		})
	})
	// The running image (1.11.0) reports is_directory and unix-seconds
	// modified_time; the test pins that shape, not the SDK's is_dir.
	mux.HandleFunc("/v1/file/list", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{"files": []map[string]any{
				{"name": "a.txt", "path": "/home/gem/a.txt", "is_directory": false, "size": 6, "modified_time": "1793250873"},
				{"name": "src", "path": "/home/gem/src", "is_directory": true, "size": nil, "modified_time": "1793250873"},
			}},
		})
	})
	mux.HandleFunc("/v1/file/glob", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["path"] != "/home/gem" || body["pattern"] != "*.txt" {
			t.Errorf("glob body = %v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{"files": []map[string]any{
				{"path": "/home/gem/a.txt", "name": "a.txt", "is_directory": false, "size": 6, "modified_time": "1793250873"},
			}},
		})
	})
	mux.HandleFunc("/v1/file/grep", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["pattern"] != "hel+o" || body["fixed_strings"] != false {
			t.Errorf("grep body = %v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"matches": []map[string]any{
					{"file": "/home/gem/a.txt", "line_number": 1, "line_content": "hello"},
				},
				"truncated": false,
			},
		})
	})
	mux.HandleFunc("/v1/browser/page/screenshot", func(w http.ResponseWriter, r *http.Request) {
		png := []byte("\x89PNG\r\n\x1a\nfake")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"image": base64.StdEncoding.EncodeToString(png)},
		})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, APIKey: "pf-test-key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	info, err := client.BrowserInfo(ctx)
	if err != nil {
		t.Fatalf("BrowserInfo: %v", err)
	}
	if info.CDPURL == "" {
		t.Fatal("no cdp url")
	}
	// The key reaches the sandbox: that container protects every service with it.
	if seenAuth != "pf-test-key" {
		t.Fatalf("api key header = %q", seenAuth)
	}

	result, err := client.Exec(ctx, "echo hi", "")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.Output != "hi\n" || result.ExitCode != 0 {
		t.Fatalf("exec = %+v", result)
	}

	file, err := client.ReadFile(ctx, "/home/gem/a.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if file.Content != "hello\n" {
		t.Fatalf("content = %q", file.Content)
	}
	if err := client.WriteFile(ctx, "/home/gem/a.txt", "x"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	nodes, err := client.List(ctx, "/home/gem")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != 2 || !nodes[1].IsDir {
		t.Fatalf("nodes = %+v", nodes)
	}
	if nodes[0].Size != 6 || nodes[0].Modified.IsZero() {
		t.Fatalf("list metadata = %+v", nodes[0])
	}

	hits, err := client.Glob(ctx, "/home/gem", "*.txt", GlobOptions{})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(hits) != 1 || hits[0].Path != "/home/gem/a.txt" {
		t.Fatalf("glob = %+v", hits)
	}

	grep, err := client.Grep(ctx, "/home/gem", "hel+o", false, 10)
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(grep.Matches) != 1 || grep.Matches[0].Line != 1 || grep.Matches[0].Text != "hello" {
		t.Fatalf("grep = %+v", grep)
	}

	// A file endpoint failure is HTTP 200 with success:false. It has to become
	// an error, or a missing file reads as an empty one.
	if _, err := client.ReadFile(ctx, "/home/gem/missing.txt"); err == nil {
		t.Fatal("a not_found answer returned no error")
	} else if !IsNotFound(err) {
		t.Fatalf("IsNotFound(%v) = false", err)
	}

	shot, err := client.Screenshot(ctx)
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if len(shot) == 0 || shot[0] != 0x89 {
		t.Fatalf("screenshot is not a png: %x", shot)
	}
}

// A sandbox that answers an error must surface it, not an empty success.
func TestClientSurfacesErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"missing api key"}`))
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Exec(context.Background(), "true", ""); err == nil {
		t.Fatal("an unauthorized answer returned no error")
	}
}

// OpenFile is the one file call that must not go through the JSON envelope:
// the download endpoint answers the bytes themselves, and only a raw body can
// carry a PNG.
func TestOpenFileStreamsRawBytes(t *testing.T) {
	binary := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff}
	var seenPath, seenKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/file/download" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		seenPath = r.URL.Query().Get("path")
		seenKey = r.Header.Get("X-AIO-API-Key")
		if seenPath == "/home/gem/missing.png" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"message":"File does not exist: /home/gem/missing.png"}`))
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "10")
		_, _ = w.Write(binary)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, APIKey: "pf-test-key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	body, size, err := client.OpenFile(ctx, "/home/gem/icon.png")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	got, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, binary) || size != int64(len(binary)) {
		t.Fatalf("stream = %x size=%d", got, size)
	}
	if seenPath != "/home/gem/icon.png" {
		t.Fatalf("path query = %q", seenPath)
	}
	if seenKey != "pf-test-key" {
		t.Fatalf("api key header = %q", seenKey)
	}

	_, _, err = client.OpenFile(ctx, "/home/gem/missing.png")
	if err == nil || !IsNotFound(err) {
		t.Fatalf("missing download error = %v", err)
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotFound {
		t.Fatalf("error is not a 404 HTTPError: %#v", err)
	}
}

// ReadPrefix is the preview's probe: a Range request on the download endpoint,
// so the caller can classify bytes the text reader would reject.
func TestReadPrefixFetchesOnlyTheRequestedWindow(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff}
	var seenRange, seenPath, seenKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/file/download" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		seenRange = r.Header.Get("Range")
		seenPath = r.URL.Query().Get("path")
		seenKey = r.Header.Get("X-AIO-API-Key")
		if seenPath == "/home/gem/missing.png" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"message":"File does not exist: /home/gem/missing.png"}`))
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Range", "bytes 0-9/70")
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(png)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, APIKey: "pf-test-key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := client.ReadPrefix(context.Background(), "/home/gem/icon.png", 10)
	if err != nil {
		t.Fatalf("ReadPrefix: %v", err)
	}
	if !bytes.Equal(got, png) {
		t.Fatalf("prefix = %x, want %x", got, png)
	}
	if seenRange != "bytes=0-9" {
		t.Fatalf("range header = %q", seenRange)
	}
	if seenPath != "/home/gem/icon.png" || seenKey != "pf-test-key" {
		t.Fatalf("path = %q key = %q", seenPath, seenKey)
	}

	if _, err := client.ReadPrefix(context.Background(), "/home/gem/missing.png", 10); !IsNotFound(err) {
		t.Fatalf("missing prefix error = %v", err)
	}
}

// Only the image's typed decode failure may become "this file is binary";
// every other error has to keep failing.
func TestIsDecodeErrorMatchesOnlyTheTypedFailure(t *testing.T) {
	if !IsDecodeError(&OpError{Type: "decode_error"}) {
		t.Fatal("decode_error was not recognised")
	}
	for _, err := range []error{
		&OpError{Type: "io_error"},
		&OpError{Type: "not_found"},
		&HTTPError{Status: http.StatusInternalServerError},
		errors.New("'utf-8' codec can't decode byte 0x89"),
	} {
		if IsDecodeError(err) {
			t.Fatalf("%v was treated as a decode failure", err)
		}
	}
}

func TestNewValidatesItsConfig(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("an empty base url was accepted")
	}
	client, err := New(Config{BaseURL: "http://127.0.0.1:8080/"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if client.BaseURL() != "http://127.0.0.1:8080" {
		t.Fatalf("base url = %q", client.BaseURL())
	}
	if client.Root != "/home/gem" {
		t.Fatalf("default root = %q", client.Root)
	}
}

// A command names a pinned shell session, and the session is created once.
//
// Without an id the image creates a session per exec, keeps ten and evicts the
// oldest idle one — which killed the user's terminal while provisioning or an
// agent startup was running execs (measured on the isolated deployment).
func TestExecPinsItsShellSessions(t *testing.T) {
	var mu sync.Mutex
	var creates []string
	var execIDs []string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/shell/sessions/create", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		creates = append(creates, body.ID)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"session_id": body.ID, "working_dir": "/home/gem"},
		})
	})
	mux.HandleFunc("/v1/shell/exec", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		execIDs = append(execIDs, body.ID)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"output": "ok\n", "exit_code": 0},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, ExecSessions: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if _, err := client.Exec(ctx, "true", ""); err != nil {
			t.Fatalf("Exec %d: %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for _, id := range execIDs {
		if id == "" {
			t.Fatal("an exec was sent without a session id; the image will create a new session for it")
		}
	}
	if len(execIDs) != 4 {
		t.Fatalf("exec ids = %v", execIDs)
	}
	if execIDs[0] == execIDs[1] {
		t.Fatalf("two consecutive execs shared one session (%v); the pool is not round-robining", execIDs)
	}
	// Four execs, two sessions: each session is created once, not once per call.
	if len(creates) != 2 {
		t.Fatalf("creates = %v, want one per pinned session", creates)
	}
}

// A second client for the same sandbox must not re-create the sessions: every
// component builds its own client, and the file panel would otherwise create two
// sessions per request.
func TestExecSessionCreationIsCachedPerSandbox(t *testing.T) {
	var mu sync.Mutex
	creates := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/shell/sessions/create", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		creates++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"session_id": body.ID, "working_dir": "/home/gem"},
		})
	})
	mux.HandleFunc("/v1/shell/exec", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"output": "ok\n", "exit_code": 0},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	for i := 0; i < 3; i++ {
		client, err := New(Config{BaseURL: server.URL, ExecSessions: 2})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, err := client.Exec(context.Background(), "true", ""); err != nil {
			t.Fatalf("Exec: %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if creates != 2 {
		t.Fatalf("creates = %d across three clients, want 2 (one per pinned session)", creates)
	}
}

// An evicted session is re-created once and the command is retried, so a sandbox
// restart does not turn every later file operation into a 404.
func TestExecRecoversFromAnEvictedSession(t *testing.T) {
	var mu sync.Mutex
	creates, execs := 0, 0
	evicted := false
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/shell/sessions/create", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		creates++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"session_id": body.ID, "working_dir": "/home/gem"},
		})
	})
	mux.HandleFunc("/v1/shell/exec", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		execs++
		gone := evicted
		evicted = false // one 404, then the re-created session works
		mu.Unlock()
		if gone {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"message":"Session pf-exec-recover-0 not found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"output": "ok\n", "exit_code": 0},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, ExecSessions: 2, ExecSessionPrefix: "pf-exec-recover"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if _, err := client.Exec(ctx, "true", ""); err != nil {
		t.Fatalf("first Exec: %v", err)
	}
	mu.Lock()
	evicted = true
	mu.Unlock()
	if _, err := client.Exec(ctx, "true", ""); err != nil {
		t.Fatalf("Exec after eviction: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if execs != 3 {
		t.Fatalf("execs = %d, want 3 (one failed, one retried, one after)", execs)
	}
	if creates != 3 {
		t.Fatalf("creates = %d, want 3 (two sessions plus the re-create)", creates)
	}
}
