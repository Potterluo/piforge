package httpapi

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/gitx"
	"github.com/piforge/piforge/backend/internal/provision"
	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/tenantfs"
)

// Containment of tenant-controlled trees (FIX 1 of
// e2e/.artifacts/adversarial3): `POST /v1/skills/import` cloned a repository and
// read every file under a SKILL.md with os.ReadFile, which follows a symlink. A
// repository containing `leakskill/key.txt -> /proc/1/environ` handed the control
// plane's environment — including the real provider key and PIFORGE_MASTER_KEY —
// back to the tenant as a skill bundle.
//
// The tests below are the report's attack, its /data variant, the zip forms of the
// same primitive, the clone half (a declared link materialises as a file), and the
// ordinary import that has to keep working.

// gitIn runs git with a fixed identity and, when input is not empty, feeds it on
// stdin (hash-object needs it to write the blob behind a declared symlink).
func gitIn(t *testing.T, dir, input string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// skillRepo builds a real git repository from rel -> body, committed once. The
// importer clones from it over file://, which is the transport a self-hosted
// install uses and the one a test can create without a network.
func skillRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := exec.Command("git", "init", "--initial-branch=main", dir).CombinedOutput(); err != nil {
		// Older git has no --initial-branch; the branch name does not matter here.
		if out, ierr := exec.Command("git", "init", dir).CombinedOutput(); ierr != nil {
			t.Fatalf("git init: %v\n%s", ierr, out)
		}
	}
	for rel, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	gitIn(t, dir, "", "add", "-A")
	gitIn(t, dir, "", "commit", "-m", "skills")
	return dir
}

// declareSymlink adds a mode-120000 entry to the repository without needing the
// machine to be able to create a real symlink: the same entry a hostile
// repository would carry, byte for byte.
func declareSymlink(t *testing.T, repo, rel, target string) {
	t.Helper()
	blob := gitIn(t, repo, target, "hash-object", "-w", "--stdin")
	gitIn(t, repo, "", "update-index", "--add", "--cacheinfo", "120000,"+blob+","+rel)
	gitIn(t, repo, "", "commit", "-m", "add "+rel)
}

// repoURL is the file:// form git and the handler both accept.
func repoURL(path string) string {
	slash := filepath.ToSlash(path)
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	return "file://" + slash
}

// outsideTree writes a file the control plane owns and returns its path: the
// stand-in for /proc/1/environ and /data/master.key. The bodies carry markers a
// test can search a response for.
func outsideTree(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return full
}

// assertNothingImported checks the two halves of "the whole import was refused":
// no row and no bundle directory.
func assertNothingImported(t *testing.T, env *testEnv, userID string) {
	t.Helper()
	skills, err := env.store.ListSkills(t.Context(), userID)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) != 0 {
		t.Fatalf("a refused import created %d skill row(s): %+v", len(skills), skills)
	}
	if entries, rerr := os.ReadDir(env.deps.SkillsDir); rerr == nil && len(entries) != 0 {
		t.Fatalf("a refused import left %d bundle director(ies) behind", len(entries))
	}
}

// TestImportRefusesASymlinkToTheEnvironment is the report's attack, end to end
// through the route the tenant uses: a repository whose "key" file is declared
// as a symbolic link to the control plane's environment.
func TestImportRefusesASymlinkToTheEnvironment(t *testing.T) {
	env := newTestEnvWithSkills(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	client := env.login("dev@piforge.local", "pw")

	const providerKey = "sk-adversarial-acceptance-3-0123456789"
	secret := outsideTree(t, "environ",
		"PIFORGE_MASTER_KEY=MEASURED-MASTER-KEY\nPIFORGE_BOOTSTRAP_PROVIDER_KEY="+providerKey+"\n")

	repo := skillRepo(t, map[string]string{"leakskill/SKILL.md": "An innocent skill.\n"})
	declareSymlink(t, repo, "leakskill/key.txt", secret)

	rec := env.doAuthed(http.MethodPost, "/v1/skills/import",
		map[string]string{"url": repoURL(repo)}, client)
	env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)

	body := rec.Body.String()
	if !strings.Contains(body, "leakskill/key.txt") {
		t.Fatalf("the refusal does not name the offending path:\n%s", body)
	}
	if !strings.Contains(body, "符号链接") {
		t.Fatalf("the refusal does not say what was refused:\n%s", body)
	}
	if strings.Contains(body, providerKey) || strings.Contains(body, "MEASURED-MASTER-KEY") {
		t.Fatal("the refusal carried the control plane's own secrets")
	}
	assertNothingImported(t, env, user.ID)
}

