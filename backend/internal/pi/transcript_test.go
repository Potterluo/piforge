package pi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// sessionJSONL writes a session file the way Pi does: one header line, then one
// entry per tree node.
func sessionJSONL(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "sessions", "ses_left.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"type":"session","version":3,"id":"ses_left.jsonl","timestamp":"2026-11-01T00:00:00Z","cwd":"/workspace/repo"}
{"type":"message","id":"e1","parentId":null,"timestamp":"2026-11-01T00:00:01Z","message":{"role":"user","content":"first question"}}
{"type":"message","id":"e2","parentId":"e1","timestamp":"2026-11-01T00:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"first answer"}]}}
{"type":"message","id":"e3","parentId":"e2","timestamp":"2026-11-01T00:00:03Z","message":{"role":"user","content":"second question"}}
{"type":"message","id":"e4","parentId":"e3","timestamp":"2026-11-01T00:00:04Z","message":{"role":"assistant","content":[{"type":"text","text":"second answer"}]}}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// TestEntriesReadTheTranscriptWhenNoAgentIsRunning is the defect from
// adversarial acceptance #2: after pause → resume with no new prompt the
// entries endpoint answered {"entries":[],"leaf_id":""} while the JSONL held
// both turns and /stats counted them. The endpoint must read the file.
func TestEntriesReadTheTranscriptWhenNoAgentIsRunning(t *testing.T) {
	dir := t.TempDir()
	path := sessionJSONL(t, dir)
	rt := NewRuntime(RuntimeConfig{
		TranscriptFor: func(_ context.Context, sessionID string) ([]byte, error) {
			if sessionID != "ses_left" {
				return nil, errors.New("unexpected session")
			}
			return os.ReadFile(path)
		},
	})
	t.Cleanup(func() { _ = rt.Close() })

	entries, leaf, err := rt.Entries(context.Background(), "ses_left", "")
	if err != nil {
		t.Fatalf("Entries without a live agent: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want the 4 turns on disk: %+v", len(entries), entries)
	}
	if leaf != "e4" {
		t.Fatalf("leaf = %q, want e4", leaf)
	}
	if entries[0].Role != "user" || entries[0].Preview != "first question" {
		t.Fatalf("first entry = %+v", entries[0])
	}
	if entries[3].Role != "assistant" || entries[3].Preview != "second answer" {
		t.Fatalf("last entry = %+v", entries[3])
	}
	if entries[1].ParentID != "e1" {
		t.Fatalf("parent link lost: %+v", entries[1])
	}
}

// A cursor keeps working offline: `?since=e2` is "what did I miss", and the
// answer is the rest of the file, not everything.
func TestEntriesFromTranscriptHonourSince(t *testing.T) {
	dir := t.TempDir()
	path := sessionJSONL(t, dir)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	entries, leaf, err := entriesFromJSONL(raw, "e2")
	if err != nil {
		t.Fatalf("entriesFromJSONL: %v", err)
	}
	if len(entries) != 2 || entries[0].EntryID != "e3" || entries[1].EntryID != "e4" {
		t.Fatalf("since=e2 = %+v", entries)
	}
	if leaf != "e4" {
		t.Fatalf("leaf = %q", leaf)
	}
	// A cursor the file no longer has (a torn or rewritten file) yields the
	// whole conversation: showing it twice is recoverable, showing it empty is
	// not.
	entries, _, err = entriesFromJSONL(raw, "e_gone")
	if err != nil {
		t.Fatalf("entriesFromJSONL: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("an unknown cursor = %d entries, want the whole file", len(entries))
	}
}

// An empty or header-only file is an empty conversation, not an error, and a
// malformed line does not take the rest of the transcript with it.
func TestEntriesFromTranscriptToleratesAColdOrTornFile(t *testing.T) {
	cases := map[string]string{
		"empty":       "",
		"header only": `{"type":"session","version":3,"id":"s.jsonl"}` + "\n",
		"torn tail": `{"type":"session","version":3,"id":"s.jsonl"}` + "\n" +
			`{"type":"message","id":"a","message":{"role":"user","content":"hi"}}` + "\n" +
			`{"type":"message","id":"b","mess`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			entries, leaf, err := entriesFromJSONL([]byte(raw), "")
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if entries == nil {
				t.Fatalf("entries is nil, want an empty slice for JSON []")
			}
			if name == "torn tail" && (len(entries) != 1 || leaf != "a") {
				t.Fatalf("torn tail = %+v leaf=%q", entries, leaf)
			}
			if name != "torn tail" && len(entries) != 0 {
				t.Fatalf("%s = %+v", name, entries)
			}
		})
	}
}

// Without a transcript hook the old behaviour stands: a session with no process
// is a not-found error the HTTP layer renders as an empty page. The fallback
// must not invent a successful empty answer where the file could not be read.
func TestEntriesWithoutATranscriptHookStillReportNoProcess(t *testing.T) {
	rt := NewRuntime(RuntimeConfig{})
	t.Cleanup(func() { _ = rt.Close() })
	_, _, err := rt.Entries(context.Background(), "ses_none", "")
	if err == nil {
		t.Fatal("Entries with no process and no transcript hook returned no error")
	}
	var apiErr *contracts.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != contracts.ErrNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}

	// And a hook that fails is not silently turned into an empty success.
	failing := NewRuntime(RuntimeConfig{
		TranscriptFor: func(context.Context, string) ([]byte, error) {
			return nil, errors.New("sandbox unreachable")
		},
	})
	t.Cleanup(func() { _ = failing.Close() })
	if _, _, err := failing.Entries(context.Background(), "ses_none", ""); err == nil {
		t.Fatal("a failing transcript read was reported as success")
	}
}
