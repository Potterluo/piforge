// Package materialize projects the platform's tooling configuration into a
// session's working directory.
//
// Pi discovers skills by reading `SKILL.md` from a directory, and MCP servers
// from a JSON config file. Both are *filesystem* facts, so a user's stored
// settings only take effect once they exist on disk inside the worktree — this
// package is that projection step.
//
// It is deliberately small and pure: given a root and a list of things to
// mount, it writes files and reports what it did. No database, no network, so
// it is cheap to test and impossible to get subtly wrong at a distance.
package materialize

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Layout constants. Pi's convention is one directory per skill containing a
// SKILL.md, and a single JSON file listing MCP servers.
const (
	SkillsRoot  = ".pi/skills"
	MCPConfig   = ".mcp.json"
	manifestRel = ".pi/mounted.json"
	mcpKey      = "mcpServers"
)

// Bundle is a skill's content on disk.
type Bundle struct {
	// Name is the skill's display name; it also names the directory.
	Name string
	// Dir is the directory holding SKILL.md and any assets.
	Dir string
}

// Manifest is the record of what this package last wrote, so a later mount can
// remove things that are no longer configured instead of leaving stale skills
// in the agent's context forever.
//
// The slices are never nil: the API returns them straight to the browser, and a
// null array there means every consumer needs a null check for a state that
// simply means "none".
type Manifest struct {
	Skills     []string `json:"skills"`
	MCPServers []string `json:"mcp_servers"`
	MCPWritten bool     `json:"mcp_written"`
}

func (m Manifest) normalized() Manifest {
	if m.Skills == nil {
		m.Skills = []string{}
	}
	if m.MCPServers == nil {
		m.MCPServers = []string{}
	}
	return m
}

// Result reports the outcome of a mount.
type Result struct {
	// Skills are the skill directories now present.
	Skills []string
	// Skipped lists skills whose content was missing, with the reason.
	Skipped map[string]string
	// MCPServers are the server names written to the config.
	MCPServers []string
	// MCPPath is the config file's path, empty when nothing was written.
	MCPPath string
}

// Options tunes the mount.
type Options struct {
	// SkillsDir is where skill bundles live on the host. A skill whose
	// bundle_key does not resolve to an existing directory is skipped rather
	// than mounted as an empty stub: an instruction-free skill is worse than a
	// missing one, because the agent will try to use it.
	SkillsDir string
}

