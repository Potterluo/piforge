package diskquota

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFile creates a file of n bytes below root (relative path included).
func writeFile(t *testing.T, root, rel string, n int) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// A sandbox under its budget is not stopped, and the report says what is there.
func TestMeasureReportsUnderTheQuota(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "repo/main.go", 4096)
	writeFile(t, root, "repo/docs/readme.md", 1024)

	g := New(Config{QuotaBytes: 1 << 20})
	rep := g.Measure(context.Background(), "sbx_under", root)
	if rep.Over {
		t.Fatalf("a sandbox of %d bytes was called over a 1 MiB quota: %+v", rep.Bytes, rep)
	}
	if rep.Bytes != 5120 {
		t.Fatalf("bytes = %d, want 5120", rep.Bytes)
	}
	if rep.Files != 2 {
		t.Fatalf("files = %d, want 2", rep.Files)
	}
	if rep.QuotaBytes != 1<<20 || rep.SandboxID != "sbx_under" {
		t.Fatalf("report does not describe the measurement: %+v", rep)
	}
}

// Over the budget is a decision, and the message a user sees names both numbers.
func TestMeasureFlagsASandboxOverTheQuota(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "repo/node_modules/big.bin", 8*1024)

	g := New(Config{QuotaBytes: 4 * 1024})
	rep := g.Measure(context.Background(), "sbx_over", root)
	if !rep.Over {
		t.Fatalf("8 KiB against a 4 KiB quota was not flagged over: %+v", rep)
	}
	message := g.Message(rep)
	for _, want := range []string{"8.0 KiB", "4.0 KiB", "配额", "运行已停止", "node_modules", "-sandbox-disk-quota"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message is missing %q:\n%s", want, message)
		}
	}
}

// A zero quota disables enforcement: the report still measures (that is how an
// operator sizes a budget), but nothing is ever refused.
func TestDisabledQuotaNeverFlagsASandbox(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "repo/huge.bin", 64*1024)

	g := New(Config{QuotaBytes: 0})
	if !g.Disabled() {
		t.Fatal("a zero quota must report itself disabled")
	}
	rep := g.Measure(context.Background(), "sbx_free", root)
	if rep.Over {
		t.Fatalf("a disabled quota flagged a sandbox: %+v", rep)
	}
	if rep.Bytes != 64*1024 {
		t.Fatalf("a disabled quota stopped measuring: %+v", rep)
	}
}

// The admin report reuses a measurement for the TTL and re-walks after it, so the
// page can poll without turning an idle screen into a disk scan.
func TestCachedReusesAMeasurementOnlyWithinTheTTL(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "repo/a.bin", 1024)

	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	g := New(Config{QuotaBytes: 1 << 20, ReportTTL: 30 * time.Second, Now: func() time.Time { return now }})
	first := g.Cached(context.Background(), "sbx_cache", root)
	if first.Bytes != 1024 {
		t.Fatalf("first measurement = %d", first.Bytes)
	}

	writeFile(t, root, "repo/b.bin", 2048)
	if again := g.Cached(context.Background(), "sbx_cache", root); again.Bytes != first.Bytes {
		t.Fatalf("the cached report was ignored inside the TTL: %d -> %d", first.Bytes, again.Bytes)
	}

	now = now.Add(31 * time.Second)
	if fresh := g.Cached(context.Background(), "sbx_cache", root); fresh.Bytes != 3072 {
		t.Fatalf("the cache was not refreshed after the TTL: %d", fresh.Bytes)
	}
}

// A sandbox whose directory was removed is zero bytes, not an error and not a
// refusal: the row may exist while the provider is still creating the checkout.
func TestMeasureOfAMissingRootIsEmpty(t *testing.T) {
	g := New(Config{QuotaBytes: 1})
	rep := g.Measure(context.Background(), "sbx_missing", filepath.Join(t.TempDir(), "nope"))
	if rep.Bytes != 0 || rep.Over {
		t.Fatalf("missing root = %+v", rep)
	}
}

// Forget makes the next Cached walk again — a destroyed and recreated sandbox
// must not inherit the old numbers.
func TestForgetDropsTheCachedReport(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "repo/a.bin", 1024)
	g := New(Config{QuotaBytes: 1 << 20})
	_ = g.Cached(context.Background(), "sbx_forget", root)
	writeFile(t, root, "repo/b.bin", 1024)
	g.Forget("sbx_forget")
	if got := g.Cached(context.Background(), "sbx_forget", root); got.Bytes != 2048 {
		t.Fatalf("Forget did not drop the cached report: %+v", got)
	}
}

// A nil guardian (a test server wired without the feature) is disabled, not a
// panic: every method has to survive it.
func TestNilGuardianIsDisabled(t *testing.T) {
	var g *Guardian
	if !g.Disabled() || g.Limit() != 0 {
		t.Fatal("a nil guardian must report itself disabled")
	}
	if rep := g.Measure(context.Background(), "sbx_nil", "/nowhere"); rep.Over {
		t.Fatalf("a nil guardian flagged a sandbox: %+v", rep)
	}
	g.Forget("sbx_nil")
}
