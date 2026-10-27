package term

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// waitFor polls Read from offset 0 until marker shows up or the deadline
// expires. It returns everything read so far.
func waitFor(t *testing.T, m *Manager, tabID, marker string, timeout time.Duration) (string, int64) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var (
		collected strings.Builder
		next      int64
	)
	for time.Now().Before(deadline) {
		data, n, eof := m.Read(tabID, next)
		if len(data) > 0 {
			collected.Write(data)
			next = n
		}
		if strings.Contains(collected.String(), marker) {
			return collected.String(), next
		}
		if eof {
			t.Fatalf("session ended before %q appeared; got %q", marker, collected.String())
		}
		select {
		case <-m.Wake(tabID):
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("timeout waiting for %q; got %q", marker, collected.String())
	return "", 0
}

// TestEchoRoundTrip is the acceptance smoke test: open a shell, type a command,
// read the output back.
func TestEchoRoundTrip(t *testing.T) {
	m := New(Config{})
	defer m.CloseAll()

	ctx := context.Background()
	info, err := m.Open(ctx, "sbx_test", "tab-1", "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if info.PID <= 0 || info.Shell == "" {
		t.Fatalf("info = %+v", info)
	}
	if info.Cols != DefaultCols || info.Rows != DefaultRows {
		t.Errorf("size = %dx%d", info.Cols, info.Rows)
	}
	if err := m.Write("tab-1", []byte("echo piforge-ok\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, m, "tab-1", "piforge-ok", 15*time.Second)

	// Opening the same tab returns the live session rather than spawning one.
	again, err := m.Open(ctx, "sbx_test", "tab-1", "")
	if err != nil || again.PID != info.PID {
		t.Fatalf("reopen = %+v, %v", again, err)
	}
	if got := m.Len(); got != 1 {
		t.Errorf("live sessions = %d, want 1", got)
	}
}

// TestReadOffsetsAreStable asserts the replay arithmetic of PRD FR-10.3.
func TestReadOffsetsAreStable(t *testing.T) {
	m := New(Config{})
	defer m.CloseAll()
	if _, err := m.Open(context.Background(), "sbx", "tab-off", ""); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := m.Write("tab-off", []byte("echo offset-check\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, m, "tab-off", "offset-check", 15*time.Second)

	first, next, eof := m.Read("tab-off", 0)
	if len(first) == 0 || next != int64(len(first)) || eof {
		t.Fatalf("first read: len=%d next=%d eof=%v", len(first), next, eof)
	}
	second, next2, _ := m.Read("tab-off", 0)
	if string(first) != string(second) || next2 != next {
		t.Fatalf("replay from 0 differs: %q vs %q (%d vs %d)", first, second, next, next2)
	}
	// Continuing from next returns only new bytes (possibly none).
	rest, next3, _ := m.Read("tab-off", next)
	if next3 < next {
		t.Errorf("next moved backwards: %d < %d", next3, next)
	}
	_ = rest
	// Reading past the end is a no-op at the end offset.
	empty, end, _ := m.Read("tab-off", next3+1000)
	if len(empty) != 0 || end != next3 {
		t.Errorf("past-end read = %q %d", empty, end)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	m := New(Config{})
	if _, err := m.Open(context.Background(), "sbx", "tab-close", ""); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := m.Close("tab-close"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := m.Close("tab-close"); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if err := m.Close("never-existed"); err != nil {
		t.Errorf("close unknown: %v", err)
	}
	// Reading and writing a closed tab are safe and report eof/no session.
	if data, next, eof := m.Read("tab-close", 7); len(data) != 0 || next != 7 || !eof {
		t.Errorf("read closed = %q %d %v", data, next, eof)
	}
	if err := m.Write("tab-close", []byte("x")); !errors.Is(err, ErrNoSession) {
		t.Errorf("write closed = %v", err)
	}
	if err := m.Resize("tab-close", 10, 10); !errors.Is(err, ErrNoSession) {
		t.Errorf("resize closed = %v", err)
	}
	if m.Len() != 0 {
		t.Errorf("live sessions = %d, want 0", m.Len())
	}
}

func TestResize(t *testing.T) {
	m := New(Config{})
	defer m.CloseAll()
	if _, err := m.Open(context.Background(), "sbx", "tab-size", ""); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := m.Resize("tab-size", 80, 24); err != nil {
		t.Fatalf("resize: %v", err)
	}
	info, ok := m.Info("tab-size")
	if !ok || info.Cols != 80 || info.Rows != 24 {
		t.Fatalf("info = %+v ok=%v", info, ok)
	}
	// Zero/negative sizes are clamped, never forwarded as 0.
	if err := m.Resize("tab-size", 0, -5); err != nil {
		t.Fatalf("clamped resize: %v", err)
	}
	if info, _ := m.Info("tab-size"); info.Cols != 1 || info.Rows != 1 {
		t.Errorf("clamped = %+v", info)
	}
}

func TestWorkDirAndPrefs(t *testing.T) {
	dir := t.TempDir()
	m := New(Config{
		WorkDir: func(ctx context.Context, sandboxID string) (string, error) {
			if sandboxID != "sbx_dir" {
				t.Errorf("sandbox id = %q", sandboxID)
			}
			return dir, nil
		},
	})
	defer m.CloseAll()
	if _, err := m.Open(context.Background(), "sbx_dir", "tab-dir", ""); err != nil {
		t.Fatalf("open: %v", err)
	}
	// Print the working directory in a platform-appropriate way.
	cmd := "pwd\n"
	if runtime.GOOS == "windows" {
		cmd = "(Get-Location).Path\n"
	}
	if err := m.Write("tab-dir", []byte(cmd)); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, _ := waitFor(t, m, "tab-dir", filepath.Base(dir), 20*time.Second)
	if !strings.Contains(strings.ToLower(out), strings.ToLower(filepath.Base(dir))) {
		t.Errorf("workdir not applied: %q", out)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("workdir vanished: %v", err)
	}
}

func TestOpenRejectsBadKeys(t *testing.T) {
	m := New(Config{})
	defer m.CloseAll()
	if _, err := m.Open(context.Background(), "", "tab", ""); err == nil {
		t.Error("empty sandbox id should fail")
	}
	if _, err := m.Open(context.Background(), "sbx", "", ""); err == nil {
		t.Error("empty tab id should fail")
	}
	if _, err := m.Open(context.Background(), "sbx", "tab\x00", ""); err == nil {
		t.Error("NUL in tab id should fail")
	}
	// A preferred shell that does not exist falls back to the platform default.
	if _, err := m.Open(context.Background(), "sbx", "tab-fallback", "/definitely/not/a/shell"); err != nil {
		t.Errorf("fallback shell: %v", err)
	}
	info, _ := m.Info("tab-fallback")
	if !strings.Contains(strings.ToLower(info.Shell), "sh") && !strings.Contains(strings.ToLower(info.Shell), "powershell") && !strings.Contains(strings.ToLower(info.Shell), "cmd") {
		t.Errorf("resolved shell = %q", info.Shell)
	}
}

func TestSessionCapAndSandboxScoping(t *testing.T) {
	m := New(Config{MaxSessions: 2, RingBytes: MinRingBytes})
	defer m.CloseAll()
	ctx := context.Background()
	if _, err := m.Open(ctx, "sbx-a", "t1", ""); err != nil {
		t.Fatalf("open t1: %v", err)
	}
	if _, err := m.Open(ctx, "sbx-a", "t2", ""); err != nil {
		t.Fatalf("open t2: %v", err)
	}
	if _, err := m.Open(ctx, "sbx-b", "t3", ""); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("third session err = %v, want ErrTooManySessions", err)
	}
	if got := len(m.List("sbx-a")); got != 2 {
		t.Errorf("sbx-a sessions = %d", got)
	}
	if got := len(m.List("sbx-b")); got != 0 {
		t.Errorf("sbx-b sessions = %d", got)
	}
	if n := m.CloseSandbox("sbx-a"); n != 2 {
		t.Errorf("closed %d sessions, want 2", n)
	}
	if m.Len() != 0 {
		t.Errorf("live = %d", m.Len())
	}
}

func TestLossyStream(t *testing.T) {
	// A tiny ring plus a producer that outruns the reader must mark the stream
	// lossy instead of growing without bound (PRD §6.3-坑5).
	m := New(Config{RingBytes: MinRingBytes})
	defer m.CloseAll()
	if _, err := m.Open(context.Background(), "sbx", "tab-lossy", ""); err != nil {
		t.Fatalf("open: %v", err)
	}
	// Generate far more than MinRingBytes of output.
	cmd := "for i in $(seq 1 4000); do echo 0123456789012345678901234567890123456789; done\n"
	if runtime.GOOS == "windows" {
		cmd = "1..4000 | ForEach-Object { '0123456789012345678901234567890123456789' }\n"
	}
	if err := m.Write("tab-lossy", []byte(cmd)); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		info, _ := m.Info("tab-lossy")
		if info.Dropped > 0 {
			break
		}
		select {
		case <-m.Wake("tab-lossy"):
		case <-time.After(50 * time.Millisecond):
		}
	}
	if info, _ := m.Info("tab-lossy"); info.Dropped == 0 {
		t.Skipf("producer did not exceed the ring on this platform (end=%d)", info.End)
	}
	// Reading from 0 cannot return everything, and the flag says so.
	data, _, _ := m.Read("tab-lossy", 0)
	if len(data) == 0 {
		t.Fatal("no data at all")
	}
	info, _ := m.Info("tab-lossy")
	if !info.Lossy {
		t.Errorf("expected Lossy=true after reading a dropped offset: %+v", info)
	}
	start, end := info.Start, info.End
	if end-start > int64(MinRingBytes) {
		t.Errorf("window %d exceeds the ring size", end-start)
	}
	if info.Dropped <= 0 {
		t.Errorf("dropped = %d", info.Dropped)
	}
}

func TestManagerClosedRejectsOpen(t *testing.T) {
	m := New(Config{})
	if _, err := m.Open(context.Background(), "sbx", "t", ""); err != nil {
		t.Fatalf("open: %v", err)
	}
	m.CloseAll()
	if _, err := m.Open(context.Background(), "sbx", "t2", ""); !errors.Is(err, ErrClosed) {
		t.Errorf("open after CloseAll = %v", err)
	}
	if m.Len() != 0 {
		t.Errorf("live = %d", m.Len())
	}
}
