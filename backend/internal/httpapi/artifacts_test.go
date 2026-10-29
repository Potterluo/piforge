package httpapi

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/fsx"
	"github.com/piforge/piforge/backend/internal/store"
)

// attachScreenshotWorktree gives the session a checkout the test controls, and
// returns it. The path is outside the artifacts directory on purpose: the two
// trees must stay visibly separate.
func attachScreenshotWorktree(t *testing.T, env *testEnv, userID string, project *contracts.Project, sess *contracts.Session) string {
	t.Helper()
	checkout := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatalf("create checkout: %v", err)
	}
	ctx := context.Background()
	sb, err := env.store.CreateSandbox(ctx, userID, store.NewSandbox{
		ProjectID: project.ID, State: contracts.SandboxReady, Provider: "local", RootDir: filepath.Dir(checkout),
	})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	wt, err := env.store.CreateWorktree(ctx, userID, store.NewWorktree{
		SandboxID: sb.ID, Path: checkout, Branch: "main",
	})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if _, err := env.store.UpdateSession(ctx, userID, sess.ID, store.SessionPatch{WorktreeID: &wt.ID}); err != nil {
		t.Fatalf("attach worktree: %v", err)
	}
	return checkout
}

// TestScreenshotWritesOutsideTheCheckoutAndIsServable is the regression for the
// drill's fragile point 6.5: a capture used to add an untracked screenshots/
// directory to the checkout. The test goes through the real POST handler (the
// capture itself is injected — launching Chrome belongs to browser's own
// tests), then through the real GET route the panel uses.
func TestScreenshotWritesOutsideTheCheckoutAndIsServable(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("shot@piforge.local", "pw", contracts.RoleUser)
	project, sess := env.mkSessionFor(user.ID, "repo")
	checkout := attachScreenshotWorktree(t, env, user.ID, project, sess)

	// A real 1x1 PNG: fsx classifies content by its bytes, and an ASCII stand-in
	// would be reported as text with an image mime type.
	png, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Y9Zl1sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	env.deps.ArtifactsDir = filepath.Join(t.TempDir(), "artifacts")
	// Real fsx, so "nothing was written under the checkout" is a filesystem
	// fact and not a property of a fake recorder.
	env.deps.Workspace = fsx.New(fsx.Config{})
	env.deps.ScreenshotCapture = func(context.Context, string, string, string) ([]byte, error) {
		return png, nil
	}
	env.rebuild()

	c := env.login("shot@piforge.local", "pw")
	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/browser/screenshot", nil, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("screenshot = %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Path  string `json:"path"`
		Bytes int    `json:"bytes"`
	}
	env.mustJSON(rec, &body)
	wantPrefix := "artifacts/" + sess.ID + "/screenshots/"
	if !strings.HasPrefix(body.Path, wantPrefix) || !strings.HasSuffix(body.Path, ".png") {
		t.Fatalf("path = %q, want %s*.png", body.Path, wantPrefix)
	}
	if body.Bytes != len(png) {
		t.Fatalf("bytes = %d, want %d", body.Bytes, len(png))
	}

	// The checkout has no screenshots directory, and no file was added at all.
	if _, err := os.Stat(filepath.Join(checkout, "screenshots")); !os.IsNotExist(err) {
		t.Fatalf("capture wrote %s/screenshots (err=%v); it must stay outside the checkout", checkout, err)
	}
	entries, err := os.ReadDir(checkout)
	if err != nil {
		t.Fatalf("read checkout: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("checkout gained entries from a capture: %v", entries)
	}

	// The bytes are in the control-plane artifacts directory...
	rel := strings.TrimPrefix(body.Path, "artifacts/"+sess.ID+"/")
	onDisk := filepath.Join(env.deps.ArtifactsDir, sess.ID, filepath.FromSlash(rel))
	got, err := os.ReadFile(onDisk)
	if err != nil {
		t.Fatalf("artifact file %s: %v", onDisk, err)
	}
	if string(got) != string(png) {
		t.Fatalf("artifact bytes differ from the capture")
	}

	// ...and reachable through the route the preview uses.
	read := env.doAuthed(http.MethodGet,
		"/v1/sessions/"+sess.ID+"/artifacts?path="+rel, nil, c)
	if read.Code != http.StatusOK {
		t.Fatalf("artifact read = %d body %s", read.Code, read.Body.String())
	}
	var content contracts.FileContent
	env.mustJSON(read, &content)
	if content.Path != body.Path || !content.Binary || content.MimeType != "image/png" || content.Size != int64(len(png)) {
		t.Fatalf("artifact content = %+v", content)
	}

	inline := env.doAuthed(http.MethodGet,
		"/v1/sessions/"+sess.ID+"/artifacts?path="+rel+"&inline=1", nil, c)
	if inline.Code != http.StatusOK {
		t.Fatalf("inline artifact = %d body %s", inline.Code, inline.Body.String())
	}
	if inline.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("inline content-type = %q", inline.Header().Get("Content-Type"))
	}
	if inline.Body.String() != string(png) {
		t.Fatalf("inline body is not the captured bytes")
	}

	// Backward compatibility: a screenshot an older build wrote inside the
	// checkout is still readable through the workspace endpoint.
	if err := os.MkdirAll(filepath.Join(checkout, "screenshots"), 0o755); err != nil {
		t.Fatalf("mkdir legacy screenshots: %v", err)
	}
	legacy := filepath.Join(checkout, "screenshots", "20260101-000000-000.png")
	if err := os.WriteFile(legacy, png, 0o644); err != nil {
		t.Fatalf("write legacy screenshot: %v", err)
	}
	old := env.doAuthed(http.MethodGet,
		"/v1/sessions/"+sess.ID+"/files?path=screenshots/20260101-000000-000.png&inline=1", nil, c)
	if old.Code != http.StatusOK {
		t.Fatalf("legacy workspace screenshot = %d body %s", old.Code, old.Body.String())
	}
	if old.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("legacy content-type = %q", old.Header().Get("Content-Type"))
	}
	if _, err := io.ReadAll(old.Result().Body); err != nil {
		t.Fatalf("read legacy body: %v", err)
	}
}
