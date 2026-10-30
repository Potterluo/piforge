package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitIn runs git with a throwaway identity so a test repo never reads the
// machine's global config.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+filepath.Join(dir, ".gitconfig"), "GIT_CONFIG_SYSTEM=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// FR-12.6: the preview answers "what is on this branch and not on its base"
// with git's own commit list and a three-dot diff — including a rename, which
// is where a naive name-only parser loses the file.
func TestPRRangeDescribesTheBranch(t *testing.T) {
	bare, _ := remoteFixture(t)
	work := filepath.Join(t.TempDir(), "work")
	gitIn(t, filepath.Dir(work), "clone", bare, work)

	// Grow the README on the base first, so the branch's rename is similar
	// enough for git to detect it as a rename rather than delete+add.
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "readme line " + string(rune('a'+i%26))
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "-m", "grow the readme")
	gitIn(t, work, "push", "origin", "main")

	gitIn(t, work, "checkout", "-b", "piforge/feature")
	if err := os.WriteFile(filepath.Join(work, "feature.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "-m", "add the feature")

	gitIn(t, work, "mv", "README.md", "docs.md")
	appended := []byte(strings.Join(lines, "\n") + "\nand one more line\n")
	if err := os.WriteFile(filepath.Join(work, "docs.md"), appended, 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "-m", "rename the readme")

	runner := NewRunner()
	out, err := runner.PRRange(context.Background(), work, "main")
	if err != nil {
		t.Fatalf("PRRange: %v", err)
	}
	if out.Base != "main" || out.Head != "piforge/feature" {
		t.Fatalf("range = %s..%s", out.Base, out.Head)
	}
	if out.HeadSHA == "" {
		t.Fatal("no head SHA")
	}
	if out.CommitCount != 2 {
		t.Fatalf("commit count = %d, want 2", out.CommitCount)
	}
	if len(out.Commits) != 2 || out.Commits[0].Subject != "rename the readme" {
		t.Fatalf("commits = %+v", out.Commits)
	}

	byPath := map[string]struct {
		status   string
		add, del int
	}{}
	for _, f := range out.Files {
		byPath[f.Path] = struct {
			status   string
			add, del int
		}{f.Status, f.Additions, f.Deletions}
	}
	feature, ok := byPath["feature.txt"]
	if !ok || feature.status != "added" || feature.add != 2 || feature.del != 0 {
		t.Fatalf("feature.txt = %+v (all: %+v)", feature, byPath)
	}
	docs, ok := byPath["docs.md"]
	if !ok || docs.status != "renamed" || docs.add != 1 {
		t.Fatalf("docs.md = %+v (all: %+v)", docs, byPath)
	}
	if _, gone := byPath["README.md"]; gone {
		t.Fatalf("the old path leaked into the file list: %+v", byPath)
	}
	if out.Additions != 3 || out.Deletions != 0 {
		t.Fatalf("totals = +%d/-%d", out.Additions, out.Deletions)
	}
}

// A branch that is behind its base must not describe the base's own commits as
// reverse changes: the PR is what the branch adds, not the delta of two tips.
func TestPRRangeUsesTheMergeBase(t *testing.T) {
	bare, seed := remoteFixture(t)
	work := filepath.Join(t.TempDir(), "work")
	gitIn(t, filepath.Dir(work), "clone", bare, work)

	gitIn(t, work, "checkout", "-b", "piforge/feature")
	if err := os.WriteFile(filepath.Join(work, "feature.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "-m", "only my commit")

	// The base moves on in another checkout.
	if err := os.WriteFile(filepath.Join(seed, "upstream.txt"), []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, seed, "add", "-A")
	gitIn(t, seed, "commit", "-m", "upstream moved")
	gitIn(t, seed, "push", "origin", "main")

	runner := NewRunner()
	out, err := runner.PRRange(context.Background(), work, "origin/main")
	if err != nil {
		t.Fatalf("PRRange: %v", err)
	}
	if out.CommitCount != 1 {
		t.Fatalf("commit count = %d, want only the branch commit", out.CommitCount)
	}
	if len(out.Files) != 1 || out.Files[0].Path != "feature.txt" {
		t.Fatalf("files = %+v, want only feature.txt", out.Files)
	}
}

func TestPRRangeNamesAMissingBase(t *testing.T) {
	bare, _ := remoteFixture(t)
	work := filepath.Join(t.TempDir(), "work")
	gitIn(t, filepath.Dir(work), "clone", bare, work)

	_, err := NewRunner().PRRange(context.Background(), work, "no-such-branch")
	if !errors.Is(err, ErrBaseMissing) {
		t.Fatalf("error = %v, want ErrBaseMissing", err)
	}
	if !strings.Contains(err.Error(), "no-such-branch") {
		t.Fatalf("the refusal does not name the missing base: %v", err)
	}
}

// In an ordinary clone only the checked-out branch is local; the PR base lives
// as origin/main. A preview that insists on a local `main` refuses a case that
// is the normal one (the deployed app hit exactly this: 400 on the first click).
func TestPRRangeResolvesTheBaseFromTheRemote(t *testing.T) {
	bare, _ := remoteFixture(t)
	first := filepath.Join(t.TempDir(), "first")
	gitIn(t, filepath.Dir(first), "clone", bare, first)
	gitIn(t, first, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(first, "feature.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, first, "add", "-A")
	gitIn(t, first, "commit", "-m", "the branch commit")
	gitIn(t, first, "push", "-u", "origin", "feature")

	// Clone only the feature branch: `main` is not a local ref here.
	work := filepath.Join(t.TempDir(), "work")
	gitIn(t, filepath.Dir(work), "clone", "-b", "feature", bare, work)
	if _, err := os.Stat(filepath.Join(work, ".git", "refs", "heads", "main")); err == nil {
		t.Fatal("the fixture has a local main; the test would prove nothing")
	}

	out, err := NewRunner().PRRange(context.Background(), work, "main")
	if err != nil {
		t.Fatalf("PRRange: %v", err)
	}
	if out.Base != "main" {
		t.Fatalf("base = %q, want the requested name, not the tracking ref", out.Base)
	}
	if out.CommitCount != 1 || len(out.Files) != 1 || out.Files[0].Path != "feature.txt" {
		t.Fatalf("range = %+v", out)
	}
}
