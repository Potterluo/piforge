package httpapi

import (
	"os"
	"path/filepath"
	"testing"
)

// FR-14.x: importing a repository should find the skills it contains and take only the
// files under each skill's own directory. A repository is somebody else's code.
func TestFindSkillsWalksTheRepository(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("skills/pdf-tools/SKILL.md", "---\nname: PDF\n---\nRead and split PDFs.\n")
	write("skills/pdf-tools/scripts/split.py", "print('hi')\n")
	write("skills/review/SKILL.md", "# Review\nReview diffs carefully.\n")
	// Not a skill: a file that merely lives next to one.
	write("skills/README.md", "not a skill\n")
	write(".git/config", "[core]\n")

	found, err := findSkills(root, 1<<20)
	if err != nil {
		t.Fatalf("findSkills: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("found %d skills, want 2: %+v", len(found), found)
	}
	byName := map[string]skillCandidate{}
	for _, candidate := range found {
		byName[candidate.Name] = candidate
	}
	pdf, ok := byName["pdf-tools"]
	if !ok {
		t.Fatalf("pdf-tools missing: %+v", byName)
	}
	if pdf.Description != "Read and split PDFs." {
		t.Fatalf("description = %q", pdf.Description)
	}
	if len(pdf.Files) != 2 {
		t.Fatalf("files = %v", pdf.Files)
	}
	if _, ok := pdf.Files["scripts/split.py"]; !ok {
		t.Fatalf("the nested file is missing: %v", pdf.Files)
	}
	// The neighbour is not part of this skill.
	if _, ok := pdf.Files["README.md"]; ok {
		t.Fatal("a file outside the skill directory was imported with it")
	}
	// Git's own storage is never walked.
	if _, ok := byName["git"]; ok {
		t.Fatal(".git was treated as a skill")
	}
}

// A repository that is itself one skill: the manifest at the root.
func TestFindSkillsHandlesARepositoryThatIsOneSkill(t *testing.T) {
	root := filepath.Join(t.TempDir(), "my-skill")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("Does one thing.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	found, err := findSkills(root, 1<<20)
	if err != nil {
		t.Fatalf("findSkills: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("found %d", len(found))
	}
	if found[0].Name != "my-skill" {
		t.Fatalf("name = %q, want the repository's own name", found[0].Name)
	}
}

// The size cap has to stop a repository of binaries rather than fill the disk.
func TestFindSkillsRefusesAnOversizedSkill(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "big")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("big\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := findSkills(root, 1024); err == nil {
		t.Fatal("an oversized skill was accepted")
	}
}

func TestSlugSkill(t *testing.T) {
	cases := map[string]string{
		"PDF Tools":  "pdf-tools",
		"my_skill":   "my-skill",
		"  spaced  ": "spaced",
		"already-ok": "already-ok",
		"中文":         "skill",
		"a...b":      "a-b",
	}
	for in, want := range cases {
		if got := slugSkill(in); got != want {
			t.Fatalf("slugSkill(%q) = %q, want %q", in, got, want)
		}
	}
}
