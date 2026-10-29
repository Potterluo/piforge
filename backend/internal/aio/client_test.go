package aio

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	mux.HandleFunc("/v1/file/write", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/v1/file/read", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"content": "hello\n", "path": "/home/gem/a.txt"},
		})
	})
	mux.HandleFunc("/v1/file/list", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"files": []map[string]any{
				{"name": "a.txt", "path": "/home/gem/a.txt", "is_dir": false, "size": 6},
				{"name": "src", "path": "/home/gem/src", "is_dir": true, "size": 0},
			}},
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
