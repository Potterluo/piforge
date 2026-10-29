package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/workspaceremote"
)

// aiGlobResponse is the shape /v1/file/glob uses for one exact-name stat: the
// preview needs the size before it can classify anything.
func aiGlobResponse(w http.ResponseWriter, name string, size int) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"data": map[string]any{"files": []map[string]any{{
			"name":          name,
			"path":          "/workspace/repo/" + name,
			"is_directory":  false,
			"size":          size,
			"modified_time": "1793250873",
		}}},
	})
}

// TestReadFileRemotePreviewOfPNGIsBinaryNotAnError is the handler-level proof
// for the reported bug: -sandbox-files remote used to answer 500 because
// /v1/file/read decodes as UTF-8. The preview must now probe the leading bytes
// and return the same binary payload the local workspace returns.
func TestReadFileRemotePreviewOfPNGIsBinaryNotAnError(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, sess := env.mkSessionFor(user.ID, "repo")
	env.withWorktree(user.ID, project.ID, sess.ID)
	base := "/v1/sessions/" + sess.ID

	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff, 0xfe, 0x01}
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/file/glob":
			aiGlobResponse(w, "icon.png", len(png))
		case "/v1/file/download":
			if got := r.URL.Query().Get("path"); got != "/workspace/repo/icon.png" {
				t.Errorf("download path = %q", got)
			}
			if got := r.Header.Get("Range"); got != "bytes=0-8191" {
				t.Errorf("range = %q, want the 8 KiB preview window", got)
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.Itoa(len(png)))
			w.Header().Set("Content-Range", "bytes 0-11/12")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(png)
		case "/v1/file/read":
			reads++
			// The real image's answer for this file. The probe must keep the
			// handler from ever needing it.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": false,
				"message": "Failed to read file: 'utf-8' codec can't decode byte 0x89 in position 0: invalid start byte",
				"data": map[string]any{
					"path": "/workspace/repo/icon.png", "operation": "read",
					"message":    "Failed to read file: 'utf-8' codec can't decode byte 0x89 in position 0: invalid start byte",
					"error_type": "decode_error", "retryable": false,
					"errno": nil, "errno_name": nil, "exception_type": "UnicodeDecodeError",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := aio.New(aio.Config{BaseURL: srv.URL, APIKey: "remote-key", Root: "/workspace"})
	if err != nil {
		t.Fatalf("aio.New: %v", err)
	}
	remote, err := workspaceremote.New(workspaceremote.Config{
		Backend: fixedRemoteBackend{target: workspaceremote.Target{Client: client, Root: "/workspace/repo"}},
	})
	if err != nil {
		t.Fatalf("workspaceremote.New: %v", err)
	}
	env.deps.Workspace = remote
	env.rebuild()

	rec := env.doAuthed(http.MethodGet, base+"/files?path=icon.png", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("binary preview status = %d body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("a preview was served as an attachment: %q", got)
	}
	var preview contracts.FileContent
	env.mustJSON(rec, &preview)
	if !preview.Binary {
		t.Fatalf("binary = false: %+v", preview)
	}
	if preview.Body != "" {
		t.Fatalf("binary preview carried a body: %q", preview.Body)
	}
	if preview.Size != int64(len(png)) {
		t.Fatalf("size = %d, want %d", preview.Size, len(png))
	}
	if preview.MimeType != "image/png" {
		t.Fatalf("mime = %q", preview.MimeType)
	}
	if reads != 0 {
		t.Fatalf("the probe still asked /v1/file/read %d times", reads)
	}
}

// TestReadFileRemotePreviewKeepsGenuineErrors pins the other half: only the
// image's typed decode failure becomes the binary result. A real read failure
// must stay a 500 rather than quietly rendering as an empty binary preview.
func TestReadFileRemotePreviewKeepsGenuineErrors(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, sess := env.mkSessionFor(user.ID, "repo")
	env.withWorktree(user.ID, project.ID, sess.ID)
	base := "/v1/sessions/" + sess.ID

	body := []byte("hello")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/file/glob":
			aiGlobResponse(w, "notes.txt", len(body))
		case "/v1/file/download":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body)
		case "/v1/file/read":
			http.Error(w, "disk on fire", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := aio.New(aio.Config{BaseURL: srv.URL, APIKey: "remote-key", Root: "/workspace"})
	if err != nil {
		t.Fatalf("aio.New: %v", err)
	}
	remote, err := workspaceremote.New(workspaceremote.Config{
		Backend: fixedRemoteBackend{target: workspaceremote.Target{Client: client, Root: "/workspace/repo"}},
	})
	if err != nil {
		t.Fatalf("workspaceremote.New: %v", err)
	}
	env.deps.Workspace = remote
	env.rebuild()

	rec := env.doAuthed(http.MethodGet, base+"/files?path=notes.txt", nil, c)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("a genuine read failure became %d: %s", rec.Code, rec.Body.String())
	}
}
