package quotaenforcer

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
