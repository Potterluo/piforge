// Package materialize projects the platform's tooling configuration into a
// session's working directory.
//
// Pi discovers skills by reading `SKILL.md` from a directory, and MCP servers
// from a JSON config file. Both are *filesystem* facts, so a user's stored
// settings only take effect once they exist on disk inside the worktree — this
// package is that projection step.
//
// It is deliberately small and pure: given a filesystem, a root and a list of
// things to mount, it writes files and reports what it did. No database, no
// network, so it is cheap to test and impossible to get subtly wrong at a
// distance.
//
// Two filesystems. The projection writes into the control plane's own disk when
// the sandbox's workspace is a directory this process can open, and into the
// sandbox's file API when it is not (stage 2 of docs/EXECUTION-BOUNDARY.md;
// see fs.go). The decision of *what* to project is the same code either way —
// that is the point of splitting it this way.
package materialize

import (
	"crypto/sha256"
	"encoding/hex"
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
	SkillsRoot = ".pi/skills"
	MCPConfig  = ".mcp.json"
	// ManifestRel is the record of what was projected, relative to a checkout
	// root. It is exported because the tooling panel reads it back through the
	// workspace abstraction instead of assuming the file is on this disk.
	ManifestRel = ".pi/mounted.json"
	manifestRel = ManifestRel
	mcpKey      = "mcpServers"
	// snapshotRel is the fallback location for frozen bind-mount sources when
	// Options.SnapshotDir is empty. A deployment passes a directory outside the
	// checkout so the snapshot has no second, writable path.
	snapshotRel = ".pi/skill-snapshots"
)

// Bundle is a skill's content on disk.
type Bundle struct {
	// Name is the skill's display name; it also names the directory.
	Name string
	// Dir is the directory holding SKILL.md and any assets.
	Dir string
}

// wantedSkill is one skill selected for mounting: the name the user gave it and
// the directory its content comes from.
type wantedSkill struct {
	name   string
	source string
	// id is the skill row this bundle came from. It is how a later pass tells
	// "the same skill was edited" (keep the frozen mount) from "a different row
	// now wins this name" (re-install, e.g. the shared fallback after a private
	// skill is disabled, FR-14.1).
	id string
	// scope is carried so a same-name collision between a private and a shared
	// row is resolved by the two-scope rule instead of by slice order. Callers
	// are expected to pass an already-resolved list (provision.mountTooling uses
	// internal/skills), but the mount point is `.pi/skills/<slug>` — one
	// directory per name — so this is the last place the collision could become
	// silent.
	scope contracts.SkillScope
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
	// Mechanisms records how each mounted skill was installed: "bind-ro" or
	// "copy-ro" (FR-14.2). It is part of the manifest because "is it a real
	// mount?" is a question an operator asks, and the answer differs per
	// platform and even per fallback on one host.
	Mechanisms map[string]string `json:"mechanisms,omitempty"`
	// SkillIDs records which skill row produced each mount. A later pass uses it
	// to distinguish an edited bundle (same row: keep the frozen copy) from a
	// different row winning the same name (re-install).
	SkillIDs map[string]string `json:"skill_ids,omitempty"`
}

func (m Manifest) normalized() Manifest {
	if m.Skills == nil {
		m.Skills = []string{}
	}
	if m.MCPServers == nil {
		m.MCPServers = []string{}
	}
	if m.Mechanisms == nil {
		m.Mechanisms = map[string]string{}
	}
	if m.SkillIDs == nil {
		m.SkillIDs = map[string]string{}
	}
	return m
}

// MountedSkill is one skill directory this package wrote, with enough identity
// to record which version the session got (FR-14.5). The directory is resolved,
// not the path the caller passed: recording a relative path would make the
// session's own report depend on the process's working directory.
type MountedSkill struct {
	// Name is the skill's name as configured, not the directory slug.
	Name string
	// Dir is the absolute directory inside the worktree that holds SKILL.md.
	Dir string
	// Slug is the directory name Dir ends with. It is carried instead of being
	// re-derived from Dir, because a container path and a Windows host path do
	// not split the same way.
	Slug string
	// Mechanism is how that directory came to exist: "bind-ro" or "copy-ro".
	Mechanism string
}

