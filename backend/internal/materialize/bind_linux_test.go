//go:build linux

package materialize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealBindMountIsReadOnly is the Linux half of FR-14.2 that a Windows
// runner cannot execute. It is skipped when the kernel refuses the mount (an
// unprivileged container), because that is a property of the host, not a bug:
// the mount loop degrades to copy-ro and records it.
func TestRealBindMountIsReadOnly(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Review\nreal bind\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	target := filepath.Join(t.TempDir(), "mounted")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}

	if err := bindReadOnly(source, target); err != nil {
		t.Skipf("this host cannot bind mount (expected without CAP_SYS_ADMIN): %v", err)
	}
	t.Cleanup(func() { _ = unmountPath(target) })

	body, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if err != nil || !strings.Contains(string(body), "real bind") {
		t.Fatalf("mounted content = %q, %v", body, err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("hacked\n"), 0o644); err == nil {
		t.Fatal("a read-only bind mount accepted a write")
	}
	if body, err := os.ReadFile(filepath.Join(source, "SKILL.md")); err != nil || !strings.Contains(string(body), "real bind") {
		t.Fatalf("the source changed through the mount: %q, %v", body, err)
	}
	// A new file in the mounted directory is refused as well, which is the half
	// copy-ro cannot promise on Windows.
	if err := os.WriteFile(filepath.Join(target, "new.txt"), []byte("x\n"), 0o644); err == nil {
		t.Fatal("a read-only bind mount allowed a new file")
	}
}
