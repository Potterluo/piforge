package sourcedigest

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// blobID is git's object id for a byte string, spelled out here rather than
// borrowed from the implementation under test.
func blobID(content string) string {
	h := sha1.New()
	h.Write([]byte("blob " + itoa(len(content)) + "\x00"))
	h.Write([]byte(content))
	return hex.EncodeToString(h.Sum(nil))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// TestDigestHashesTheDocumentedText is the definition, not a restatement of the
// implementation: it builds the exact bytes the package documents and hashes
// them itself. If this changes, every image label in every deployment stops
// matching, so the change has to be deliberate (and would bump SaltPrefix).
func TestDigestHashesTheDocumentedText(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "alpha\n")
	write(t, root, "deploy/b.txt", "beta\n")

	// Deliberately unsorted and with a duplicate: the digest is a function of
	// the tree's content, not of the order a caller happened to list it in.
	got, err := DigestOf(root, []string{"deploy/b.txt", "a.txt", "a.txt"}, "Dockerfile", []string{"BASE=x"})
	if err != nil {
		t.Fatalf("DigestOf: %v", err)
	}
	rows := []string{
		"a.txt\x00" + blobID("alpha\n"),
		"deploy/b.txt\x00" + blobID("beta\n"),
	}
	text := "piforge-source-digest-v1\nDockerfile\nBASE=x\n" + strings.Join(rows, "\n")
	sum := sha256.Sum256([]byte(text))
	want := hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("digest = %s, want %s (the documented text changed)", got, want)
	}
}

// TestDigestPinsOneValue: a literal, so a refactor that keeps the shape but
// changes a byte (an extra newline, a different separator) is caught even if the
// test above is edited to match it.
func TestDigestPinsOneValue(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "alpha\n")
	got, err := DigestOf(root, []string{"a.txt"}, "Dockerfile", nil)
	if err != nil {
		t.Fatalf("DigestOf: %v", err)
	}
	// Cross-checked outside Go: `printf 'piforge-source-digest-v1\nDockerfile\n\na.txt\0<blob>' |
	// sha256sum`, with blob = `printf 'blob 6\0alpha\n' | sha1sum`.
	const want = "12df1ed4749c42424867cfa3de6f135a6527950b5dae00234ae59c4a3bbaab06"
	if got != want {
		t.Fatalf("digest = %s, want the pinned %s", got, want)
	}
}

// TestDigestNoticesContentArgsAndRecipe: the three inputs a build has, each on
// its own. A digest that missed one would reuse an image across a real change.
func TestDigestNoticesContentArgsAndRecipe(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "alpha\n")
	base, err := DigestOf(root, []string{"a.txt"}, "Dockerfile", nil)
	if err != nil {
		t.Fatalf("DigestOf: %v", err)
	}

	// Same content, different mtime: not a rebuild.
	write(t, root, "a.txt", "alpha\n")
	same, err := DigestOf(root, []string{"a.txt"}, "Dockerfile", nil)
	if err != nil {
		t.Fatalf("DigestOf: %v", err)
	}
	if same != base {
		t.Fatalf("rewriting identical bytes changed the digest: %s != %s", same, base)
	}

	write(t, root, "a.txt", "alpha!\n")
	changed, err := DigestOf(root, []string{"a.txt"}, "Dockerfile", nil)
	if err != nil {
		t.Fatalf("DigestOf: %v", err)
	}
	if changed == base {
		t.Fatal("a content change did not change the digest")
	}

	withArg, err := DigestOf(root, []string{"a.txt"}, "Dockerfile", []string{"BASE=x"})
	if err != nil {
		t.Fatalf("DigestOf: %v", err)
	}
	if withArg == changed {
		t.Fatal("a build argument did not change the digest")
	}

	otherRecipe, err := DigestOf(root, []string{"a.txt"}, "deploy/Dockerfile.runner", nil)
	if err != nil {
		t.Fatalf("DigestOf: %v", err)
	}
	if otherRecipe == changed {
		t.Fatal("the Dockerfile's name did not change the digest: two recipes would label identical images")
	}
}

// TestDigestSkipsAFileThatIsGone. `git ls-files` lists index entries that were
// deleted from the working tree; they are not part of what a build copies, so
// they must not be read (and must not be an error).
func TestDigestSkipsAFileThatIsGone(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "alpha\n")
	withGhost, err := DigestOf(root, []string{"a.txt", "gone.txt"}, "Dockerfile", nil)
	if err != nil {
		t.Fatalf("DigestOf: %v", err)
	}
	without, err := DigestOf(root, []string{"a.txt"}, "Dockerfile", nil)
	if err != nil {
		t.Fatalf("DigestOf: %v", err)
	}
	if withGhost != without {
		t.Fatalf("a deleted index entry changed the digest: %s != %s", withGhost, without)
	}
}

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this host")
	}
}

// TestSourceFilesReadsTheTreeThroughGit: tracked plus untracked-but-not-ignored,
// and nothing that git ignores (a build does not copy an ignored file either).
func TestSourceFilesReadsTheTreeThroughGit(t *testing.T) {
	gitAvailable(t)
	root := t.TempDir()
	write(t, root, "backend/keep.go", "package keep\n")
	write(t, root, "backend/new.go", "package keep\n")
	write(t, root, "backend/ignored.go", "package keep\n")
	write(t, root, "docs/not-an-input.md", "docs cannot change an image\n")
	write(t, root, ".gitignore", "backend/ignored.go\n")
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", ".gitignore", "backend/keep.go"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	files, err := SourceFiles(root)
	if err != nil {
		t.Fatalf("SourceFiles: %v", err)
	}
	want := []string{"backend/keep.go", "backend/new.go"}
	if strings.Join(files, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v (tracked + untracked, ignored and out-of-root excluded)", files, want)
	}
}

// TestSourceFilesFallsBackToTheRecipes: without git the digest must still be
// computable, or up-isolated.ps1 would have to guess from mtimes again.
func TestSourceFilesFallsBackToTheRecipes(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Dockerfile", "FROM scratch\n")
	write(t, root, "deploy/Dockerfile.sandbox-agent", "FROM scratch\n")
	t.Setenv("PATH", "") // no git to find

	files, err := SourceFiles(root)
	if err != nil {
		t.Fatalf("SourceFiles: %v", err)
	}
	want := []string{"Dockerfile", "deploy/Dockerfile.sandbox-agent"}
	if strings.Join(files, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v", files, want)
	}
	if _, err := Digest(root, "Dockerfile", nil); err != nil {
		t.Fatalf("Digest on the fallback: %v", err)
	}
}
