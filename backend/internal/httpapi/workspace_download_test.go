package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/workspaceremote"
)

// fixedRemoteBackend resolves every root to one sandbox target, which is all a
// download test needs from cmd/piforged's real backend.
type fixedRemoteBackend struct{ target workspaceremote.Target }

func (b fixedRemoteBackend) Target(context.Context, string) (workspaceremote.Target, error) {
	return b.target, nil
}

// TestReadFilePreviewStillClassifiesBinaryAndTruncation pins the non-download
// path: it must keep returning the preview JSON, classified and truncated, and
// must never turn into a raw stream.
func TestReadFilePreviewStillClassifiesBinaryAndTruncation(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, sess := env.mkSessionFor(user.ID, "repo")
	env.withWorktree(user.ID, project.ID, sess.ID)
	base := "/v1/sessions/" + sess.ID

	env.workspace.content = &contracts.FileContent{
		Path: "icon.png", Size: 70, Binary: true, Truncated: true, MimeType: "image/png",
	}
	rec := env.doAuthed(http.MethodGet, base+"/files?path=icon.png", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("binary preview status = %d body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("a preview was served as an attachment: %q", got)
	}
	var binary contracts.FileContent
	env.mustJSON(rec, &binary)
	if !binary.Binary || !binary.Truncated || binary.Body != "" || binary.MimeType != "image/png" {
		t.Fatalf("binary preview = %+v", binary)
	}

	preview := "head\n\n… (truncated) …\n\ntail"
	env.workspace.content = &contracts.FileContent{
		Path: "big.txt", Size: 30 << 20, Body: preview, Truncated: true, MimeType: "text/plain",
	}
	rec = env.doAuthed(http.MethodGet, base+"/files?path=big.txt", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("text preview status = %d body %s", rec.Code, rec.Body.String())
	}
	var text contracts.FileContent
	env.mustJSON(rec, &text)
	if text.Body != preview || !text.Truncated || text.Binary {
		t.Fatalf("text preview = %+v", text)
	}
}

// TestReadFileDownloadFromRemoteStreamsRawBytes is the end-to-end handler test
// for -sandbox-files remote: the workspace is a real workspaceremote over an
// httptest AIO server, and the PNG bytes must arrive exactly. The fake server
// fails /v1/file/read, proving the download never asks for a text decode.
func TestReadFileDownloadFromRemoteStreamsRawBytes(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, sess := env.mkSessionFor(user.ID, "repo")
	env.withWorktree(user.ID, project.ID, sess.ID)
	base := "/v1/sessions/" + sess.ID

	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff, 0xfe, 0x01}
	// Larger than the 2 MiB preview window: the old fallback would have served a
	// truncated preview body here.
	large := bytes.Repeat([]byte("0123456789abcdef"), 3<<20/16)
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/file/download":
			if got := r.Header.Get("X-AIO-API-Key"); got != "remote-key" {
				t.Errorf("api key = %q", got)
			}
			q := r.URL.Query().Get("path")
			var payload []byte
			switch q {
			case "/workspace/repo/icon.png":
				payload = png
			case "/workspace/repo/large.txt":
				payload = large
			default:
				t.Errorf("download path = %q", q)
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload)
		case "/v1/file/read":
			reads++
			http.Error(w, "binary is not UTF-8", http.StatusInternalServerError)
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

	rec := env.doAuthed(http.MethodGet, base+"/files?path=icon.png&download=1", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("content type = %q", got)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(png)) {
		t.Fatalf("content length = %q, want %d", got, len(png))
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="icon.png"` {
		t.Fatalf("content disposition = %q", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), png) {
		t.Fatalf("body = %x, want %x", rec.Body.Bytes(), png)
	}

	rec = env.doAuthed(http.MethodGet, base+"/files?path=large.txt&download=1", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("large status = %d body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain" {
		t.Fatalf("large content type = %q", got)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(large)) {
		t.Fatalf("large content length = %q, want %d", got, len(large))
	}
	if !bytes.Equal(rec.Body.Bytes(), large) {
		t.Fatalf("large body is %d bytes, want %d", rec.Body.Len(), len(large))
	}
	if reads != 0 {
		t.Fatalf("download hit /v1/file/read %d times", reads)
	}
}
