package httpapi

import (
	"net/http"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/materialize"
)

// toolingResponse tells the UI what the session's sandbox actually has on disk.
//
// It reports two different things on purpose: what the user configured, and
// what survived the projection into the worktree. They can differ — a skill
// whose bundle was never uploaded is configured but not mounted — and hiding
// that difference is how "skills are enabled but nothing happens" becomes a
// mystery.
type toolingResponse struct {
	Skills     []*contracts.Skill     `json:"skills"`
	MCPServers []*contracts.MCPServer `json:"mcp_servers"`
	Mounted    materialize.Manifest   `json:"mounted"`
	// MountedAt is the worktree root the projection ran against; empty when the
	// session has no checkout yet.
	MountedAt string `json:"mounted_at"`
}

// handleSessionTooling reports the configured and mounted tooling for a session.
func (s *server) handleSessionTooling(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	skills, err := s.deps.Store.ListSkills(r.Context(), user.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	servers, err := s.deps.Store.ListMCPServers(r.Context(), user.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	resp := toolingResponse{Skills: skills, MCPServers: servers}
	if sess.WorktreeID == "" {
		// Nothing has been provisioned yet; the configuration is still valid.
		writeJSON(w, http.StatusOK, resp)
		return
	}
	root, err := s.worktreeRoot(r.Context(), user.ID, sess)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp.MountedAt = root
	resp.Mounted = materialize.Read(root)
	writeJSON(w, http.StatusOK, resp)
}
