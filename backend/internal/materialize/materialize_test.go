package materialize

import (
	"encoding/json"
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

	// Disabled servers must not be written at all.
	if len(result.MCPServers) != 2 {
		t.Fatalf("mcp servers = %v", result.MCPServers)
	}
	raw, err := os.ReadFile(result.MCPPath)
	if err != nil {
		t.Fatalf("read mcp config: %v", err)
	}
	var config struct {
		Servers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("mcp config is not JSON: %v", err)
	}
	if _, ok := config.Servers["off"]; ok {
		t.Fatal("a disabled MCP server was written")
	}
	if got := config.Servers["playwright"]["command"]; got != "npx" {
		t.Fatalf("stdio command = %v", got)
	}
	if got := config.Servers["docs"]["type"]; got != "http" {
		t.Fatalf("http type = %v", got)
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

func TestMountRemovesMCPConfigItWrote(t *testing.T) {
	work := t.TempDir()
	root := filepath.Join(work, "repo")

	if _, err := Mount(root, nil, []*contracts.MCPServer{
		{Name: "s", Transport: contracts.MCPStdio, Command: "run", Enabled: true},
	}, Options{}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, MCPConfig)); err != nil {
		t.Fatalf("mcp config missing: %v", err)
	}

	// With the server removed, the file we created must go away.
	if _, err := Mount(root, nil, nil, Options{}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, MCPConfig)); !os.IsNotExist(err) {
		t.Fatalf("mcp config survived with no servers: %v", err)
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