// Mount writes the project's tooling into root.
//
// The operation is idempotent: mounting the same configuration twice leaves the
// same files, and mounting a smaller configuration removes what it previously
// managed. Files this package did not write are never deleted.
func Mount(root string, skills []*contracts.Skill, servers []*contracts.MCPServer, opts Options) (Result, error) {
	if strings.TrimSpace(root) == "" {
		return Result{}, errors.New("materialize: root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Result{}, fmt.Errorf("materialize: create root: %w", err)
	}

	result := Result{Skills: []string{}, MCPServers: []string{}, Skipped: map[string]string{}}
	previous := readManifest(root)

	// --- skills ---------------------------------------------------------
	wanted := map[string]string{} // dir name -> source dir
	for _, skill := range skills {
		if skill == nil || !skill.Enabled {
			continue
		}
		slug, err := slugify(skill.Name)
		if err != nil {
			result.Skipped[skill.Name] = err.Error()
			continue
		}
		source := ""
		if opts.SkillsDir != "" && skill.BundleKey != "" {
			candidate := filepath.Join(opts.SkillsDir, skill.BundleKey)
			if inside, cerr := safeJoin(opts.SkillsDir, skill.BundleKey); cerr == nil {
				candidate = inside
			}
			if info, serr := os.Stat(candidate); serr == nil && info.IsDir() {
				source = candidate
			}
		}
		if source == "" {
			result.Skipped[skill.Name] = "no content bundle on disk"
			continue
		}
		wanted[slug] = source
	}

	skillsRoot, err := safeJoin(root, SkillsRoot)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(skillsRoot, 0o755); err != nil {
		return Result{}, fmt.Errorf("materialize: create skills root: %w", err)
	}

	names := make([]string, 0, len(wanted))
	for slug := range wanted {
		names = append(names, slug)
	}
	sort.Strings(names)

	for _, slug := range names {
		source := wanted[slug]
		target := filepath.Join(skillsRoot, slug)
		// Rewrite wholesale so an updated bundle never leaves stale files.
		if err := os.RemoveAll(target); err != nil {
			return Result{}, fmt.Errorf("materialize: clear skill %s: %w", slug, err)
		}
		if err := copyTree(source, target); err != nil {
			return Result{}, fmt.Errorf("materialize: mount skill %s: %w", slug, err)
		}
		if _, err := os.Stat(filepath.Join(target, "SKILL.md")); err != nil {
			// A directory without SKILL.md is invisible to Pi, and silently
			// mounting it would look like the feature works when it does not.
			_ = os.RemoveAll(target)
			result.Skipped[slug] = "bundle has no SKILL.md"
			continue
		}
		result.Skills = append(result.Skills, slug)
	}

	// Remove skills we mounted last time that are no longer configured.
	for _, stale := range previous.Skills {
		if _, keep := wanted[stale]; keep {
			continue
		}
		if err := os.RemoveAll(filepath.Join(skillsRoot, stale)); err != nil {
			return Result{}, fmt.Errorf("materialize: remove stale skill %s: %w", stale, err)
		}
	}

	// --- MCP ------------------------------------------------------------
	//
	// MCP configuration is written by `pimodel.WriteMCP` into the session's agent
	// directory, which is where Pi actually looks (`$PI_CODING_AGENT_DIR/mcp.json`).
	// This function used to write `<worktree>/.mcp.json`, a filename Pi does not read:
	// the mount looked correct in the file tree and the agent never had the tools.
	//
	// It is deliberately not duplicated here. Two files would mean two sources of
	// truth, and the project-local one wins in Pi — silently dropping the credentials
	// the agent directory carries.
	servers2 := map[string]any{}
	for _, server := range servers {
		if server == nil || !server.Enabled {
			continue
		}
		result.MCPServers = append(result.MCPServers, server.Name)
	}
	sort.Strings(result.MCPServers)

	// Remove the file older builds wrote. It is ours by construction — the path is a
	// constant of this package — and leaving it would keep a config Pi never reads.
	configPath := filepath.Join(root, MCPConfig)
	if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
		return Result{}, fmt.Errorf("materialize: remove legacy mcp config: %w", err)
	}

	// Mounted tooling must not show up as the user's changes. Writing to
	// .git/info/exclude instead of .gitignore keeps the repository's tracked
	// files untouched (we have no business editing the user's commit contents)
	// while `git status` stays an honest description of *their* work.
	if err := excludeFromGit(root, []string{"/" + SkillsRoot, "/" + manifestRel, "/" + MCPConfig}); err != nil {
		return Result{}, err
	}

	next := Manifest{Skills: result.Skills, MCPServers: result.MCPServers, MCPWritten: len(servers2) > 0}
	if err := writeManifest(root, next); err != nil {
		return Result{}, err
	}
	return result, nil
}

// mcpEntry renders one server in the shape the config file expects.
func mcpEntry(server *contracts.MCPServer) (map[string]any, error) {
	switch server.Transport {
	case contracts.MCPStdio:
		if strings.TrimSpace(server.Command) == "" {
			return nil, errors.New("stdio server has no command")
		}
		entry := map[string]any{"command": server.Command}
		if len(server.Args) > 0 {
			entry["args"] = server.Args
		}
		if server.Cwd != "" {
			entry["cwd"] = server.Cwd
		}
		if server.Exposure != "" {
			entry["exposure"] = server.Exposure
		}
		return entry, nil
	case contracts.MCPHTTP:
		if strings.TrimSpace(server.URL) == "" {
			return nil, errors.New("http server has no url")
		}
		entry := map[string]any{"type": "http", "url": server.URL}
		if server.Exposure != "" {
			entry["exposure"] = server.Exposure
		}
		return entry, nil
	default:
		return nil, fmt.Errorf("unsupported transport %q", server.Transport)
	}
}

