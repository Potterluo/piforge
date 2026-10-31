package runtimedirs

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEveryInventoryEntryHasALifecycle is the table-driven guard the task asks
// for: every directory the server creates under <dataDir> must name its creator,
// its owner, the request that deletes it, and what reclaims it when that request
// fails. A future directory added to the inventory without those answers fails
// here.
func TestEveryInventoryEntryHasALifecycle(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Inventory() {
		e := e
		t.Run(e.Name, func(t *testing.T) {
			if strings.TrimSpace(e.Name) == "" {
				t.Fatal("an inventory entry has no name")
			}
			if strings.Contains(e.Name, `\`) || filepath.IsAbs(e.Name) {
				t.Fatalf("the inventory key must be a slash-relative data-dir path, got %q", e.Name)
			}
			if seen[e.Name] {
				t.Fatalf("%q appears twice in the inventory", e.Name)
			}
			seen[e.Name] = true

			if e.Kind != KindDir && e.Kind != KindFile {
				t.Fatalf("unknown kind %q", e.Kind)
			}
			for field, value := range map[string]string{
				"Creator":    e.Creator,
				"Owner":      string(e.Owner),
				"DeletePath": e.DeletePath,
				"Fallback":   e.Fallback,
			} {
				if strings.TrimSpace(value) == "" {
					t.Fatalf("%s is empty: a path with no lifecycle is exactly the leak this table prevents", field)
				}
			}
			switch e.Owner {
			case OwnerDeployment, OwnerSession, OwnerProject, OwnerSandbox, OwnerSkill:
			default:
				t.Fatalf("unknown owner scope %q", e.Owner)
			}
			if e.Kind == KindDir && e.Owner != OwnerDeployment && e.Janitor == "" {
				t.Fatalf("directory %q has no janitor sweep: its eager delete path cannot be the only reclaim path", e.Name)
			}
			if e.Kind == KindFile && e.Janitor != "" {
				t.Fatalf("file %q claims a janitor sweep; files are not swept", e.Name)
			}
		})
	}
}

// dataDirLiteral matches filepath.Join(opt.dataDir, "name") and
// filepath.Join(dataDir, "name"), which is how every path in this table is
// built. A variable first argument (the dev binary) is skipped: the test is a
// guard for new literals, not a Go parser.
var dataDirLiteral = regexp.MustCompile(`filepath\.Join\((?:opt\.)?dataDir,\s*"([^"]+)"`)

// TestEveryDataDirLiteralIsInTheInventory closes the other half of the hole: the
// table could stay complete while a new directory is created with a fresh
// literal in main.go. It scans the backend sources for such literals and fails
// on any name the inventory does not know.
func TestEveryDataDirLiteralIsInTheInventory(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		// `go test` runs in this package's directory: backend/internal/runtimedirs,
		// so the module root is two levels up.
		t.Fatalf("cannot locate the backend module from %s: %v", root, err)
	}

	found := map[string]string{}
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for _, match := range dataDirLiteral.FindAllStringSubmatch(string(body), -1) {
			found[match[1]] = path
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", root, walkErr)
	}
	if len(found) == 0 {
		t.Fatal("the literal scan found nothing; it would pass for the wrong reason")
	}

	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := Lookup(name); !ok {
			t.Errorf("filepath.Join(dataDir, %q) in %s has no inventory entry: add its owner, delete path and fallback to internal/runtimedirs",
				name, found[name])
		}
	}
}

// The status endpoint and the janitor both rely on Dirs preserving the
// inventory's order and filtering files out.
func TestDirsExcludesDeploymentFilesAndKeepsOrder(t *testing.T) {
	dirs := Dirs()
	if len(dirs) == 0 {
		t.Fatal("no directory rows")
	}
	for i := 1; i < len(dirs); i++ {
		if dirs[i-1].Name > dirs[i].Name {
			t.Fatalf("Dirs is not sorted: %q before %q", dirs[i-1].Name, dirs[i].Name)
		}
	}
	for _, d := range dirs {
		if d.Kind != KindDir {
			t.Fatalf("%q is not a directory", d.Name)
		}
	}
	if _, ok := Lookup("piforge.db"); !ok {
		t.Fatal("the database must stay in the inventory: it is what the other paths are relative to")
	}
	if _, ok := Lookup("master.key"); !ok {
		t.Fatal("master.key must stay in the inventory: deleting it is a deployment decision, not a sweep")
	}
}

// Measure counts direct children and the recursive file total, and treats a
// missing root as zero rather than an error.
func TestMeasureCountsEntriesAndBytes(t *testing.T) {
	root := t.TempDir()
	one := filepath.Join(root, "ses_ONE")
	if err := os.MkdirAll(filepath.Join(one, "screenshots"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(one, "screenshots", "a.png"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(one, "models.json"), []byte("12"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "ses_TWO"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := Measure("artifacts", root, 0)
	if u.Entries != 2 || u.Files != 2 || u.Bytes != 7 {
		t.Fatalf("usage = %+v, want 2 entries, 2 files, 7 bytes", u)
	}
	if u.Truncated {
		t.Fatalf("a small tree was reported as truncated: %+v", u)
	}

	missing := Measure("artifacts", filepath.Join(root, "nope"), 0)
	if missing.Entries != 0 || missing.Files != 0 || missing.Bytes != 0 {
		t.Fatalf("missing root = %+v, want zeroes", missing)
	}
}

func TestMeasureTruncatesAtTheCap(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		name := filepath.Join(root, string(rune('a'+i))+".txt")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	u := Measure("artifacts", root, 2)
	if !u.Truncated {
		t.Fatalf("a capped walk did not report truncation: %+v", u)
	}
	if u.Files != 2 {
		t.Fatalf("files = %d, want the cap 2", u.Files)
	}
}