// The /data variant: the master key is the file that decrypts every sealed
// provider key and PR token in the database. Same mechanism, same refusal — and
// the refusal must be the whole import, so a legitimate skill shipped next to the
// link does not get imported either.
func TestImportRefusesASymlinkToTheMasterKey(t *testing.T) {
	env := newTestEnvWithSkills(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	client := env.login("dev@piforge.local", "pw")

	masterKey := outsideTree(t, "master.key", "MASTER-KEY-BYTES-THAT-MUST-NOT-ESCAPE")
	repo := skillRepo(t, map[string]string{
		"pdf-tools/SKILL.md": "A perfectly ordinary skill next to the attack.\n",
		"tools/README.txt":   "not a skill\n",
	})
	// The link is in a directory that holds no SKILL.md of its own, which is what
	// "refuse the whole import" has to cover.
	declareSymlink(t, repo, "tools/master.key", masterKey)

	rec := env.doAuthed(http.MethodPost, "/v1/skills/import",
		map[string]string{"url": repoURL(repo)}, client)
	env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)
	body := rec.Body.String()
	if !strings.Contains(body, "tools/master.key") {
		t.Fatalf("the refusal does not name the offending path:\n%s", body)
	}
	if strings.Contains(body, "MASTER-KEY-BYTES") {
		t.Fatal("the refusal carried the master key")
	}
	assertNothingImported(t, env, user.ID)

	// The ordinary skill in the same repository was not imported either: refusing
	// one file must not leave a half-imported collection behind.
	if skills, _ := env.store.ListSkills(t.Context(), user.ID); len(skills) != 0 {
		t.Fatalf("the legit skill was imported despite the refusal: %+v", skills)
	}
}

// findSkills is the fence under the transport. Even if a link ever reached the
// checkout's disk (an older git that ignores core.symlinks, a clone from a
// filesystem that does not support the flag, a future transport), the walk
// refuses it, names it, and returns before any skill row is written.
func TestFindSkillsRefusesALinkOnDisk(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func(t *testing.T) string
		rel    string
	}{
		{
			name: "environment",
			target: func(t *testing.T) string {
				return outsideTree(t, "environ", "PIFORGE_MASTER_KEY=x\n")
			},
			rel: "leakskill/key.txt",
		},
		{
			name: "master-key",
			target: func(t *testing.T) string {
				// The report's own path. On a machine without /data it is a
				// dangling link, which is refused the same way.
				return "/data/master.key"
			},
			rel: "leakskill/master.key",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "leakskill"), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, "leakskill", "SKILL.md"), []byte("x\n"), 0o644); err != nil {
				t.Fatalf("write SKILL.md: %v", err)
			}
			target := tc.target(t)
			full := filepath.Join(root, filepath.FromSlash(tc.rel))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatalf("mkdir for link: %v", err)
			}
			if err := os.Symlink(target, full); err != nil {
				t.Skipf("this machine cannot create symlinks: %v", err)
			}

			_, err := findSkills(root, skillImportMaxBytes)
			if err == nil {
				t.Fatal("a symlink in the checkout was walked")
			}
			var refusal *tenantfs.Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("error %v is not a typed containment refusal", err)
			}
			if filepath.ToSlash(refusal.Path) != tc.rel {
				t.Fatalf("refusal names %q, want %q", refusal.Path, tc.rel)
			}
			if refusal.Kind != tenantfs.KindSymlink {
				t.Fatalf("refusal kind = %q", refusal.Kind)
			}
			if !strings.Contains(importRefusalMessage(err), tc.rel) {
				t.Fatalf("the message does not name the path: %s", importRefusalMessage(err))
			}
		})
	}
}

