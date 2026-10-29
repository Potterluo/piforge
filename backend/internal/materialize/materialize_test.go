package materialize

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

func writeBundle(t *testing.T, root, key, body string) string {
	t.Helper()
	dir := filepath.Join(root, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return dir
}

func skill(name, bundleKey string) *contracts.Skill {
	return &contracts.Skill{Name: name, BundleKey: bundleKey, Enabled: true}
}

func TestMountWritesSkillsAndMCPConfig(t *testing.T) {
	work := t.TempDir()
	bundles := t.TempDir()
	writeBundle(t, bundles, "pdf-1", "---\nname: pdf\n---\nRead PDFs.\n")

	root := filepath.Join(work, "repo")
	result, err := Mount(root, []*contracts.Skill{skill("PDF Tools", "pdf-1")}, []*contracts.MCPServer{
		{Name: "playwright", Transport: contracts.MCPStdio, Command: "npx", Args: []string{"-y", "@playwright/mcp"}, Enabled: true},
		{Name: "docs", Transport: contracts.MCPHTTP, URL: "https://docs.example/mcp", Enabled: true},
		{Name: "off", Transport: contracts.MCPStdio, Command: "noop", Enabled: false},
	}, Options{SkillsDir: bundles})
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	// The skill directory name must be derived, not trusted.
	if len(result.Skills) != 1 || result.Skills[0] != "pdf-tools" {
		t.Fatalf("skills = %v, want [pdf-tools]", result.Skills)
	}
	mounted := filepath.Join(root, SkillsRoot, "pdf-tools", "SKILL.md")
	if body, err := os.ReadFile(mounted); err != nil || !strings.Contains(string(body), "Read PDFs") {
		t.Fatalf("SKILL.md = %q, %v", body, err)
	}

	// Mount reports the enabled servers, and deliberately writes no MCP file: Pi reads
	// the one `pimodel.WriteMCP` puts in the agent directory, and a second file here
	// would win over it and drop the credentials.
	if len(result.MCPServers) != 2 {
		t.Fatalf("mcp servers = %v", result.MCPServers)
	}
	if result.MCPPath != "" {
		t.Fatalf("mount wrote an MCP file at %q", result.MCPPath)
	}
	if _, err := os.Stat(filepath.Join(root, MCPConfig)); !os.IsNotExist(err) {
		t.Fatalf("an MCP file appeared in the worktree: %v", err)
	}
}

func TestMountIsIdempotentAndRemovesStaleSkills(t *testing.T) {
	work := t.TempDir()
	bundles := t.TempDir()
	writeBundle(t, bundles, "a", "alpha\n")
	writeBundle(t, bundles, "b", "beta\n")
	root := filepath.Join(work, "repo")

	first, err := Mount(root, []*contracts.Skill{skill("alpha", "a"), skill("beta", "b")}, nil, Options{SkillsDir: bundles})
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if len(first.Skills) != 2 {
		t.Fatalf("first mount = %v", first.Skills)
	}

	// Mounting the same thing again changes nothing.
	second, err := Mount(root, []*contracts.Skill{skill("alpha", "a"), skill("beta", "b")}, nil, Options{SkillsDir: bundles})
	if err != nil {
		t.Fatalf("second Mount: %v", err)
	}
	if strings.Join(second.Skills, ",") != strings.Join(first.Skills, ",") {
		t.Fatalf("second mount = %v, want %v", second.Skills, first.Skills)
	}

	// Dropping a skill must remove its directory: a stale SKILL.md would keep
	// feeding instructions to the agent for the rest of the session.
	third, err := Mount(root, []*contracts.Skill{skill("alpha", "a")}, nil, Options{SkillsDir: bundles})
	if err != nil {
		t.Fatalf("third Mount: %v", err)
	}
	if strings.Join(third.Skills, ",") != "alpha" {
		t.Fatalf("third mount = %v", third.Skills)
	}
	if _, err := os.Stat(filepath.Join(root, SkillsRoot, "beta")); !os.IsNotExist(err) {
		t.Fatalf("stale skill beta is still mounted: %v", err)
	}

	// Files we did not write are left alone.
	custom := filepath.Join(root, SkillsRoot, "handwritten")
	if err := os.MkdirAll(custom, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := Mount(root, nil, nil, Options{SkillsDir: bundles}); err != nil {
		t.Fatalf("empty Mount: %v", err)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Fatalf("Mount deleted a directory it did not own: %v", err)
	}
}

func TestMountSkipsSkillsWithoutContent(t *testing.T) {
	work := t.TempDir()
	bundles := t.TempDir()
	root := filepath.Join(work, "repo")

	result, err := Mount(root, []*contracts.Skill{
		skill("ghost", "does-not-exist"),
		{Name: "no-bundle", Enabled: true},
	}, nil, Options{SkillsDir: bundles})
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if len(result.Skills) != 0 {
		t.Fatalf("mounted a skill with no content: %v", result.Skills)
	}
	if len(result.Skipped) != 2 {
		t.Fatalf("skipped = %v, want both", result.Skipped)
	}

	// A bundle without SKILL.md is invisible to Pi, so it must not be mounted.
	writeBundle(t, bundles, "empty", "ignored\n")
	if err := os.Remove(filepath.Join(bundles, "empty", "SKILL.md")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	result, err = Mount(root, []*contracts.Skill{skill("empty", "empty")}, nil, Options{SkillsDir: bundles})
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if len(result.Skills) != 0 || result.Skipped["empty"] == "" {
		t.Fatalf("result = %+v, want empty with a skip reason", result)
	}
}

// A worktree that already has an MCP file from an older build must lose it: it named a
// file Pi does not read, and leaving it behind would keep the lie alive.
func TestMountRemovesAStaleMCPFileFromTheWorktree(t *testing.T) {
	work := t.TempDir()
	root := filepath.Join(work, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale := filepath.Join(root, MCPConfig)
	if err := os.WriteFile(stale, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := Mount(root, nil, []*contracts.MCPServer{
		{Name: "s", Transport: contracts.MCPStdio, Command: "run", Enabled: true},
	}, Options{}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the stale MCP file survived: %v", err)
	}
}

func TestMountRefusesPathTraversal(t *testing.T) {
	work := t.TempDir()
	bundles := t.TempDir()
	root := filepath.Join(work, "repo")

	// A bundle_key that tries to escape the skills root resolves to nothing.
	outside := filepath.Join(work, "secret")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte("stolen\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	result, err := Mount(root, []*contracts.Skill{skill("evil", "../../secret")}, nil, Options{SkillsDir: bundles})
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if len(result.Skills) != 0 {
		t.Fatalf("mounted a bundle from outside the skills root: %v", result.Skills)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"PDF Tools":        "pdf-tools",
		"already-slug":     "already-slug",
		"../../etc/passwd": "etc-passwd",
		"  空格 名字  ":        "",
		"a/b":              "a-b",
	}
	for in, want := range cases {
		got, err := slugify(in)
		if want == "" {
			if err == nil {
				t.Errorf("slugify(%q) = %q, want an error", in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("slugify(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMountRejectsSymlinkedBundles(t *testing.T) {
	work := t.TempDir()
	bundles := t.TempDir()
	root := filepath.Join(work, "repo")

	target := filepath.Join(bundles, "real")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(bundles, "linked", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(filepath.Join(target, "SKILL.md"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := Mount(root, []*contracts.Skill{skill("linked", "linked")}, nil, Options{SkillsDir: bundles}); err == nil {
		t.Fatal("a bundle containing a symlink was mounted")
	}
}

// TestMountExcludesToolingFromGitStatus covers the consequence of mounting into
// a checkout: the files exist, so git would report them as the user's untracked
// changes. They are platform-owned, and info/exclude is where that belongs.
func TestMountExcludesToolingFromGitStatus(t *testing.T) {
	work := t.TempDir()
	root := filepath.Join(work, "repo")
	if err := os.MkdirAll(filepath.Join(root, ".git", "info"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	if _, err := Mount(root, nil, []*contracts.MCPServer{
		{Name: "s", Transport: contracts.MCPStdio, Command: "run", Enabled: true},
	}, Options{}); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	exclude, err := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("read exclude: %v", err)
	}
	for _, want := range []string{"/.pi/skills", "/.mcp.json"} {
		if !strings.Contains(string(exclude), want) {
			t.Fatalf("exclude is missing %q:\n%s", want, exclude)
		}
	}

	// Idempotent: a second mount must not append duplicates.
	if _, err := Mount(root, nil, nil, Options{}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	again, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	if strings.Count(string(again), "/.mcp.json") != 1 {
		t.Fatalf("exclude entries were duplicated:\n%s", again)
	}
}

// TestMountFollowsWorktreeGitFile covers the linked-worktree layout, where .git
// is a file pointing at the real git directory.
func TestMountFollowsWorktreeGitFile(t *testing.T) {
	work := t.TempDir()
	realGit := filepath.Join(work, "main", ".git", "worktrees", "branch")
	if err := os.MkdirAll(filepath.Join(realGit, "info"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	root := filepath.Join(work, "wt")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir wt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+realGit+"\n"), 0o644); err != nil {
		t.Fatalf("write .git file: %v", err)
	}

	if _, err := Mount(root, nil, nil, Options{}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	exclude, err := os.ReadFile(filepath.Join(realGit, "info", "exclude"))
	if err != nil {
		t.Fatalf("read worktree exclude: %v", err)
	}
	if !strings.Contains(string(exclude), "/.pi/skills") {
		t.Fatalf("worktree exclude = %s", exclude)
	}
}

// TestDigestIsContentAddressed covers the version derivation behind FR-14.5:
// the same files always fingerprint the same, and any content or path change
// produces a different one. Without the second half, editing a skill would look
// like no edit at all and two sessions would wrongly report the same version.
func TestDigestIsContentAddressed(t *testing.T) {
	first := t.TempDir()
	if err := os.WriteFile(filepath.Join(first, "SKILL.md"), []byte("# Review\nRead the diff.\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(first, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(first, "scripts", "check.sh"), []byte("echo hi\n"), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}

	base, err := Digest(first)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if len(base) != 64 {
		t.Fatalf("Digest = %q, want a sha256 hex string", base)
	}

	// Same content, different directory: the digest must not depend on where the
	// bundle was written or the order the filesystem returns files in.
	second := t.TempDir()
	if err := os.MkdirAll(filepath.Join(second, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(second, "scripts", "check.sh"), []byte("echo hi\n"), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if err := os.WriteFile(filepath.Join(second, "SKILL.md"), []byte("# Review\nRead the diff.\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	same, err := Digest(second)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if same != base {
		t.Fatalf("same content gave different digests: %s vs %s", base, same)
	}

	// A changed byte changes the version.
	if err := os.WriteFile(filepath.Join(second, "SKILL.md"), []byte("# Review\nRead the diff carefully.\n"), 0o644); err != nil {
		t.Fatalf("rewrite SKILL.md: %v", err)
	}
	changed, err := Digest(second)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if changed == base {
		t.Fatal("changed content kept the same digest, so a session could not tell two versions apart")
	}

	// Renaming a file with identical bytes is also a different bundle: the agent
	// sees a different path, and the digest is of the mounted tree.
	if err := os.Rename(filepath.Join(second, "scripts", "check.sh"), filepath.Join(second, "scripts", "verify.sh")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	renamed, err := Digest(second)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if renamed == changed {
		t.Fatal("a renamed file kept the same digest")
	}
}

// TestDigestMatchesTheUploadHash locks the derivation to the hash the upload path
// stores, so an untouched bundle records the same content hash it was created
// with. Two algorithms would make "the session's hash" disagree with the skill
// centre for the same bytes.
func TestDigestMatchesTheUploadHash(t *testing.T) {
	dir := t.TempDir()
	body := "---\nname: review\n---\n# Review\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := Digest(dir)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	// The same framing writeSkillBundle uses: "<path>\x00<content>\x00", sorted.
	hasher := sha256.New()
	fmt.Fprintf(hasher, "%s\x00%s\x00", "SKILL.md", body)
	if want := hex.EncodeToString(hasher.Sum(nil)); got != want {
		t.Fatalf("Digest = %s, upload hash = %s", got, want)
	}
}
