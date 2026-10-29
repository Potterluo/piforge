package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/fsx"
)

// Control-plane artifacts.
//
// A browser screenshot used to be written to `screenshots/` at the top of the
// checkout. That made it visible to the agent, but also made the user's
// repository dirty on every capture: `git status` grew an untracked directory
// that showed up in the change list and could be committed by accident.
//
// Artifacts now live outside the checkout, one directory per session:
//
//	<ArtifactsDir>/<session-id>/screenshots/<timestamp>-<ms>.png
//
// The API still exposes a stable path to the panel. It is `artifacts/<session>/
// screenshots/<name>` — a virtual path, not a filesystem path — and the panel
// reads it through `/v1/sessions/{id}/artifacts?path=screenshots/<name>`.
// Workspace paths keep working, so a screenshot captured before this change
// (`screenshots/…` inside the checkout) is still previewable.
const artifactVirtualRoot = "artifacts"

// artifactsRoot is the deployment's artifacts directory. The temp fallback keeps
// hand-built dependencies (tests, `go run` without -data) working while still
// staying outside any checkout.
func (s *server) artifactsRoot() string {
	if strings.TrimSpace(s.deps.ArtifactsDir) != "" {
		return s.deps.ArtifactsDir
	}
	return filepath.Join(os.TempDir(), "piforge-artifacts")
}

// artifactSessionRoot is the directory a session owns. An id containing a
// separator would escape the root; session ids are generated, but this handler
// must not rely on that.
func (s *server) artifactSessionRoot(sessionID string) (string, bool) {
	if sessionID == "" || strings.ContainsAny(sessionID, `/\`) || sessionID == "." || sessionID == ".." {
		return "", false
	}
	return filepath.Join(s.artifactsRoot(), sessionID), true
}

// artifactVirtualPath is what the client stores and shows: artifacts/<sid>/<rel>.
func artifactVirtualPath(sessionID, rel string) string {
	return path.Join(artifactVirtualRoot, sessionID, filepath.ToSlash(rel))
}

// saveScreenshot writes one PNG into the session's artifacts directory and
// returns its virtual path. Nothing is written under the checkout.
func (s *server) saveScreenshot(ctx context.Context, sessionID string, png []byte, now time.Time) (string, error) {
	root, ok := s.artifactSessionRoot(sessionID)
	if !ok {
		return "", apiErr(contracts.ErrInvalidArgument, "invalid session id for artifacts")
	}
	rel := path.Join("screenshots", fmt.Sprintf("%s-%03d.png",
		now.Format("20060102-150405"), now.Nanosecond()/int(time.Millisecond)))
	if err := s.artifacts.WriteFile(ctx, root, rel, png); err != nil {
		return "", err
	}
	return artifactVirtualPath(sessionID, rel), nil
}

// handleArtifactFile is the read/serve half of the artifacts directory. It is
// deliberately separate from /files: those paths are interpreted by the
// session's Workspace (which may be a remote sandbox), while these bytes are
// owned by the control plane.
func (s *server) handleArtifactFile(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if rel == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "path is required"))
		return
	}
	root, ok := s.artifactSessionRoot(sess.ID)
	if !ok {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "invalid session id for artifacts"))
		return
	}

	if queryBool(r, "download") || queryBool(r, "inline") {
		s.streamArtifact(w, r, root, rel, sess.ID, queryBool(r, "inline"))
		return
	}
	content, err := s.artifacts.ReadFile(r.Context(), root, rel)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The panel displays this path and the clickable failure links compare
	// against workspace paths, so the virtual one is what the client sees.
	content.Path = artifactVirtualPath(sess.ID, rel)
	writeJSON(w, http.StatusOK, content)
}

func (s *server) streamArtifact(w http.ResponseWriter, r *http.Request, root, rel, sessionID string, inline bool) {
	body, size, err := s.artifacts.OpenFile(r.Context(), root, rel)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", fsx.GuessMime(rel, true))
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	if !inline {
		w.Header().Set("Content-Disposition", `attachment; filename="`+dispositionName(rel)+`"`)
	}
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, body); err != nil {
		s.log.Warn("artifact stream interrupted", "path", rel, "session", sessionID, "error", err)
	}
}
