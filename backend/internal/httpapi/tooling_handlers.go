package httpapi

import (
	"net/http"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/materialize"
	skillset "github.com/piforge/piforge/backend/internal/skills"
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

	resp := toolingResponse{Skills: skillset.Annotate(skills), MCPServers: servers}
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

// sessionSkillsResponse is what a session was actually mounted with (FR-14.5).
//
// It is a record, not a view over `skills`: a skill edited after this session
// started does not change what is reported here. `skills` is never null so the
// browser can render "none" without a null check.
type sessionSkillsResponse struct {
	Skills []*contracts.SessionSkill `json:"skills"`
	// Mechanisms is how each mounted skill was installed (FR-14.2), keyed by
	// directory slug. It comes from the worktree's mount manifest rather than
	// the record: the record says *what* was mounted, this says *how*, and a
	// Windows deployment is a read-only copy, not a bind mount.
	Mechanisms map[string]string `json:"mechanisms,omitempty"`
}

// handleSessionSkills lists the skill versions materialised for a session.
func (s *server) handleSessionSkills(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	list, err := s.deps.Store.SessionSkills(r.Context(), user.ID, sess.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if list == nil {
		list = []*contracts.SessionSkill{}
	}
	resp := sessionSkillsResponse{Skills: list}
	if sess.WorktreeID != "" {
		if root, rerr := s.worktreeRoot(r.Context(), user.ID, sess); rerr == nil {
			resp.Mechanisms = materialize.Read(root).Mechanisms
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
