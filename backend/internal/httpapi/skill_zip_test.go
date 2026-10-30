package httpapi

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkzip builds an archive in memory. `mode` lets a test declare an entry as a
// symlink or a directory without also having to produce a real filesystem.
func mkzip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Store}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create %s: %v", entry.name, err)
		}
		if _, err := file.Write([]byte(entry.body)); err != nil {
			t.Fatalf("write %s: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

type zipEntry struct {
	name string
	body string
	mode os.FileMode
}

func readBody(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

// FR-14.4: a zip is unpacked and walked exactly like a checkout, so the same
// "one skill per directory with a SKILL.md" rule applies and only that directory's
// files survive.
func TestExtractSkillZipThenFindSkills(t *testing.T) {
	archive := mkzip(t, []zipEntry{
		{name: "collection/pdf-tools/SKILL.md", body: "Read and split PDFs.\n"},
		{name: "collection/pdf-tools/scripts/split.py", body: "print('split')\n"},
		{name: "collection/review/SKILL.md", body: "Review diffs.\n"},
		{name: "README.md", body: "not a skill\n"},
	})

	dir := t.TempDir()
	if err := extractSkillZip(archive, dir); err != nil {
		t.Fatalf("extractSkillZip: %v", err)
	}
	if got := readBody(t, filepath.Join(dir, "collection/pdf-tools/scripts/split.py")); got != "print('split')\n" {
		t.Fatalf("split.py = %q", got)
	}

	found, err := findSkills(dir, skillImportMaxBytes)
	if err != nil {
		t.Fatalf("findSkills: %v", err)
	}
	names := map[string]bool{}
	for _, candidate := range found {
		names[candidate.Name] = true
	}
	if !names["pdf-tools"] || !names["review"] || len(found) != 2 {
		t.Fatalf("found = %+v", found)
	}
	// The README beside the skills is not part of either bundle.
	for _, candidate := range found {
		for rel := range candidate.Files {
			if strings.Contains(rel, "..") || rel == "README.md" {
				t.Fatalf("%s took %q, which is outside its own directory", candidate.Name, rel)
			}
		}
	}
}

// A zip entry is attacker input: `../` must refuse the whole archive, not skip the
// entry and import the rest.
func TestExtractSkillZipRefusesTraversal(t *testing.T) {
	dir := t.TempDir()
	archive := mkzip(t, []zipEntry{
		{name: "../escape/SKILL.md", body: "escaped\n"},
		{name: "fine/SKILL.md", body: "fine\n"},
	})
	err := extractSkillZip(archive, dir)
	if err == nil {
		t.Fatal("a traversal entry was extracted")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(dir), "escape")); !os.IsNotExist(statErr) {
		t.Fatalf("a file was written outside the destination: %v", statErr)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("the archive was half extracted: %v", entries)
	}
}

func TestExtractSkillZipRefusesAbsoluteAndSymlinkEntries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry zipEntry
	}{
		{name: "absolute", entry: zipEntry{name: "/etc/passwd", body: "x"}},
		{name: "symlink", entry: zipEntry{name: "link", body: "x", mode: os.ModeSymlink | 0o777}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := extractSkillZip(mkzip(t, []zipEntry{tc.entry}), dir); err == nil {
				t.Fatalf("%s entry was extracted", tc.name)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Fatalf("wrote something for a refused entry: %v", entries)
			}
		})
	}
}

func TestExtractSkillZipRefusesAnOversizedEntry(t *testing.T) {
	dir := t.TempDir()
	archive := mkzip(t, []zipEntry{
		{name: "big/SKILL.md", body: strings.Repeat("x", maxSkillFileBytes+1)},
	})
	err := extractSkillZip(archive, dir)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("error = %v, want an oversized-file refusal", err)
	}
}

func TestExtractSkillZipRefusesSomethingThatIsNotAZip(t *testing.T) {
	if err := extractSkillZip([]byte("this is not a zip"), t.TempDir()); err == nil {
		t.Fatal("plain text was accepted as a zip")
	}
}
