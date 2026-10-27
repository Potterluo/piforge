package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"

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
func (s *server) handleCreateSkill(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	// web/lib/api/client.ts posts {name, description, body} and expects the
	// index row back. content_hash is derived from body when the caller does
	// not supply one, so the content-addressed store stays the source of truth.
	var in struct {
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
		Scope:       contracts.SkillUser,
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
	writeJSON(w, http.StatusCreated, skillResponse{Skill: skill})
}

// handleDeleteSkill removes one of the caller's skills.
func (s *server) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if err := s.deps.Store.DeleteSkill(r.Context(), user.ID, pathID(r)); err != nil {
		s.fail(w, r, err)
		return
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
		// Secrets is accepted but not persisted: the vault that must hold it
		// (AES-GCM, PRD FR-15.2) is not wired into this layer yet.
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

// handleTestProvider would send a one-token request to the provider to prove
// the endpoint and key work (PRD FR-13.7). It is registered but not
// implemented: doing it properly needs the secret layer to decrypt the key,
// and a fake "ok" would be worse than an honest 501.
func (s *server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	// Prove the provider is visible to the caller before answering, so the 501
	// does not leak which ids exist.
	if _, err := s.deps.Store.Provider(r.Context(), user.ID, pathID(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	s.notImplemented(w, r)
}