// slugify turns a skill name into a directory name that cannot escape the
// skills root. Names are user input, so this is a security boundary.
func slugify(name string) (string, error) {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == '.' || r == ' ' || r == '/':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "", fmt.Errorf("skill name %q has no usable characters", name)
	}
	if slug == "." || slug == ".." {
		return "", fmt.Errorf("skill name %q is not a usable directory name", name)
	}
	if len(slug) > 64 {
		slug = slug[:64]
	}
	return slug, nil
}

// safeJoin joins base and rel, refusing anything that escapes base.
func safeJoin(base, rel string) (string, error) {
	if base == "" {
		return "", errors.New("materialize: base is required")
	}
	clean := path.Clean("/" + strings.ReplaceAll(rel, "\\", "/"))
	joined := filepath.Join(base, filepath.FromSlash(clean))
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("materialize: resolve base: %w", err)
	}
	absJoined, err := filepath.Abs(joined)
	if err != nil {
		return "", fmt.Errorf("materialize: resolve target: %w", err)
	}
	if absJoined != absBase && !strings.HasPrefix(absJoined, absBase+string(os.PathSeparator)) {
		return "", fmt.Errorf("materialize: %q escapes %q", rel, base)
	}
	return absJoined, nil
}

// copyTree copies a directory recursively, skipping nothing: a skill bundle is
// small and complete or it is wrong.
func copyTree(source, target string) error {
	return filepath.WalkDir(source, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(source, p)
		if rerr != nil {
			return rerr
		}
		dest := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		if d.Type()&os.ModeSymlink != 0 {
			// A symlink in a bundle could point anywhere on the host; copying
			// the link would carry that into the worktree.
			return fmt.Errorf("skill bundle contains a symlink: %s", rel)
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(dest, data, 0o644)
	})
}

// Read reports what is currently mounted in root, from the manifest this
// package writes. A missing manifest is not an error: it means nothing was
// mounted yet.
func Read(root string) Manifest { return readManifest(root).normalized() }

func readManifest(root string) Manifest {
	var m Manifest
	data, err := os.ReadFile(filepath.Join(root, manifestRel))
	if err != nil {
		return Manifest{}
	}
	if json.Unmarshal(data, &m) != nil {
		return Manifest{}
	}
	return m
}

func writeManifest(root string, m Manifest) error {
	path := filepath.Join(root, manifestRel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("materialize: create manifest dir: %w", err)
	}
	payload, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("materialize: encode manifest: %w", err)
	}
	if err := os.WriteFile(path, append(payload, '\n'), 0o644); err != nil {
		return fmt.Errorf("materialize: write manifest: %w", err)
	}
	return nil
}

// excludeFromGit appends entries to the repository's info/exclude, which is the
// per-checkout ignore file that is never committed.
func excludeFromGit(root string, entries []string) error {
	gitDir, err := resolveGitDir(root)
	if err != nil || gitDir == "" {
		// Not a repository (yet): nothing to exclude from, and that is fine.
		return nil
	}
	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return fmt.Errorf("materialize: create git info dir: %w", err)
	}
	path := filepath.Join(infoDir, "exclude")

	existing := ""
	if data, rerr := os.ReadFile(path); rerr == nil {
		existing = string(data)
	}
	present := map[string]bool{}
	for _, line := range strings.Split(existing, "\n") {
		present[strings.TrimSpace(line)] = true
	}

	var add []string
	for _, entry := range entries {
		if !present[entry] {
			add = append(add, entry)
		}
	}
	if len(add) == 0 {
		return nil
	}

	if existing != "" && !strings.HasSuffix(existing, "\n") {
		existing += "\n"
	}
	if existing == "" {
		existing = "# Written by PiForge: platform-mounted tooling is not your change.\n"
	}
	existing += strings.Join(add, "\n") + "\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		return fmt.Errorf("materialize: write git exclude: %w", err)
	}
	return nil
}

// resolveGitDir finds the real git directory for root, following a worktree's
// `.git` file. It returns "" when root is not inside a repository.
func resolveGitDir(root string) (string, error) {
	dotGit := filepath.Join(root, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return "", nil
	}
	if info.IsDir() {
		return dotGit, nil
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return "", fmt.Errorf("materialize: read .git: %w", err)
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return "", nil
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	return filepath.Clean(target), nil
}