// Result reports the outcome of a mount.
type Result struct {
	// Skills are the skill directories now present.
	Skills []string
	// Mounted carries the same skills with their names and absolute paths, so
	// the caller can fingerprint and record them without re-deriving slugs.
	Mounted []MountedSkill
	// Skipped lists skills whose content was missing, with the reason.
	Skipped map[string]string
	// Warnings are non-fatal degradations, such as a bind mount the kernel
	// refused and the copy that replaced it. Provisioning continues; the
	// mechanism is still reported honestly.
	Warnings []string
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
	//
	// Bundles are always on the control plane's disk, even when the tree they
	// are projected into is not: they are uploaded here and read here.
	SkillsDir string
	// SnapshotDir is where a bind mount's frozen copy lives. It is outside the
	// worktree in a deployment, so the mounted tree cannot be reached through a
	// second writable path. Empty falls back to <root>/.pi/skill-snapshots.
	SnapshotDir string
	// Platform overrides runtime.GOOS for tests of the mechanism decision.
	Platform string
	// Capabilities overrides the runtime capability probe. Nil probes this
	// process, which is what a deployment gets: a container started without
	// SYS_ADMIN reports copy-ro, one started with it reports bind-ro. Tests set
	// it to exercise both without privileges.
	Capabilities *Capabilities
	// BindMount overrides the kernel bind mount. Nil uses the real one on
	// Linux; tests use it to exercise the bind path without privileges.
	BindMount func(source, target string) error
	// Unmount overrides the kernel unmount. Nil uses the real one.
	Unmount func(target string) error
}

// Mount writes the project's tooling into root on the control plane's own disk.
//
// The operation is idempotent: mounting the same configuration twice leaves the
// same files, and mounting a smaller configuration removes what it previously
// managed. Files this package did not write are never deleted.
func Mount(root string, skills []*contracts.Skill, servers []*contracts.MCPServer, opts Options) (Result, error) {
	return MountFS(hostFS{opts: opts}, root, skills, servers, opts)
}

