package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/tenantfs"
)

// Limits on an uploaded skill bundle. A skill is instructions, not a project:
// the cap keeps a stray directory upload from becoming a disk-filling attack.
const (
	maxSkillFileBytes  = 1 << 20 // 1 MiB per file
	maxSkillTotalBytes = 4 << 20 // 4 MiB per bundle
	maxSkillFiles      = 64
	maxSkillPathLen    = 200
)

// DefaultSkillMaxBytes bounds the total a single import may expand on disk
// (FR-5.8). The per-bundle cap above is four megabytes; this is the ceiling for
// one archive's expansion across every skill it contains, and it is what stops an
// archive bomb instead of trusting the sizes a zip header claims.
// cmd/piforged fills Deps.SkillMaxBytes from -skill-max-bytes /
// PIFORGE_SKILL_MAX_BYTES; 32 MiB is the deployment default.
const DefaultSkillMaxBytes int64 = 32 << 20

// skillMaxBytes is the effective cap for this server.
func (s *server) skillMaxBytes() int64 {
	if s != nil && s.deps.SkillMaxBytes > 0 {
		return s.deps.SkillMaxBytes
	}
	return DefaultSkillMaxBytes
}

type skillFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// bundleFiles normalizes and validates an uploaded file set.
//
// Paths are user input and end up on disk inside the sandbox, so this is a
// security boundary, not a convenience: every path is cleaned against a virtual
// root, backslashes are normalized, absolute paths and parent traversal are
// refused, and the result must contain SKILL.md or Pi will never see the skill.
func bundleFiles(files []skillFile) (map[string]string, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("no files were provided")
	}
	if len(files) > maxSkillFiles {
		return nil, fmt.Errorf("a skill bundle may contain at most %d files", maxSkillFiles)
	}

	clean := make(map[string]string, len(files))
	total := 0
	hasSkillMD := false

	for _, file := range files {
		raw := strings.TrimSpace(strings.ReplaceAll(file.Path, "\\", "/"))
		if raw == "" {
			return nil, fmt.Errorf("a file has an empty path")
		}
		if len(raw) > maxSkillPathLen {
			return nil, fmt.Errorf("path %q is too long", raw)
		}
		if strings.HasPrefix(raw, "/") || strings.Contains(raw, "..") {
			return nil, fmt.Errorf("path %q is not allowed", raw)
		}
		// path.Clean on a rooted join makes traversal visible before it is used.
		rel := strings.TrimPrefix(path.Clean("/"+raw), "/")
		if rel == "" || rel == "." {
			return nil, fmt.Errorf("path %q is not allowed", raw)
		}
		if len(file.Content) > maxSkillFileBytes {
			return nil, fmt.Errorf("%s is larger than %d bytes", rel, maxSkillFileBytes)
		}
		total += len(file.Content)
		if total > maxSkillTotalBytes {
			return nil, fmt.Errorf("the bundle is larger than %d bytes", maxSkillTotalBytes)
		}
		if strings.EqualFold(rel, "SKILL.md") {
			hasSkillMD = true
		}
		clean[rel] = file.Content
	}

	if !hasSkillMD {
		return nil, fmt.Errorf("a skill bundle must contain SKILL.md")
	}
	return clean, nil
}

// writeSkillBundle materialises a validated file set under the skills root.
//
// The bundle is written to a temporary directory and then swapped in, so a
// failed upload cannot leave a half-written skill that Pi would load.
func (s *server) writeSkillBundle(skillID string, files map[string]string) (string, string, error) {
	if strings.TrimSpace(s.deps.SkillsDir) == "" {
		return "", "", apiErr(contracts.ErrInternal, "skill storage is not configured on this server")
	}
	if skillID == "" || strings.ContainsAny(skillID, `/\`) {
		return "", "", apiErr(contracts.ErrInvalidArgument, "invalid skill id")
	}

	root := filepath.Join(s.deps.SkillsDir, skillID)
	staging := root + ".tmp"

	if err := os.RemoveAll(staging); err != nil {
		return "", "", fmt.Errorf("skill bundle: clear staging: %w", err)
	}
	if err := os.RemoveAll(root); err != nil {
		return "", "", fmt.Errorf("skill bundle: clear previous: %w", err)
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	hasher := sha256.New()
	for _, rel := range names {
		target := filepath.Join(staging, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", "", fmt.Errorf("skill bundle: create %s: %w", path.Dir(rel), err)
		}
		content := files[rel]
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			return "", "", fmt.Errorf("skill bundle: write %s: %w", rel, err)
		}
		// Hash the sorted content so the same bundle always yields the same
		// hash, whatever order the client sent the files in.
		fmt.Fprintf(hasher, "%s\x00%s\x00", rel, content)
	}
	if err := os.Rename(staging, root); err != nil {
		return "", "", fmt.Errorf("skill bundle: publish: %w", err)
	}
	return skillID, hex.EncodeToString(hasher.Sum(nil)), nil
}

// handleUploadSkillBundle replaces a skill's content.
func (s *server) handleUploadSkillBundle(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	skill, err := s.deps.Store.Skill(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Inherited (admin) skills are read-only for a normal user.
	if skill.Scope != contracts.SkillUser {
		s.fail(w, r, apiErr(contracts.ErrForbidden, "only your own skills can be edited"))
		return
	}

	var in struct {
		Files []skillFile `json:"files"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	files, err := bundleFiles(in.Files)
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrInvalidArgument, "%s", err.Error()))
		return
	}

	key, hash, err := s.writeSkillBundle(skill.ID, files)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	updated, err := s.deps.Store.UpdateSkillBundle(r.Context(), user.ID, skill.ID, key, hash)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, skillResponse{Skill: updated})
}

// handleReadSkillBundle returns the bundle's files so the editor shows what is
// actually mounted rather than what was typed into a form once.
func (s *server) handleReadSkillBundle(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	skill, err := s.deps.Store.Skill(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if skill.BundleKey == "" {
		writeJSON(w, http.StatusOK, map[string]any{"files": []skillFile{}})
		return
	}
	if strings.TrimSpace(s.deps.SkillsDir) == "" {
		s.fail(w, r, apiErr(contracts.ErrInternal, "skill storage is not configured on this server"))
		return
	}

	root := filepath.Join(s.deps.SkillsDir, filepath.Base(skill.BundleKey))
	files := make([]skillFile, 0, 8)

	// Read back through the containment every tenant-derived tree is read
	// through (internal/tenantfs): the bundle directory is written by this
	// process, but it is served to the tenant and nothing here should be able to
	// follow a link out of it if one ever appeared (a restore, a hand-copied
	// directory, a future upload path). A missing bundle is an empty editor, not
	// an error.
	tree, terr := tenantfs.Open(root)
	if terr != nil {
		if errors.Is(terr, fs.ErrNotExist) {
			writeJSON(w, http.StatusOK, map[string]any{"files": []skillFile{}})
			return
		}
		s.fail(w, r, apiErr(contracts.ErrInternal, "%v", terr))
		return
	}
	walkErr := tree.WalkFiles("", func(rel string, info os.FileInfo) error {
		if info.Size() > maxSkillFileBytes {
			return &tenantfs.Refusal{Path: rel, Kind: tenantfs.KindTooLarge,
				Detail: fmt.Sprintf("more than %d bytes", maxSkillFileBytes)}
		}
		data, rerr := tree.ReadFile(rel, maxSkillFileBytes)
		if rerr != nil {
			return rerr
		}
		files = append(files, skillFile{Path: filepath.ToSlash(rel), Content: string(data)})
		return nil
	})
	if walkErr != nil {
		s.fail(w, r, walkErr)
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}
