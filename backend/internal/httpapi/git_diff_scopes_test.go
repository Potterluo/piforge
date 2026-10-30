package httpapi

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/fsx"
	"github.com/piforge/piforge/backend/internal/gitx"
)

// runGitIn runs one git command in dir with an identity, so a test repo can
// commit without touching the machine's global config.
func runGitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// TestDiffScopesThroughTheRoutes is FR-12.1 / FR-12.2 from the diff view's own
// path: the browser asks GET /git/diff for a scope, then POSTs /git/hunk with
// the index of the hunk it is showing. The workspace here is the real gitx
// runner against a real repository, so the assertions are about content, not
// about a call being forwarded.
func TestDiffScopesThroughTheRoutes(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("scopes@piforge.local", "pw", contracts.RoleUser)
	c := env.login("scopes@piforge.local", "pw")
	project, session := env.mkSessionFor(user.ID, "scopes")
	// A checkout the test owns, plus the real git implementation behind it: the
	// fake workspace returns canned diffs, which cannot prove a range changed.
	checkout := attachScreenshotWorktree(t, env, user.ID, project, session)
	env.deps.Workspace = fsx.New(fsx.Config{Git: gitx.NewRunner()})
	env.rebuild()

	runGitIn(t, checkout, "init", "-b", "main")
	lines := make([]string, 24)
	for i := range lines {
		lines[i] = "line " + string(rune('a'+i))
	}
	writeLines := func() {
		t.Helper()
		if err := os.WriteFile(filepath.Join(checkout, "f.txt"),
			[]byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeLines()
	runGitIn(t, checkout, "add", "-A")
	runGitIn(t, checkout, "commit", "-m", "seed")

	// Two commits on top of the seed, each editing a different line.
	lines[1] = "line b FIRST"
	writeLines()
	runGitIn(t, checkout, "commit", "-am", "first")
	lines[20] = "line u SECOND"
	writeLines()
	runGitIn(t, checkout, "commit", "-am", "second")

	base := "/v1/sessions/" + session.ID
	diffBody := func(query string) []contracts.FileDiff {
		t.Helper()
		rec := env.doAuthed(http.MethodGet, base+"/git/diff?"+query, nil, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", query, rec.Code, rec.Body.String())
		}
		var body struct {
			Diffs []contracts.FileDiff `json:"diffs"`
		}
		env.mustJSON(rec, &body)
		return body.Diffs
	}

	// --- the range is content, not a label --------------------------------
	last := diffBody("scope=range&from=HEAD~1&to=HEAD")
	if len(last) != 1 {
		t.Fatalf("HEAD~1..HEAD files = %d", len(last))
	}
	if !strings.Contains(last[0].Patch, "line u SECOND") {
		t.Fatalf("HEAD~1..HEAD is missing the last commit:\n%s", last[0].Patch)
	}
	if strings.Contains(last[0].Patch, "line b FIRST") {
		t.Fatalf("HEAD~1..HEAD leaked the commit before it:\n%s", last[0].Patch)
	}

	two := diffBody("scope=range&from=HEAD~2&to=HEAD")
	if len(two) != 1 {
		t.Fatalf("HEAD~2..HEAD files = %d", len(two))
	}
	for _, want := range []string{"line b FIRST", "line u SECOND"} {
		if !strings.Contains(two[0].Patch, want) {
			t.Fatalf("HEAD~2..HEAD is missing %q:\n%s", want, two[0].Patch)
		}
	}

	// The same range scoped to a path that did not change in it is empty: the
	// path filter is applied inside the range, not before it.
	if files := diffBody("scope=range&from=HEAD~1&to=HEAD&path=nope.txt"); len(files) != 0 {
		t.Fatalf("a path outside the range returned %+v", files)
	}

	// A range without its left side is refused with the documented code, so a
	// client cannot silently get HEAD~1..HEAD instead of what it asked for.
	rec := env.doAuthed(http.MethodGet, base+"/git/diff?scope=range&to=HEAD", nil, c)
	env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)

	// --- per-hunk staging from the diff the view shows ---------------------
	lines[3] = "line d THIRD"
	lines[22] = "line w FOURTH"
	writeLines()

	unstaged := diffBody("scope=unstaged")
	if len(unstaged) != 1 {
		t.Fatalf("unstaged files = %d", len(unstaged))
	}
	hunks, err := gitx.Hunks(unstaged[0].Patch)
	if err != nil {
		t.Fatalf("Hunks: %v", err)
	}
	if len(hunks) != 2 {
		t.Fatalf("hunks = %d, want 2\n%s", len(hunks), unstaged[0].Patch)
	}
	// The index the panel draws must be the one the server applies: hunk 1 is
	// the later edit.
	if !strings.Contains(hunks[1].Patch, "line w FOURTH") {
		t.Fatalf("hunk 1 is not the later edit:\n%s", hunks[1].Patch)
	}

	stage := env.doAuthed(http.MethodPost, base+"/git/hunk",
		map[string]any{"path": "f.txt", "index": 1, "action": "stage"}, c)
	if stage.Code != http.StatusOK {
		t.Fatalf("stage hunk: %d %s", stage.Code, stage.Body.String())
	}

	staged := diffBody("scope=staged")
	if len(staged) != 1 {
		t.Fatalf("staged files = %d", len(staged))
	}
	if !strings.Contains(staged[0].Patch, "line w FOURTH") || strings.Contains(staged[0].Patch, "line d THIRD") {
		t.Fatalf("the index has the wrong hunk:\n%s", staged[0].Patch)
	}
	remaining := diffBody("scope=unstaged")
	if len(remaining) != 1 || !strings.Contains(remaining[0].Patch, "line d THIRD") ||
		strings.Contains(remaining[0].Patch, "line w FOURTH") {
		t.Fatalf("the other hunk did not stay in the worktree:\n%+v", remaining)
	}

	// Unstaging the hunk brings it back without touching the file.
	unstage := env.doAuthed(http.MethodPost, base+"/git/hunk",
		map[string]any{"path": "f.txt", "index": 0, "action": "unstage"}, c)
	if unstage.Code != http.StatusOK {
		t.Fatalf("unstage hunk: %d %s", unstage.Code, unstage.Body.String())
	}
	if files := diffBody("scope=staged"); len(files) != 0 {
		t.Fatalf("the index still has %+v", files)
	}
	if files := diffBody("scope=unstaged"); len(files) != 1 {
		t.Fatalf("the worktree lost the change: %+v", files)
	}
}
