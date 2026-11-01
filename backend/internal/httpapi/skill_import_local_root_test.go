package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// `file://` is the one import transport whose "URL" is a path on this machine:
// git clones it with the control plane's own filesystem access. Accepting any
// path let a tenant name a repository the tenant cannot otherwise read and have
// its contents returned as a skill bundle — the same shape as the clone that
// copied /proc/1/environ, reached through the argument instead of a symlink.
//
// The rule is the one internal/tenantfs states for every tenant-shaped path:
// the deployment writes down which roots local imports may name, and a path
// outside them is refused before git runs. Empty means "this deployment has no
// local import roots", which refuses every file:// import rather than allowing
// every one.
func TestLocalSkillImportIsConfinedToTheDeploymentRoots(t *testing.T) {
	env := newTestEnvWithSkills(t)
	ctx := context.Background()
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	client := env.login("dev@piforge.local", "pw")

	// A real repository holding a marker, staged where a test can find it.
	repo := skillRepo(t, map[string]string{"evil/SKILL.md": "LOCAL-READ-MARKER\n"})
	url := repoURL(repo)

	base := t.TempDir()
	allowed := filepath.Join(base, "allowed")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatalf("mkdir allowed: %v", err)
	}

	assertNothingImported := func(when string) {
		t.Helper()
		skills, err := env.store.ListSkills(ctx, user.ID)
		if err != nil {
			t.Fatalf("ListSkills: %v", err)
		}
		if len(skills) != 0 {
			t.Fatalf("%s: a refused import created %d skill row(s)", when, len(skills))
		}
	}

	// 1. No roots configured: file:// is disabled, and the refusal says how to
	// enable it instead of naming a directory the deployment does not have.
	env.deps.SkillImportRoots = nil
	env.rebuild()
	rec := env.doAuthed(http.MethodPost, "/v1/skills/import", map[string]string{"url": url}, client)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("import with no roots = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "skill-import-roots") {
		t.Fatalf("the refusal does not name the setting: %s", body)
	}
	assertNothingImported("no roots")

	// 2. A root is configured, but the repository is outside it.
	env.deps.SkillImportRoots = []string{allowed}
	env.rebuild()
	rec = env.doAuthed(http.MethodPost, "/v1/skills/import", map[string]string{"url": url}, client)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("import outside the roots = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	assertNothingImported("outside the roots")

	// 3. A link inside the root is not a way back out: the path resolves to the
	// repository, which is outside, so the same refusal applies.
	link := filepath.Join(allowed, "escape")
	if err := os.Symlink(repo, link); err == nil {
		rec = env.doAuthed(http.MethodPost, "/v1/skills/import", map[string]string{"url": repoURL(link)}, client)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("import through a link = %d, want 400: %s", rec.Code, rec.Body.String())
		}
		assertNothingImported("through a link")
	} else {
		t.Logf("this machine cannot create symlinks, link case skipped: %v", err)
	}

	// 4. Inside a configured root the feature still works: the operator's intent
	// is the boundary, not "file:// is forbidden".
	env.deps.SkillImportRoots = []string{os.TempDir()}
	env.rebuild()
	rec = env.doAuthed(http.MethodPost, "/v1/skills/import", map[string]string{"url": url}, client)
	if rec.Code != http.StatusOK {
		t.Fatalf("import inside a configured root = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	skills, err := env.store.ListSkills(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("import inside the root created %d skills, want 1", len(skills))
	}
}

