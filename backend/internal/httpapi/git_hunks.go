package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Hunk-level and per-file operations (PRD FR-12.2).
//
// The client sends a path and an index, never a patch: a patch from a browser would let
// "stage this hunk" write arbitrary content into the index under a name that sounds
// harmless.

// handleGitHunk stages, unstages or discards one hunk.
func (s *server) handleGitHunk(w http.ResponseWriter, r *http.Request) {
	root, ok := s.rootOrFail(w, r)
	if !ok {
		return
	}
	var in struct {
		Path   string `json:"path"`
		Index  *int   `json:"index"`
		Action string `json:"action"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if strings.TrimSpace(in.Path) == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "path is required"))
		return
	}
	if in.Index == nil {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "index is required"))
		return
	}
	index := *in.Index
	if index < 0 {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "index must not be negative"))
		return
	}

	ctx, cancel := detach(r.Context(), 30*time.Second)
	defer cancel()

	var err error
	switch in.Action {
	case "stage":
		err = s.deps.Workspace.StageHunk(ctx, root, in.Path, index)
	case "unstage":
		err = s.deps.Workspace.UnstageHunk(ctx, root, in.Path, index)
	case "discard":
		err = s.deps.Workspace.DiscardHunk(ctx, root, in.Path, index)
	default:
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument,
			"action must be one of stage, unstage, discard"))
		return
	}
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "%v", err))
		return
	}
	writeOK(w)
}

// handleGitDiscardFile restores one file, or deletes it when it is new.
func (s *server) handleGitDiscardFile(w http.ResponseWriter, r *http.Request) {
	root, ok := s.rootOrFail(w, r)
	if !ok {
		return
	}
	var in struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if strings.TrimSpace(in.Path) == "" {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "path is required"))
		return
	}
	ctx, cancel := detach(r.Context(), 30*time.Second)
	defer cancel()

	if err := s.deps.Workspace.DiscardFile(ctx, root, in.Path); err != nil {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "%v", err))
		return
	}
	writeOK(w)
}