// The clone half: git is told not to create links, so a declared link lands as
// the regular text file it would have pointed at. The tenant gets no read of the
// target, and the import still refuses the repository (the index declares it).
func TestCloneMaterialisesADeclaredSymlinkAsAFile(t *testing.T) {
	secret := outsideTree(t, "environ", "PIFORGE_MASTER_KEY=DO-NOT-READ\n")
	repo := skillRepo(t, map[string]string{"leakskill/SKILL.md": "x\n"})
	declareSymlink(t, repo, "leakskill/key.txt", secret)

	dest := filepath.Join(t.TempDir(), "checkout")
	if err := cloneCheckout(t.Context(), repoURL(repo), "", dest, DefaultSkillCloneMaxBytes, testURLPolicy()); err != nil {
		t.Fatalf("cloneCheckout: %v", err)
	}

	full := filepath.Join(dest, "leakskill", "key.txt")
	info, err := os.Lstat(full)
	if err != nil {
		t.Fatalf("stat key.txt: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the clone created a live symlink on the control plane's disk")
	}
	body, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("read key.txt: %v", err)
	}
	if string(body) != secret {
		t.Fatalf("key.txt = %q, want the link's target as text (not the target's bytes)", body)
	}
	if strings.Contains(string(body), "DO-NOT-READ") {
		t.Fatal("the clone read through the link")
	}

	// And the walk would import it as a plain file: the content is the target's
	// *name*, which is what the tenant already knew.
	found, err := findSkills(dest, skillImportMaxBytes)
	if err != nil {
		t.Fatalf("findSkills on a materialised checkout: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("found %d skills", len(found))
	}
	if got := found[0].Files["key.txt"]; got != secret {
		t.Fatalf("bundle key.txt = %q", got)
	}
}

// The handler refuses the same repository even though the checkout now holds a
// text file: the index says 120000, and a tenant who ships a link is refused,
// not quietly given a skill whose key file is a path string.
func TestImportRefusesARepositoryThatDeclaresASymlink(t *testing.T) {
	env := newTestEnvWithSkills(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	client := env.login("dev@piforge.local", "pw")

	repo := skillRepo(t, map[string]string{"leakskill/SKILL.md": "x\n"})
	declareSymlink(t, repo, "leakskill/key.txt", "/proc/1/environ")

	rec := env.doAuthed(http.MethodPost, "/v1/skills/import",
		map[string]string{"url": repoURL(repo)}, client)
	env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)
	if !strings.Contains(rec.Body.String(), "leakskill/key.txt") {
		t.Fatalf("the refusal does not name the offending path:\n%s", rec.Body.String())
	}
	assertNothingImported(t, env, user.ID)
}

// The zip forms of the same primitive: an absolute entry and one that climbs out
// are refused as a whole, through the route.
func TestZipImportRefusesAnAbsoluteOrTraversingEntry(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry zipEntry
	}{
		{name: "absolute", entry: zipEntry{name: "/proc/1/environ", body: "x"}},
		{name: "traversal", entry: zipEntry{name: "../../data/master.key", body: "x"}},
		{
			name: "symlink",
			entry: zipEntry{name: "leakskill/key.txt", body: "/proc/1/environ",
				mode: os.ModeSymlink | 0o777},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnvWithSkills(t)
			user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
			client := env.login("dev@piforge.local", "pw")

			archive := mkzip(t, []zipEntry{
				tc.entry,
				{name: "fine/SKILL.md", body: "a legitimate skill next to the attack\n"},
			})
			contentType, body := zipUpload(t, "collection.zip", archive)
			rec := env.doRaw(http.MethodPost, "/v1/skills/import/zip", contentType, body, client)
			env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)
			assertNothingImported(t, env, user.ID)
		})
	}
}