// A repository reached through a skill bundle would be the other half of the
// same trick: upload `.git/config` as a bundle file, then name the bundle
// directory with file://. Bundles are instructions, not repositories, so the
// path is refused outright.
func TestSkillBundleRefusesGitStoragePaths(t *testing.T) {
	env := newTestEnvWithSkills(t)
	_ = env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	client := env.login("dev@piforge.local", "pw")

	created := env.doAuthed(http.MethodPost, "/v1/skills", map[string]string{
		"name": "bundle-repo", "body": "hello\n",
	}, client)
	if created.Code != http.StatusCreated {
		t.Fatalf("create skill = %d %s", created.Code, created.Body.String())
	}
	var body struct {
		Skill *contracts.Skill `json:"skill"`
	}
	env.mustJSON(created, &body)

	// The spellings Windows normalises onto `.git`: a trailing dot, a trailing
	// space, and two dots. Adversarial round 5 uploaded `.git./config`, got 200,
	// and found `.git\config` on the volume — Win32 strips the dot after the
	// check has run.
	for _, rel := range []string{
		".git/config", "nested/.git/HEAD", ".GIT/index",
		".git./config", ".git /config", ".git../HEAD", "nested/.git./config",
	} {
		rec := env.doAuthed(http.MethodPut, "/v1/skills/"+body.Skill.ID+"/bundle", map[string]any{
			"files": []map[string]string{
				{"path": "SKILL.md", "content": "ok\n"},
				{"path": rel, "content": "x\n"},
			},
		}, client)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("bundle path %q = %d, want 400: %s", rel, rec.Code, rec.Body.String())
		}
	}
	// The refused uploads changed nothing: no git storage on disk, and the
	// bundle still holds exactly the file the create wrote.
	for _, rel := range []string{".git", "nested/.git", ".GIT"} {
		if _, err := os.Stat(filepath.Join(env.skillsDir, body.Skill.ID, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("a refused bundle left %q on disk: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(env.skillsDir, body.Skill.ID, "SKILL.md")); err != nil {
		t.Fatalf("the existing bundle was disturbed: %v", err)
	}
}

// The three spellings of "a path on this machine". Windows authors write
// file://C:/dir as often as file:///C:/dir, and the two parse differently (the
// drive letter is the host in the first), so both have to name the same local
// path -- while a real host still does not.
func TestLocalPathFromFileURL(t *testing.T) {
	type tc struct {
		in   string
		want string
		bad  bool
	}
	cases := []tc{
		{in: "file://fileserver/share/x", bad: true},
		{in: "https://example.com/x", bad: true},
	}
	if runtime.GOOS == "windows" {
		// A POSIX path is not an absolute path here, and the drive forms are.
		cases = append(cases,
			tc{in: "file:///tmp/x", bad: true},
			tc{in: "file://C:/Users/x/repo", want: `C:\Users\x\repo`},
			tc{in: "file:///C:/Users/x/repo", want: `C:\Users\x\repo`},
		)
	} else {
		cases = append(cases,
			tc{in: "file:///tmp/x", want: filepath.Clean("/tmp/x")},
			tc{in: "file://localhost/tmp/x", want: filepath.Clean("/tmp/x")},
		)
	}
	for _, c := range cases {
		got, err := localPathFromFileURL(c.in)
		if c.bad {
			if err == nil {
				t.Fatalf("localPathFromFileURL(%q) = %q, want a refusal", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("localPathFromFileURL(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("localPathFromFileURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The resolved path is what git is handed, and that is what closes the
// check-then-use race: `imports/race` is verified once, and the clone then names
// `imports/race-real`, which no flip can change. This asserts the property
// directly, because a race is not something a test can schedule.
func TestImportURLHandsGitTheResolvedPath(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "allowed")
	real := filepath.Join(root, "race-real")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{real, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	link := filepath.Join(root, "race")
	if err := makeTestLink(link, real); err != nil {
		t.Skipf("this machine cannot create a %s link: %v", testLinkKind(), err)
	}

	srv := &server{
		deps: Deps{SkillImportRoots: []string{root}},
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	got, err := srv.importURL(context.Background(), repoURL(link))
	if err != nil {
		t.Fatalf("importURL: %v", err)
	}
	if !strings.Contains(got, "race-real") || strings.Contains(got, "race/") {
		t.Fatalf("git would be handed %q, want the resolved directory", got)
	}

	// And a link that leaves the root is refused, resolved or not.
	outsideLink := filepath.Join(root, "escape")
	if err := makeTestLink(outsideLink, outside); err == nil {
		if _, err := srv.importURL(context.Background(), repoURL(outsideLink)); err == nil {
			t.Fatal("a link to a directory outside the root was accepted")
		}
	}
}

func makeTestLink(link, target string) error {
	if runtime.GOOS == "windows" {
		out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
		if err != nil {
			return &testLinkError{msg: string(out)}
		}
		return nil
	}
	return os.Symlink(target, link)
}

func testLinkKind() string {
	if runtime.GOOS == "windows" {
		return "junction"
	}
	return "symbolic"
}

type testLinkError struct{ msg string }

func (e *testLinkError) Error() string { return e.msg }
