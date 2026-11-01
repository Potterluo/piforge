package httpapi

// The clone bound (adversarial acceptance #4, attack 10): the 4 MB read cap runs
// after the clone, so a repository was written to the control plane's /tmp in
// full before the import was refused — 38 829 857 B for a 32 MiB repository,
// 268 800 878 B for a 268 MiB one. The clone is now bounded while it runs.

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/urlguard"
)

func TestSkillImportRefusesACloneOverTheCap(t *testing.T) {
	env := newTestEnvWithSkills(t)
	env.deps.SkillCloneMaxBytes = 256 << 10
	env.rebuild()
	user := env.mkUser("clone-cap@piforge.local", "pw", contracts.RoleUser)
	c := env.login("clone-cap@piforge.local", "pw")

	// One skill, one megabyte of payload: well inside the 4 MB *read* cap, and
	// far outside this test's 256 KiB clone cap.
	repo := skillRepo(t, map[string]string{
		"big/SKILL.md":     "# Big\n\nA skill that is too big to clone.\n",
		"big/payload.txt":  strings.Repeat("x", 1<<20),
		"big/payload2.txt": strings.Repeat("y", 1<<20),
	})

	rec := env.doAuthed(http.MethodPost, "/v1/skills/import",
		map[string]any{"url": repoURL(repo)}, c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "克隆上限") {
		t.Fatalf("refusal does not name the clone cap: %s", rec.Body.String())
	}
	// The number the operator needs: what was measured, not "too big".
	if !strings.Contains(rec.Body.String(), "KiB") && !strings.Contains(rec.Body.String(), "MiB") {
		t.Fatalf("refusal does not report the bytes measured: %s", rec.Body.String())
	}
	assertNothingImported(t, env, user.ID)

	// The staging directory is gone: refusing must not leave the bytes behind.
	leftovers, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	for _, entry := range leftovers {
		if strings.HasPrefix(entry.Name(), "piforge-skills-") {
			// The handler removes its own directory on the way out; a directory
			// that is still here either belongs to another test running in
			// parallel (none do) or was left by the refused clone.
			t.Fatalf("the refused clone left %s behind", entry.Name())
		}
	}
}

// TestGitURLForPolicyChecksTheSameHost: the scp-style address is a different
// transport for the same question ("where does this host live"), so it is checked
// as an https URL of that host — which is what makes an internal git host refused
// and a public one allowed.
func TestGitURLForPolicyChecksTheSameHost(t *testing.T) {
	if got := gitURLForPolicy("git@github.com:acme/app.git"); got != "https://github.com/acme/app.git" {
		t.Fatalf("git@github.com:acme/app.git -> %q", got)
	}
	if got := gitURLForPolicy("https://github.com/acme/app.git"); got != "https://github.com/acme/app.git" {
		t.Fatalf("an https URL was rewritten to %q", got)
	}
	if got := gitURLForPolicy("file:///data/repo"); got != "file:///data/repo" {
		t.Fatalf("a file:// URL was rewritten to %q", got)
	}
	// The host rules apply to the ssh shape: an internal name is refused by the
	// same policy the http shape uses.
	if _, err := urlguard.Default().CheckSave(t.Context(), gitURLForPolicy("git@piforge:acme/app.git")); err == nil {
		t.Fatal("git@piforge:acme/app.git passed the host policy")
	}
}

// TestSkillImportStillWorksUnderTheDefaultCap keeps the bound honest: the
// ordinary repository is not affected by it.
func TestSkillImportStillWorksUnderTheDefaultCap(t *testing.T) {
	env := newTestEnvWithSkills(t)
	env.mkUser("clone-ok@piforge.local", "pw", contracts.RoleUser)
	c := env.login("clone-ok@piforge.local", "pw")

	repo := skillRepo(t, map[string]string{
		"pdf/SKILL.md":    "# PDF\n\nWork with PDFs.\n",
		"pdf/notes.txt":   "hello",
		"other/README.md": "not a skill",
	})
	rec := env.doAuthed(http.MethodPost, "/v1/skills/import",
		map[string]any{"url": repoURL(repo)}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"name":"pdf"`) {
		t.Fatalf("the skill was not imported: %s", rec.Body.String())
	}
}
