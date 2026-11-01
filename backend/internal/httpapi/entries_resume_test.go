package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/pi"
)

// TestEntriesSurvivePauseAndResumeWithoutAPrompt is the display defect from
// adversarial acceptance #2, asserted end to end through the API.
//
// The measured state was: after `pause` then `resume` and no new prompt,
// `GET /v1/sessions/{id}/entries` answered {"entries":[],"leaf_id":""} while
// `/stats` still counted two user and two assistant messages and the sandbox's
// JSONL held both turns. Nothing was lost — the endpoint was answering from a
// process that resume does not start.
//
// This test uses the real pi.Runtime with a transcript hook pointing at a real
// session file, so the assertion is about the mechanism ("read where it lives"),
// not about a hand-written fake that could agree with the handler by accident.
func TestEntriesSurvivePauseAndResumeWithoutAPrompt(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	project, session := env.mkSessionFor(user.ID, "resumed")
	attachPauseSandbox(t, env, user.ID, project, session)

	// The transcript the sandbox would hold: two turns, four entries.
	dir := t.TempDir()
	file := filepath.Join(dir, session.ID+".jsonl")
	body := `{"type":"session","version":3,"id":"` + session.ID + `.jsonl","timestamp":"2026-11-01T00:00:00Z","cwd":"/workspace/repo"}
{"type":"message","id":"u1","parentId":null,"timestamp":"2026-11-01T00:00:01Z","message":{"role":"user","content":"add a test"}}
{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-11-01T00:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"added one"}]}}
{"type":"message","id":"u2","parentId":"a1","timestamp":"2026-11-01T00:00:03Z","message":{"role":"user","content":"run it"}}
{"type":"message","id":"a2","parentId":"u2","timestamp":"2026-11-01T00:00:04Z","message":{"role":"assistant","content":[{"type":"text","text":"it passes"}]}}
`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	// No live process is ever started: pause+resume starts the container, not
	// the agent, which is exactly the state that used to answer empty.
	runtime := pi.NewRuntime(pi.RuntimeConfig{
		TranscriptFor: func(context.Context, string) ([]byte, error) {
			return os.ReadFile(file)
		},
	})
	t.Cleanup(func() { _ = runtime.Close() })

	env.deps.Runtime = runtime
	env.deps.Sandbox = &recordPauseResume{}
	env.rebuild()
	client := env.login("dev@piforge.local", "pw")

	for _, route := range []string{"pause", "resume"} {
		rec := env.doAuthed("POST", "/v1/sessions/"+session.ID+"/sandbox/"+route, nil, client)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", route, rec.Code, rec.Body.String())
		}
	}

	rec := env.doAuthed(http.MethodGet, "/v1/sessions/"+session.ID+"/entries", nil, client)
	if rec.Code != http.StatusOK {
		t.Fatalf("entries = %d: %s", rec.Code, rec.Body.String())
	}
	var page struct {
		Entries []*contracts.TreeEntry `json:"entries"`
		LeafID  string                 `json:"leaf_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode entries: %v", err)
	}
	if len(page.Entries) != 4 {
		t.Fatalf("entries after pause+resume = %d, want the 4 on disk: %s", len(page.Entries), rec.Body.String())
	}
	if page.LeafID != "a2" {
		t.Fatalf("leaf_id = %q, want a2", page.LeafID)
	}
	if page.Entries[0].Role != "user" || page.Entries[0].Preview != "add a test" {
		t.Fatalf("first entry = %+v", page.Entries[0])
	}
	if page.Entries[3].Role != "assistant" || page.Entries[3].Preview != "it passes" {
		t.Fatalf("last entry = %+v", page.Entries[3])
	}
}
