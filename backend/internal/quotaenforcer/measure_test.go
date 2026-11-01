package quotaenforcer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The walk has three outcomes, and the middle one is the one that was missing:
// GNU du exits 1 when *some* entries were unreadable, which is what `du -sbx /`
// does for the sandbox uid on every reading (17 root-only directories in the
// shipped image, measured). Treating that as "no measurement" turned the
// whole-container term off in exactly the deployment the product ships, so the
// total was the workspace plus zero and /tmp had no budget at all.
func TestClassifyDuWalkOutcomes(t *testing.T) {
	// success
	bytes, err := classifyDu("/", []byte("108199\t/\n"), "", 0)
	if err != nil || bytes != 108199 {
		t.Fatalf("success = %d, %v; want 108199, nil", bytes, err)
	}

	// partial: exit 1, a real total, complaints on stderr
	var walk *WalkError
	bytes, err = classifyDu("/", []byte("8576239753\t/\n"),
		"du: cannot read directory '/root/x': Permission denied\ndu: cannot read directory '/var/lib/y': Permission denied\n", 1)
	if !errors.As(err, &walk) {
		t.Fatalf("partial walk error = %v, want a *WalkError", err)
	}
	if bytes != 8576239753 || walk.Unreadable != 2 || walk.Bytes != 8576239753 {
		t.Fatalf("partial = %d bytes, %d unreadable (%d carried); want 8576239753, 2, 8576239753",
			bytes, walk.Unreadable, walk.Bytes)
	}
	if !strings.Contains(walk.Error(), "2 entries could not be read") {
		t.Fatalf("partial error text = %q", walk.Error())
	}

	// a genuine failure is not usable at any price
	if _, err := classifyDu("/", []byte(""), "du: cannot access '/nope': No such file or directory\n", 1); err == nil || errors.As(err, &walk) {
		t.Fatalf("missing root = %v, want a plain error rather than a partial walk", err)
	}
	if _, err := classifyDu("/", []byte(""), "", 2); err == nil || errors.As(err, &walk) {
		t.Fatalf("exit 2 = %v, want a plain error", err)
	}
	if _, err := classifyDu("/", []byte(""), "", 0); err == nil {
		t.Fatal("empty output was accepted as a measurement")
	}
	if _, err := classifyDu("/", []byte("not-a-number\t/\n"), "", 0); err == nil {
		t.Fatal("a non-numeric total was accepted")
	}
}

// The second term is what makes the budget a disk budget: the container's
// writable layers outside the workspace (where /tmp and /home/gem live) are
// measured as the root filesystem minus the size it had at boot.
//
// The boot number has to be read defensively. A missing file, an empty one or
// garbage all mean "no baseline", and no baseline has to turn the term *off*:
// counting the image's own ~1.5 GB as tenant usage would pause every sandbox on
// its first measurement, which is a worse failure than the gap it closes.
func TestReadBaseline(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	if got := ReadBaseline(write("good", "1234567\n")); got != 1234567 {
		t.Fatalf("ReadBaseline(good) = %d, want 1234567", got)
	}
	for _, tc := range []struct{ name, body string }{
		{name: "empty", body: ""},
		{name: "garbage", body: "not a number\n"},
		{name: "negative", body: "-5\n"},
	} {
		if got := ReadBaseline(write(tc.name, tc.body)); got != 0 {
			t.Fatalf("ReadBaseline(%s) = %d, want 0 (no baseline)", tc.name, got)
		}
	}
	if got := ReadBaseline(filepath.Join(dir, "missing")); got != 0 {
		t.Fatalf("ReadBaseline(missing) = %d, want 0", got)
	}
	if got := ReadBaseline(""); got != 0 {
		t.Fatalf("ReadBaseline(empty path) = %d, want 0", got)
	}
}

// The defaults are part of the claim the docs make, so they are pinned here too.
func TestSecondaryMeasurementDefaults(t *testing.T) {
	if DefaultSysRoot != "/" {
		t.Fatalf("DefaultSysRoot = %q, want /", DefaultSysRoot)
	}
	if DefaultBaselineFile != "/run/piforge/disk-baseline" {
		t.Fatalf("DefaultBaselineFile = %q, want the path the image's hook writes", DefaultBaselineFile)
	}
	if DefaultSysInterval != time.Second {
		t.Fatalf("DefaultSysInterval = %s, want 1s", DefaultSysInterval)
	}
	if got := (Policy{}).sysInterval(); got != DefaultSysInterval {
		t.Fatalf("sysInterval() = %s, want %s", got, DefaultSysInterval)
	}
	if got := (Policy{SysInterval: 3 * time.Second}).sysInterval(); got != 3*time.Second {
		t.Fatalf("sysInterval(3s) = %s", got)
	}
}