// A legitimate repository still imports, and a session created afterwards really
// mounts it: containment must not have cost the feature.
func TestLegitimateRepositoryStillImportsAndMounts(t *testing.T) {
	env := newTestEnvWithSkills(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	client := env.login("dev@piforge.local", "pw")

	root := t.TempDir()
	local, err := sandbox.NewLocal(sandbox.LocalConfig{Root: root, InitialBranch: "main"})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	env.deps.Provisioner = provision.New(provision.Config{
		Store:     env.store,
		Sandbox:   local,
		Git:       gitx.New(gitx.Config{}),
		SkillsDir: env.skillsDir,
	})
	env.rebuild()

	repo := skillRepo(t, map[string]string{
		"collection/pdf-tools/SKILL.md":         "---\nname: PDF\n---\nRead and split PDFs.\n",
		"collection/pdf-tools/scripts/split.py": "print('split')\n",
		"collection/other/SKILL.md":             "Another one.\n",
	})
	rec := env.doAuthed(http.MethodPost, "/v1/skills/import",
		map[string]string{"url": repoURL(repo)}, client)
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d %s", rec.Code, rec.Body.String())
	}
	var imported struct {
		Skills []importedSkill `json:"skills"`
	}
	env.mustJSON(rec, &imported)
	if len(imported.Skills) != 2 {
		t.Fatalf("import returned %d skills: %s", len(imported.Skills), rec.Body.String())
	}
	for _, item := range imported.Skills {
		if item.Skipped || !item.Enabled {
			t.Fatalf("skill %q = %+v", item.Name, item)
		}
	}

	// The bundle is readable through the same containment the import enforces.
	skills, err := env.store.ListSkills(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("%d skill rows", len(skills))
	}
	bundle := env.doAuthed(http.MethodGet, "/v1/skills/"+skills[0].ID+"/bundle", nil, client)
	if bundle.Code != http.StatusOK {
		t.Fatalf("bundle = %d %s", bundle.Code, bundle.Body.String())
	}
	var files struct {
		Files []skillFile `json:"files"`
	}
	env.mustJSON(bundle, &files)
	if len(files.Files) == 0 {
		t.Fatal("the imported bundle is empty")
	}

	// And a session created now mounts it where the agent reads skills.
	project, _ := env.mkSessionFor(user.ID, "import-target")
	mounted := createSessionAndMountPath(t, env, client, project.ID, root)
	body, err := os.ReadFile(filepath.Join(mounted, "SKILL.md"))
	if err != nil {
		t.Fatalf("the imported skill was not mounted: %v", err)
	}
	if !strings.Contains(string(body), "Read and split PDFs") {
		t.Fatalf("mounted SKILL.md = %q", body)
	}
}

// The bundle read-back is confined too: a link inside a skills directory (which
// nothing in this build creates, but a restore or a hand copy can) is refused
// instead of served.
func TestSkillBundleReadRefusesALink(t *testing.T) {
	env := newTestEnvWithSkills(t)
	env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	client := env.login("dev@piforge.local", "pw")

	secret := outsideTree(t, "environ", "PIFORGE_MASTER_KEY=MUST-NOT-BE-SERVED\n")
	created := env.doAuthed(http.MethodPost, "/v1/skills", map[string]string{
		"name": "pdf-tools", "description": "d", "body": "hello\n",
	}, client)
	if created.Code != http.StatusCreated {
		t.Fatalf("create skill = %d %s", created.Code, created.Body.String())
	}
	var createdBody struct {
		Skill *contracts.Skill `json:"skill"`
	}
	env.mustJSON(created, &createdBody)

	bundleDir := filepath.Join(env.deps.SkillsDir, createdBody.Skill.ID)
	if err := os.Symlink(secret, filepath.Join(bundleDir, "key.txt")); err != nil {
		t.Skipf("this machine cannot create symlinks: %v", err)
	}

	rec := env.doAuthed(http.MethodGet, "/v1/skills/"+createdBody.Skill.ID+"/bundle", nil, client)
	if rec.Code == http.StatusOK {
		t.Fatalf("the bundle was served with a link in it: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "MUST-NOT-BE-SERVED") {
		t.Fatal("the bundle read followed the link")
	}
}
