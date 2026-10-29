package term

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/aio"
)

// Optional live check against a running AIO Sandbox. It is skipped unless the
// endpoint is exported, because the image is 2 GB and not every machine has it.
//
//	PIFORGE_AIO_URL=http://127.0.0.1:18081 PIFORGE_AIO_KEY=... go test ./internal/term -run TestLiveRemoteSession -v
func TestLiveRemoteSession(t *testing.T) {
	base := os.Getenv("PIFORGE_AIO_URL")
	if base == "" {
		t.Skip("PIFORGE_AIO_URL is not set")
	}
	client, err := aio.New(aio.Config{BaseURL: base, APIKey: os.Getenv("PIFORGE_AIO_KEY")})
	if err != nil {
		t.Fatalf("aio.New: %v", err)
	}
	ctx := context.Background()

	// Drive the production path: Manager.Open resolves the remote hook, creates
	// the container shell, and the rest is the same ring/Read/Write surface the
	// WebSocket relay uses.
	m := New(Config{
		RingBytes: 4 << 20,
		Remote: func(context.Context, string) (*aio.Client, string, string, error) {
			// A fresh id per run: the image reuses an existing session for a
			// repeated create, and its accumulated output would confuse the
			// assertion.
			sessionID := fmt.Sprintf("pf-term-live-%d", time.Now().UnixNano())
			return client, sessionID, client.Root, nil
		},
	})
	defer m.CloseAll()

	info, err := m.Open(ctx, "live-worktree", "tab-live", "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if info.Shell != "aio" {
		t.Fatalf("shell = %q, want the remote session", info.Shell)
	}

	if err := m.Write("tab-live", []byte("echo remote-terminal-ok\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	deadline := time.Now().Add(20 * time.Second)
	var collected strings.Builder
	var next int64
	for time.Now().Before(deadline) {
		data, n, eof := m.Read("tab-live", next)
		if len(data) > 0 {
			collected.Write(data)
			next = n
		}
		// The local echo already contains the command text, so one occurrence
		// only proves that typing worked. Two prove the container ran it.
		if strings.Count(collected.String(), "remote-terminal-ok") >= 2 {
			t.Logf("terminal output: %q", collected.String())
			if err := m.Close("tab-live"); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if m.Len() != 0 {
				t.Fatalf("live sessions = %d after Close", m.Len())
			}
			return
		}
		if eof {
			t.Fatalf("the session ended before the echo appeared: %q", collected.String())
		}
		select {
		case <-m.Wake("tab-live"):
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("the echo never appeared; output = %q", collected.String())
}