// MountFS writes the project's tooling into root through fs.
//
// fs is the control plane's disk for a workspace it can open, and the sandbox's
// file API for a workspace that exists only inside the container. The rules are
// identical — this is the same function — and only the mechanism a skill can be
// given differs: a bind mount needs somewhere to mount, which a file API does
// not have.
func MountFS(fs FileSystem, root string, skills []*contracts.Skill, servers []*contracts.MCPServer, opts Options) (Result, error) {
	if fs == nil {
		return Result{}, errors.New("materialize: a filesystem is required")
	}
	if strings.TrimSpace(root) == "" {
		return Result{}, errors.New("materialize: root is required")
	}
	if err := fs.MkdirAll(root); err != nil {
		return Result{}, fmt.Errorf("materialize: create root: %w", err)
	}

	result := Result{Skills: []string{}, Mounted: []MountedSkill{}, MCPServers: []string{}, Skipped: map[string]string{}, Warnings: []string{}}
	previous := readManifestFS(fs, root)

	// --- skills ---------------------------------------------------------
	wanted := map[string]wantedSkill{} // dir name -> name and source dir
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
			if inside, cerr := hostSafeJoin(opts.SkillsDir, skill.BundleKey); cerr == nil {
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
		// One directory per name. If two rows still reach here (a caller that did
		// not resolve scopes), the private row wins regardless of order, matching
		// the rule documented in internal/skills; equal scopes keep the first.
		if existing, ok := wanted[slug]; ok {
			if !(skill.Scope == contracts.SkillUser && existing.scope != contracts.SkillUser) {
				continue
			}
		}
		wanted[slug] = wantedSkill{name: skill.Name, source: source, scope: skill.Scope, id: skill.ID}
	}

	skillsRoot, err := safeJoin(fs, root, SkillsRoot)
	if err != nil {
		return Result{}, err
	}
	if err := fs.MkdirAll(skillsRoot); err != nil {
		return Result{}, fmt.Errorf("materialize: create skills root: %w", err)
	}

	names := make([]string, 0, len(wanted))
	for slug := range wanted {
		names = append(names, slug)
	}
	sort.Strings(names)

	// The platform decision is made once per pass; a per-skill kernel refusal
	// still degrades that skill to copy-ro and says so. The mount is never
	// pointed at the live bundle: a running session keeps what it was given.
	plan := platformPlan(opts)
	snapshots := snapshotRoot(root, opts)

	for _, slug := range names {
		entry := wanted[slug]
		target := fs.Join(skillsRoot, slug)

		// A target that already carries SKILL.md from the same skill row is this
		// worktree's frozen copy from an earlier pass. Keep it: re-copying would
		// make an edit reach a session that already started, which is exactly
		// what FR-14.2/14.5 say must not happen. A *different* row winning the
		// same name (a shared skill after its private shadow was disabled) is a
		// configuration change, not an edit, and is re-installed.
		if isMountedSkillFS(fs, target) && previous.SkillIDs[slug] == entry.id {
			mechanism := previous.Mechanisms[slug]
			if mechanism == "" {
				// A copy written by an older build (before mechanisms were
				// recorded) is a plain directory; hardening it in place is safe
				// and makes the read-only promise hold for it too.
				mechanism = string(MechanismCopyRO)
				if _, err := fs.MakeReadOnly(target); err != nil {
					result.Warnings = append(result.Warnings,
						fmt.Sprintf("skill %s: could not make the existing copy read-only: %v", slug, err))
				}
			}
			absTarget, err := fs.Abs(target)
			if err != nil {
				return Result{}, fmt.Errorf("materialize: resolve mounted skill %s: %w", slug, err)
			}
			result.Skills = append(result.Skills, slug)
			result.Mounted = append(result.Mounted, MountedSkill{Name: entry.name, Dir: absTarget, Slug: slug, Mechanism: mechanism})
			continue
		}

		// A stale mount or a stale read-only copy must not break provisioning:
		// unmount best-effort, clear read-only bits, retry once. Only a target
		// that still will not move is skipped, and the session keeps working.
		if err := fs.RemoveAll(target); err != nil {
			result.Skipped[slug] = "a stale mount could not be removed: " + err.Error()
			continue
		}

		used, warning, err := installSkillFS(fs, root, snapshots, target, entry.source, slug, plan, opts)
		if err != nil {
			// A bundle that cannot be installed is not a transient condition:
			// a symlink inside it, a full disk, or a denied write would keep
			// failing and the agent would silently have no skill. Fail the
			// provisioning pass instead of reporting a mount that is not there.
			_ = fs.RemoveAll(target)
			return Result{}, fmt.Errorf("materialize: mount skill %s: %w", slug, err)
		}
		if warning != "" {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skill %s: %s", slug, warning))
		}
		if kind, lerr := fs.Lookup(fs.Join(target, "SKILL.md")); lerr != nil || kind != KindFile {
			// A directory without SKILL.md is invisible to Pi, and silently
			// mounting it would look like the feature works when it does not.
			_ = fs.RemoveAll(target)
			result.Skipped[slug] = "bundle has no SKILL.md"
			continue
		}
		absTarget, err := fs.Abs(target)
		if err != nil {
			return Result{}, fmt.Errorf("materialize: resolve mounted skill %s: %w", slug, err)
		}
		result.Skills = append(result.Skills, slug)
		result.Mounted = append(result.Mounted, MountedSkill{Name: entry.name, Dir: absTarget, Slug: slug, Mechanism: used})
	}

	// Remove skills we mounted last time that are no longer configured.
	for _, stale := range previous.Skills {
		if _, keep := wanted[stale]; keep {
			continue
		}
		if err := fs.RemoveAll(fs.Join(skillsRoot, stale)); err != nil {
			// A directory we cannot remove must not take the whole session down;
			// say so and keep the rest of the projection intact.
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("stale skill %s could not be removed: %v", stale, err))
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
	for _, server := range servers {
		if server == nil || !server.Enabled {
			continue
		}
		result.MCPServers = append(result.MCPServers, server.Name)
	}
	sort.Strings(result.MCPServers)

	// Remove the file older builds wrote. It is ours by construction — the path is a
	// constant of this package — and leaving it would keep a config Pi never reads.
	configPath := fs.Join(root, MCPConfig)
	if err := removeIfPresent(fs, configPath); err != nil {
		return Result{}, fmt.Errorf("materialize: remove legacy mcp config: %w", err)
	}

	// Mounted tooling must not show up as the user's changes. Writing to
	// .git/info/exclude instead of .gitignore keeps the repository's tracked
	// files untouched (we have no business editing the user's commit contents)
	// while `git status` stays an honest description of *their* work.
	if err := excludeFromGitFS(fs, root, []string{"/" + SkillsRoot, "/" + ManifestRel, "/" + MCPConfig, "/" + snapshotRel}); err != nil {
		return Result{}, err
	}

	// `MCPServers` non-empty means the agent will get a config: `pimodel.WriteMCP` writes
	// it at spawn. The field kept its name for the wire format, not for the file it used
	// to describe.
	mechanisms := make(map[string]string, len(result.Mounted))
	skillIDs := make(map[string]string, len(result.Mounted))
	for _, mounted := range result.Mounted {
		mechanisms[mounted.Slug] = mounted.Mechanism
		if entry, ok := wanted[mounted.Slug]; ok {
			skillIDs[mounted.Slug] = entry.id
		}
	}
	next := Manifest{
		Skills:     result.Skills,
		MCPServers: result.MCPServers,
		MCPWritten: len(result.MCPServers) > 0,
		Mechanisms: mechanisms,
		SkillIDs:   skillIDs,
	}
	if err := writeManifestFS(fs, root, next); err != nil {
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

// hostSafeJoin joins base and rel on the control plane's disk, refusing anything
// that escapes base. It is used for bundle sources, which are always here.
func hostSafeJoin(base, rel string) (string, error) {
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

// snapshotRoot is where frozen bind-mount sources live. A deployment points it
// outside the checkout; the fallback stays inside .pi, which is git-excluded.
func snapshotRoot(root string, opts Options) string {
	if strings.TrimSpace(opts.SnapshotDir) != "" {
		return opts.SnapshotDir
	}
	return filepath.Join(root, filepath.FromSlash(snapshotRel))
}

// isMountedSkillFS reports whether a directory already holds a mounted skill.
//
// It is the whole freeze rule: once a worktree has a skill, later passes leave
// it alone. Editing the bundle therefore reaches only sessions provisioned
// after the edit, while the running session keeps the content it was given.
func isMountedSkillFS(fs FileSystem, target string) bool {
	if kind, err := fs.Lookup(target); err != nil || kind != KindDir {
		return false
	}
	kind, err := fs.Lookup(fs.Join(target, "SKILL.md"))
	return err == nil && kind == KindFile
}

// platformPlan resolves the decision for this pass: the capability override when
// a test (or an operator) provides one, otherwise the process's own set.
func platformPlan(opts Options) Plan {
	caps := ProbeCapabilities()
	if opts.Capabilities != nil {
		caps = *opts.Capabilities
	}
	return PlatformPlan(opts.Platform, caps)
}

// installSkillFS creates the mounted tree for one skill and reports the
// mechanism that actually succeeded.
//
// bind-ro is tried first when the filesystem may bind: a snapshot is taken
// first, then bind-mounted read-only, so the mount is both frozen and cheap.
// Everywhere else — another platform, Linux without CAP_SYS_ADMIN, or a
// sandbox whose files are only reachable through its API — the tree is a
// read-only copy, which needs no capability. The copy's read-only promise is
// probed rather than assumed (copy-ro when the bits hold against the account
// that reads the tree, copy when they do not); the reverse never happens: a
// placement without bind mounts does not pretend.
func installSkillFS(fs FileSystem, root, snapshots, target, source, slug string, plan Plan, opts Options) (string, string, error) {
	warning := ""
	if binder, ok := fs.(Binder); ok && plan.CanBind && fs.CanBind() {
		snapshot := filepath.Join(snapshots, slug)
		if err := ensureSnapshot(snapshot, source, opts); err != nil {
			warning = fmt.Sprintf("could not take a mount snapshot (%v); copying instead", err)
		} else if err := binder.BindReadOnly(snapshot, target); err == nil {
			return string(MechanismBindRO), "", nil
		} else {
			// The capability was there but the kernel still said no (a
			// seccomp profile, an AppArmor rule, a filesystem that refuses
			// mounts). The copy below is still read-only where this process
			// honours the bits, and the warning names what happened.
			warning = fmt.Sprintf("bind mount refused (%v); using a read-only copy", err)
		}
	}
	if err := copyTreeFS(fs, source, target); err != nil {
		return "", warning, fmt.Errorf("mount skill: %w", err)
	}
	// Permission bits are not a guarantee: a privileged account ignores them.
	// Probe by opening the mounted SKILL.md for writing (without truncating) —
	// on the control plane's disk, or, for a sandbox tree, by asking the
	// sandbox's own shell as the account the agent runs as. A process that still
	// carries CAP_DAC_OVERRIDE would otherwise report a guarantee it does not
	// have.
	writable, err := fs.MakeReadOnly(target)
	if err != nil {
		return "", warning, fmt.Errorf("mount skill: make read-only: %w", err)
	}
	if writable {
		return string(MechanismCopy), "the mounted copy is writable by the account that reads it (root without CAP_SYS_ADMIN and with CAP_DAC_OVERRIDE); the tree is frozen but not read-only", nil
	}
	return string(MechanismCopyRO), warning, nil
}

// mountedFileIsWritable reports whether this process can open the mounted
// SKILL.md for writing. It never writes: opening alone is what permission bits
// and Windows' read-only attribute refuse.
func mountedFileIsWritable(target string) bool {
	file, err := os.OpenFile(filepath.Join(target, "SKILL.md"), os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	_ = file.Close()
	return true
}

// ensureSnapshot copies the bundle once and reuses it afterwards. Reuse is what
// freezes the mounted tree: the snapshot belongs to this session and is never
// refreshed from a bundle that changed under a running session.
func ensureSnapshot(snapshot, source string, opts Options) error {
	if hostIsMountedSkill(snapshot) {
		return nil
	}
	if err := clearMountedTree(snapshot, opts); err != nil {
		return err
	}
	return copyTree(source, snapshot)
}

// hostIsMountedSkill is isMountedSkillFS for the control plane's own disk, used
// by the snapshot path, which is host-only by construction (a bind mount needs a
// source on the host).
func hostIsMountedSkill(target string) bool {
	info, err := os.Lstat(target)
	if err != nil || !info.IsDir() {
		return false
	}
	_, err = os.Stat(filepath.Join(target, "SKILL.md"))
	return err == nil
}

// copyReadOnly copies a bundle and marks the result read-only.
//
// On Unix that is file mode 0444/0555. On Windows Go maps the write bits to
// FILE_ATTRIBUTE_READONLY, which refuses overwriting or deleting a mounted file;
// creating a new file in the directory is still possible there, and the manifest
// says copy-ro rather than claiming a bind mount.
func copyReadOnly(source, target string) error {
	if err := copyTree(source, target); err != nil {
		return err
	}
	return chmodTree(target, true)
}

// chmodTree sets every file (and then every directory, deepest first) to a
// writable or read-only mode. Missing entries are ignored: the same helper is
// used after a partial deletion.
func chmodTree(root string, readOnly bool) error {
	fileMode, dirMode := os.FileMode(0o644), os.FileMode(0o755)
	if readOnly {
		fileMode, dirMode = 0o444, 0o555
	}
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			// Do not follow a link: chmod follows it, which would change a file
			// outside the tree.
			return nil
		}
		if err := os.Chmod(p, fileMode); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], dirMode); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// clearMountedTree removes a previous mount or copy from the host.
//
// A stale mount must not break provisioning, so this is deliberately forgiving:
// detach best-effort (the kernel says EINVAL when it was never a mount point),
// then clear read-only bits, then retry once. Callers report a failure per skill
// instead of failing the session.
func clearMountedTree(target string, opts Options) error {
	if _, err := os.Lstat(target); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	unmount := opts.Unmount
	if unmount == nil {
		unmount = unmountPath
	}
	// A lazy detach is safe on a normal directory (it fails) and is the only way
	// to remove a mount point that a crashed session left behind.
	_ = unmount(target)
	if err := os.RemoveAll(target); err == nil {
		return nil
	}
	if err := chmodTree(target, false); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.RemoveAll(target)
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

// copyTreeFS copies a bundle from the control plane's disk into fs.
//
// The source is always local: bundles are uploaded to, and stored on, the
// control plane. Only the destination can be remote, which is why the walk is
// os.WalkDir and the writes go through the filesystem.
func copyTreeFS(fs FileSystem, source, target string) error {
	return filepath.WalkDir(source, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(source, p)
		if rerr != nil {
			return rerr
		}
		dest := target
		if rel != "." {
			dest = fs.Join(target, filepath.ToSlash(rel))
		}
		if d.IsDir() {
			return fs.MkdirAll(dest)
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill bundle contains a symlink: %s", rel)
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		return fs.WriteFile(dest, data, 0o644)
	})
}

// Read reports what is currently mounted in root, from the manifest this
// package writes on the control plane's own disk. A missing manifest is not an
// error: it means nothing was mounted yet.
func Read(root string) Manifest { return readManifestFS(hostFS{}, root) }

// ReadFS reports what is mounted in root through fs. It is what a deployment
// whose checkout lives inside a container reads through the sandbox API.
func ReadFS(fs FileSystem, root string) Manifest { return readManifestFS(fs, root) }

// ParseManifest turns the manifest file's bytes into a Manifest, applying the
// same "empty means none" normalisation Read does. It exists for callers that
// read the file through the workspace abstraction (which hands back bytes)
// rather than through a FileSystem.
func ParseManifest(raw []byte) Manifest {
	var m Manifest
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil {
		return Manifest{}.normalized()
	}
	return m.normalized()
}

// Digest is a stable fingerprint of a mounted skill bundle (FR-14.5) on the
// control plane's own disk.
//
// It hashes every file's path and bytes, in sorted path order, so the same
// files always produce the same value regardless of directory-walk order and a
// changed byte anywhere produces a different one. This is what lets a session
// record a version even when the skill's author never declared one, and it is
// the same shape the upload path hashes — an unchanged bundle therefore has the
// same digest in `skills.content_hash` and in the session's record.
func Digest(dir string) (string, error) {
	files, err := walkDigest(dir)
	if err != nil {
		return "", err
	}
	return hashFiles(files), nil
}

// DigestFS is Digest over fs: the fingerprint of what the sandbox actually
// stores, read back through the API. Reading it back rather than hashing the
// source bundle is deliberate — the record says what the agent reads, and the
// round trip is also the proof that the copy landed.
func DigestFS(fs FileSystem, dir string) (string, error) {
	if fs == nil {
		return "", errors.New("materialize: digest: a filesystem is required")
	}
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("materialize: digest: directory is required")
	}
	files := make([]digestFile, 0, 8)
	var walk func(dir, prefix string) error
	walk = func(dir, prefix string) error {
		entries, err := fs.List(dir)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		for _, entry := range entries {
			rel := entry.Name
			if prefix != "" {
				rel = prefix + "/" + entry.Name
			}
			full := fs.Join(dir, entry.Name)
			if entry.IsDir {
				if err := walk(full, rel); err != nil {
					return err
				}
				continue
			}
			data, rerr := fs.ReadFile(full)
			if rerr != nil {
				return rerr
			}
			files = append(files, digestFile{rel: rel, data: data})
		}
		return nil
	}
	if err := walk(dir, ""); err != nil {
		return "", fmt.Errorf("materialize: digest %s: %w", dir, err)
	}
	return hashFiles(files), nil
}

// digestFile is one file's contribution to a bundle's fingerprint.
type digestFile struct {
	rel  string
	data []byte
}

// walkDigest collects a bundle's files from the host, refusing symlinks: a
// bundle with a symlink is refused at mount time too, and hashing the link's
// target would make the digest depend on the host.
func walkDigest(dir string) ([]digestFile, error) {
	files := make([]digestFile, 0, 8)
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill bundle contains a symlink: %s", p)
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		files = append(files, digestFile{rel: filepath.ToSlash(rel), data: data})
		return nil
	})
	return files, err
}

// hashFiles is the one framing both digest paths share: path, NUL, content,
// NUL, in sorted order. Without the separators "ab"+"c" and "a"+"bc" would hash
// the same.
func hashFiles(files []digestFile) string {
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	hasher := sha256.New()
	for _, f := range files {
		fmt.Fprintf(hasher, "%s\x00%s\x00", f.rel, f.data)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func readManifestFS(fs FileSystem, root string) Manifest {
	var m Manifest
	data, err := fs.ReadFile(fs.Join(root, manifestRel))
	if err != nil {
		return Manifest{}
	}
	if json.Unmarshal(data, &m) != nil {
		return Manifest{}
	}
	return m
}

func writeManifestFS(fs FileSystem, root string, m Manifest) error {
	p := fs.Join(root, manifestRel)
	payload, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("materialize: encode manifest: %w", err)
	}
	if err := fs.WriteFile(p, append(payload, '\n'), 0o644); err != nil {
		return fmt.Errorf("materialize: write manifest: %w", err)
	}
	return nil
}

// removeIfPresent removes one file, treating "not there" as success.
func removeIfPresent(fs FileSystem, p string) error {
	kind, err := fs.Lookup(p)
	if err != nil {
		return err
	}
	if kind == KindMissing {
		return nil
	}
	return fs.RemoveAll(p)
}

// excludeFromGitFS appends entries to the repository's info/exclude, which is
// the per-checkout ignore file that is never committed.
func excludeFromGitFS(fs FileSystem, root string, entries []string) error {
	gitDir, err := resolveGitDirFS(fs, root)
	if err != nil {
		return err
	}
	if gitDir == "" {
		// Not a repository (yet): nothing to exclude from, and that is fine.
		return nil
	}
	infoDir := fs.Join(gitDir, "info")
	if err := fs.MkdirAll(infoDir); err != nil {
		return fmt.Errorf("materialize: create git info dir: %w", err)
	}
	p := fs.Join(infoDir, "exclude")

	existing := ""
	if data, rerr := fs.ReadFile(p); rerr == nil {
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
	if err := fs.WriteFile(p, []byte(existing), 0o644); err != nil {
		return fmt.Errorf("materialize: write git exclude: %w", err)
	}
	return nil
}

// resolveGitDirFS finds the real git directory for root, following a worktree's
// `.git` file. It returns "" when root is not inside a repository.
func resolveGitDirFS(fs FileSystem, root string) (string, error) {
	dotGit := fs.Join(root, ".git")
	kind, err := fs.Lookup(dotGit)
	if err != nil {
		return "", nil
	}
	if kind == KindMissing {
		return "", nil
	}
	if kind == KindDir {
		return dotGit, nil
	}
	data, err := fs.ReadFile(dotGit)
	if err != nil {
		return "", fmt.Errorf("materialize: read .git: %w", err)
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return "", nil
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if !fs.IsAbs(target) {
		target = fs.Join(root, target)
	}
	return fs.Join(target), nil
}
