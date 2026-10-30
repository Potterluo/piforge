package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
)

// This file covers the settings surfaces the frontend already renders (skills,
// MCP servers) and the one provider route that has no implementation yet. Each
// route is registered even when it only answers 501, because an unregistered
// path comes back as an HTML 404 that the client cannot classify.

// skillsResponse is the skill list envelope.
type skillsResponse struct {
	Skills []*contracts.Skill `json:"skills"`
}

type skillResponse struct {
	Skill *contracts.Skill `json:"skill"`
}

// handleListSkills lists shared and private skills.
func (s *server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	skills, err := s.deps.Store.ListSkills(r.Context(), user.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if skills == nil {
		skills = []*contracts.Skill{}
	}
	writeJSON(w, http.StatusOK, skillsResponse{Skills: skills})
}

// handleCreateSkill registers a skill bundle the caller already uploaded; the
// content itself lives in the skill store, this row is the index entry.
//
// scope is accepted here because a shared skill has to come from somewhere: only
// the delete half of "admin 共享 skill 管理" existed, so an admin could remove a
// shared skill but never create one (FR-14.1 / FR-16.3). A shared skill is
// deployment-wide, so creating one is an admin act; a normal user asking for
// scope=admin is refused rather than silently downgraded to a private skill.
func (s *server) handleCreateSkill(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	// web/lib/api/client.ts posts {name, description, body} and expects the
	// index row back. content_hash is derived from body when the caller does
	// not supply one, so the content-addressed store stays the source of truth.
	var in struct {
		Scope       string `json:"scope"`
		Name        string `json:"name"`
		Version     string `json:"version"`
		Description string `json:"description"`
		Body        string `json:"body"`
		ContentHash string `json:"content_hash"`
		SourceURL   string `json:"source_url"`
		Enabled     *bool  `json:"enabled"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}

	scope := contracts.SkillScope(strings.TrimSpace(in.Scope))
	switch scope {
	case "":
		scope = contracts.SkillUser
	case contracts.SkillUser:
	case contracts.SkillAdmin:
		if user.Role != contracts.RoleAdmin {
			s.fail(w, r, apiErr(contracts.ErrForbidden, "共享 Skill 只能由管理员创建"))
			return
		}
	default:
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "scope must be user or admin"))
		return
	}

	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	contentHash := in.ContentHash
	if contentHash == "" {
		sum := sha256.Sum256([]byte(in.Body))
		contentHash = hex.EncodeToString(sum[:])
	}
	skill, err := s.deps.Store.CreateSkill(r.Context(), user.ID, contracts.Skill{
		Scope:       scope,
		Name:        in.Name,
		Version:     in.Version,
		Description: in.Description,
		ContentHash: contentHash,
		SourceURL:   in.SourceURL,
		Enabled:     enabled,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// A skill without content is metadata that never reaches the agent, so a
	// create that carries a body also writes the bundle. The row exists first
	// because its id names the directory; if the write fails the row is removed
	// rather than left looking configured while mounting nothing.
	if strings.TrimSpace(in.Body) != "" {
		files, ferr := bundleFiles([]skillFile{{Path: "SKILL.md", Content: in.Body}})
		if ferr != nil {
			_ = s.deps.Store.DeleteSkill(r.Context(), user.ID, skill.ID)
			s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "%s", ferr.Error()))
			return
		}
		key, hash, werr := s.writeSkillBundle(skill.ID, files)
		if werr != nil {
			_ = s.deps.Store.DeleteSkill(r.Context(), user.ID, skill.ID)
			s.fail(w, r, werr)
			return
		}
		if updated, uerr := s.deps.Store.UpdateSkillBundle(r.Context(), user.ID, skill.ID, key, hash); uerr == nil {
			skill = updated
		}
	}
	writeJSON(w, http.StatusCreated, skillResponse{Skill: skill})
}

// handlePatchSkill edits what can be edited without replacing the content: today
// that is only `enabled` (FR-14.4's 可禁用).
//
// Disabling is ownership-checked like deletion: a shared skill is a deployment
// asset, so a normal user may not switch it off for everybody. The UI already
// explains that rule on the delete button; this is the same rule on the switch.
func (s *server) handlePatchSkill(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	skill, err := s.deps.Store.Skill(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if skill.Scope == contracts.SkillAdmin && user.Role != contracts.RoleAdmin {
		s.fail(w, r, apiErr(contracts.ErrForbidden, "共享 Skill 由管理员维护"))
		return
	}

	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.Enabled == nil {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "enabled is required"))
		return
	}
	var updated *contracts.Skill
	if skill.Scope == contracts.SkillAdmin {
		// Any admin may manage a shared skill, not only the one who created it;
		// the role was checked above, so this is the same rule as delete.
		updated, err = s.deps.Store.SetSharedSkillEnabled(r.Context(), skill.ID, *in.Enabled)
	} else {
		updated, err = s.deps.Store.SetSkillEnabled(r.Context(), user.ID, skill.ID, *in.Enabled)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, skillResponse{Skill: updated})
}

// handleDeleteSkill removes one of the caller's skills.
func (s *server) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	skill, err := s.deps.Store.Skill(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// A shared skill is everybody's, so deleting one is an admin act. Refusing without
	// offering the admin path is what made the UI say 共享 Skill 由管理员维护 with no way
	// for an admin to do it either.
	if skill.Scope == contracts.SkillAdmin {
		if user.Role != contracts.RoleAdmin {
			s.fail(w, r, apiErr(contracts.ErrForbidden, "共享 Skill 由管理员维护"))
			return
		}
		if err := s.deps.Store.DeleteSharedSkill(r.Context(), skill.ID); err != nil {
			s.fail(w, r, err)
			return
		}
	} else if err := s.deps.Store.DeleteSkill(r.Context(), user.ID, skill.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	// The bundle on disk goes with it: leaving the directory behind meant a re-created
	// skill with the same name silently inherited the old content.
	if s.deps.SkillsDir != "" {
		_ = os.RemoveAll(filepath.Join(s.deps.SkillsDir, skill.ID))
	}
	writeOK(w)
}

// mcpServersResponse is the MCP list envelope.
type mcpServersResponse struct {
	Servers []*contracts.MCPServer `json:"servers"`
}

type mcpServerResponse struct {
	Server *contracts.MCPServer `json:"server"`
}

// handleListMCPServers lists shared and private MCP server definitions.
func (s *server) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	servers, err := s.deps.Store.ListMCPServers(r.Context(), user.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if servers == nil {
		servers = []*contracts.MCPServer{}
	}
	writeJSON(w, http.StatusOK, mcpServersResponse{Servers: servers})
}

// handleCreateMCPServer stores a user-defined MCP server. stdio is not
// restricted by design (PRD §5.9): the user already has a shell in the
// sandbox, so the transport is not a security boundary here.
func (s *server) handleCreateMCPServer(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	// The client sends Omit<MCPServer, "id"|"created_at"|"has_secrets"> plus an
	// optional `secrets` blob, so scope and secrets are accepted here. scope is
	// forced to the caller: only /v1/admin routes may write shared entries.
	var in struct {
		Scope     string   `json:"scope"`
		Name      string   `json:"name"`
		Transport string   `json:"transport"`
		Command   string   `json:"command"`
		Args      []string `json:"args"`
		Cwd       string   `json:"cwd"`
		URL       string   `json:"url"`
		Exposure  string   `json:"exposure"`
		Enabled   *bool    `json:"enabled"`
		// Secrets is write-only: encrypted at rest, never returned (FR-15.2).
		Secrets string `json:"secrets"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.Scope != "" && in.Scope != "user" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument,
			"only admin routes may create scope=%q entries", in.Scope))
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	server, err := s.deps.Store.CreateMCPServer(r.Context(), user.ID, contracts.MCPServer{
		Scope:     contracts.SkillUser,
		Name:      in.Name,
		Transport: contracts.MCPTransport(in.Transport),
		Command:   in.Command,
		Args:      in.Args,
		Cwd:       in.Cwd,
		URL:       in.URL,
		Exposure:  in.Exposure,
		Enabled:   enabled,
		// Encrypted before it touches the database; the store never keeps plaintext.
		Secrets: in.Secrets,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, mcpServerResponse{Server: server})
}

// handleDeleteMCPServer removes one of the caller's MCP servers.
func (s *server) handleDeleteMCPServer(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if err := s.deps.Store.DeleteMCPServer(r.Context(), user.ID, pathID(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	writeOK(w)
}
