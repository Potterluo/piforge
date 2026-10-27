package pitest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testTimeout bounds every wait so a protocol regression fails instead of
// hanging the suite.
const testTimeout = 15 * time.Second

// peer drives one in-process Serve over a duplex pipe.
type peer struct {
	conn io.ReadWriteCloser
	msgs chan map[string]any
	done chan struct{}
}

// startPeer runs Serve on one end of a pipe and decodes every record the peer
// writes on the other.
func startPeer(t *testing.T, script string, delay time.Duration, sessionFile string) *peer {
	t.Helper()
	ours, theirs := NewPipe()
	served := make(chan struct{})
	go func() {
		defer close(served)
		defer theirs.Close()
		if err := Serve(script, delay, sessionFile, theirs, theirs); err != nil {
			t.Logf("Serve returned: %v", err)
		}
	}()

	p := &peer{conn: ours, msgs: make(chan map[string]any, 4096), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		br := bufio.NewReader(ours)
		for {
			line, err := br.ReadBytes('\n')
			if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
				var m map[string]any
				if json.Unmarshal(trimmed, &m) == nil {
					p.msgs <- m
				}
			}
			if err != nil {
				return
			}
		}
	}()

	t.Cleanup(func() {
		_ = ours.Close()
		select {
		case <-served:
		case <-time.After(testTimeout):
			t.Errorf("Serve did not return after its stdin closed")
		}
	})
	return p
}

// send writes one command record.
func (p *peer) send(t *testing.T, cmd map[string]any) {
	t.Helper()
	p.sendRaw(t, marshal(t, cmd))
}

// sendRaw writes already-encoded bytes, which is how a record containing a raw
// U+2028 can be produced (encoding/json would escape it).
func (p *peer) sendRaw(t *testing.T, raw string) {
	t.Helper()
	if _, err := io.WriteString(p.conn, raw+"\n"); err != nil {
		t.Fatalf("write command: %v", err)
	}
}

// closeWrite half-closes stdin, which is the "shut down cleanly" signal.
func (p *peer) closeWrite(t *testing.T) {
	t.Helper()
	cw, ok := p.conn.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("transport does not support CloseWrite")
	}
	if err := cw.CloseWrite(); err != nil {
		t.Fatalf("close write: %v", err)
	}
}

// next returns the next record or fails.
func (p *peer) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case m := <-p.msgs:
		return m
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for a record")
		return nil
	}
}

// until reads records until match is satisfied and returns everything it saw,
// the match included.
func (p *peer) until(t *testing.T, match func(map[string]any) bool) []map[string]any {
	t.Helper()
	var out []map[string]any
	deadline := time.After(testTimeout)
	for {
		select {
		case m := <-p.msgs:
			out = append(out, m)
			if match(m) {
				return out
			}
		case <-deadline:
			t.Fatalf("timed out after %d records waiting for the expected one: %v", len(out), types(out))
		}
	}
}

// response waits for the response with the given id.
func (p *peer) response(t *testing.T, id string) map[string]any {
	t.Helper()
	records := p.until(t, func(m map[string]any) bool {
		return m["type"] == "response" && m["id"] == id
	})
	return records[len(records)-1]
}

func types(records []map[string]any) []string {
	out := make([]string, len(records))
	for i, m := range records {
		out[i], _ = m["type"].(string)
	}
	return out
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func arr(v any) []any {
	a, _ := v.([]any)
	return a
}

// readSessionFile returns the entries of a JSONL session file, skipping the
// header line.
func readSessionFile(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open session file: %v", err)
	}
	defer f.Close()
	var out []map[string]any
	br := bufio.NewReader(f)
	for {
		line, err := br.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var m map[string]any
			if json.Unmarshal(trimmed, &m) == nil && m["type"] != "session" {
				out = append(out, m)
			}
		}
		if err != nil {
			return out
		}
	}
}

// TestServeFullRun walks one prompt from agent_start to agent_settled and
// checks the intermediate records Pi's consumers depend on.
func TestServeFullRun(t *testing.T) {
	p := startPeer(t, ScriptOK, 0, "")
	p.send(t, map[string]any{"id": "1", "type": "prompt", "message": "hi"})

	records := p.until(t, func(m map[string]any) bool {
		return m["type"] == "agent_end"
	})

	resp := obj(records[0]["data"])
	if records[0]["type"] != "response" || records[0]["id"] != "1" {
		t.Fatalf("first record = %v, want the prompt response", records[0])
	}
	if resp["disposition"] != "started" {
		t.Fatalf("disposition = %v, want started", resp["disposition"])
	}

	got := types(records)
	for _, want := range []string{
		"agent_start", "turn_start", "message_start", "message_update",
		"message_end", "tool_execution_start", "tool_execution_update",
		"tool_execution_end", "turn_end", "agent_end",
	} {
		found := false
		for _, typ := range got {
			if typ == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no %s record in %v", want, got)
		}
	}

	// agent_settled must still follow, and only then is the run complete.
	rest := p.until(t, func(m map[string]any) bool { return m["type"] == "agent_settled" })
	if len(rest) != 1 {
		t.Fatalf("records between agent_end and agent_settled = %v", types(rest))
	}
	if aborted, _ := rest[0]["aborted"].(bool); aborted {
		t.Fatalf("a normal run reported aborted:true")
	}
}

// TestServeStreamingBehaviorRequired mirrors Pi's refusal to accept a prompt
// into a streaming agent without a delivery instruction.
func TestServeStreamingBehaviorRequired(t *testing.T) {
	p := startPeer(t, ScriptSlow, 0, "")
	p.send(t, map[string]any{"id": "1", "type": "prompt", "message": "long"})
	p.response(t, "1")

	p.send(t, map[string]any{"id": "2", "type": "prompt", "message": "second"})
	rejected := p.response(t, "2")
	if success, _ := rejected["success"].(bool); success {
		t.Fatalf("a second prompt without streamingBehavior was accepted: %v", rejected)
	}
	if msg, _ := rejected["error"].(string); !strings.Contains(msg, "streamingBehavior") {
		t.Errorf("error = %q, want a streamingBehavior complaint", msg)
	}

	p.send(t, map[string]any{"id": "3", "type": "prompt", "message": "queued", "streamingBehavior": "followUp"})
	queued := p.response(t, "3")
	if success, _ := queued["success"].(bool); !success {
		t.Fatalf("follow-up prompt rejected: %v", queued)
	}
	if got := obj(queued["data"])["disposition"]; got != "queued" {
		t.Fatalf("disposition = %v, want queued", got)
	}

	p.send(t, map[string]any{"id": "4", "type": "abort"})
	p.until(t, func(m map[string]any) bool { return m["type"] == "agent_settled" })
}

// TestServeAbortMidRun checks the abort path produces agent_settled{aborted}
// and leaves the peer usable.
func TestServeAbortMidRun(t *testing.T) {
	p := startPeer(t, ScriptSlow, 0, "")
	p.send(t, map[string]any{"id": "1", "type": "prompt", "message": "long"})
	p.until(t, func(m map[string]any) bool { return m["type"] == "message_update" })

	p.send(t, map[string]any{"id": "2", "type": "abort"})

	rest := p.until(t, func(m map[string]any) bool { return m["type"] == "agent_settled" })
	if aborted, _ := rest[len(rest)-1]["aborted"].(bool); !aborted {
		t.Fatalf("agent_settled after abort = %v, want aborted:true", rest[len(rest)-1])
	}
	found := false
	for _, m := range rest {
		if m["type"] == "response" && m["id"] == "2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("abort was never answered; records = %v", types(rest))
	}

	p.send(t, map[string]any{"id": "3", "type": "prompt", "message": "again"})
	resp := p.response(t, "3")
	if success, _ := resp["success"].(bool); !success {
		t.Fatalf("prompt after abort rejected: %v", resp)
	}
	p.until(t, func(m map[string]any) bool { return m["type"] == "agent_settled" })
}

// TestServeSplitsOnlyOnLF feeds a command containing a raw U+2028 inside a JSON
// string. A reader that splits on it would truncate the command, so the echoed
// user message is the proof that framing is LF-only.
func TestServeSplitsOnlyOnLF(t *testing.T) {
	p := startPeer(t, ScriptOK, 0, "")
	const text = "before\u2028after\u2029end"
	// Build the record by hand: encoding/json escapes U+2028, and the point is
	// to put the raw character on the wire.
	p.sendRaw(t, `{"id":"1","type":"prompt","message":"`+text+`"}`)
	p.response(t, "1")

	records := p.until(t, func(m map[string]any) bool {
		return m["type"] == "message_end" && obj(m["message"])["role"] == "user"
	})
	last := records[len(records)-1]
	msg := obj(last["message"])
	if got, _ := msg["content"].(string); got != text {
		t.Fatalf("echoed user message = %q, want %q", got, text)
	}
	// Let the run finish so teardown is not reported as a write error.
	p.until(t, func(m map[string]any) bool { return m["type"] == "agent_settled" })
}

// TestServeForkWritesTrimmedSessionFile pins the fork contract: the branch file
// holds only the entries up to the fork point and the source file is untouched.
func TestServeForkWritesTrimmedSessionFile(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "main.jsonl")
	branchFile := filepath.Join(dir, "branch.jsonl")

	p := startPeer(t, ScriptOK, 0, mainFile)
	p.send(t, map[string]any{"id": "1", "type": "prompt", "message": "first task"})
	p.until(t, func(m map[string]any) bool { return m["type"] == "agent_settled" })

	p.send(t, map[string]any{"id": "2", "type": "get_tree"})
	tree := obj(p.response(t, "2")["data"])
	roots := arr(tree["tree"])
	if len(roots) != 1 {
		t.Fatalf("tree has %d roots, want 1: %v", len(roots), tree)
	}
	userEntryID := obj(obj(roots[0])["entry"])["id"].(string)

	p.send(t, map[string]any{"id": "3", "type": "fork", "entryId": userEntryID, "sessionPath": branchFile})
	forked := p.response(t, "3")
	if success, _ := forked["success"].(bool); !success {
		t.Fatalf("fork failed: %v", forked)
	}

	branchEntries := readSessionFile(t, branchFile)
	if len(branchEntries) != 1 {
		t.Fatalf("branch file has %d entries, want 1: %v", len(branchEntries), branchEntries)
	}
	if branchEntries[0]["id"] != userEntryID {
		t.Errorf("branch entry = %v, want %s", branchEntries[0]["id"], userEntryID)
	}

	mainEntries := readSessionFile(t, mainFile)
	if len(mainEntries) != 3 {
		t.Fatalf("source file has %d entries after a fork, want the original 3", len(mainEntries))
	}

	// A process started on the branch file resumes exactly the fork point.
	restored := startPeer(t, ScriptOK, 0, branchFile)
	restored.send(t, map[string]any{"id": "4", "type": "get_tree"})
	restoredTree := obj(restored.response(t, "4")["data"])
	if got := len(arr(restoredTree["tree"])); got != 1 {
		t.Fatalf("restored branch has %d roots, want 1", got)
	}
}

// TestServeClosingStdinWaitsForRun is the documented shutdown behaviour: when
// stdin closes mid-run the peer finishes emitting the run before exiting, so a
// piped caller never loses the tail.
func TestServeClosingStdinWaitsForRun(t *testing.T) {
	p := startPeer(t, ScriptSlow, 0, "")
	p.send(t, map[string]any{"id": "1", "type": "prompt", "message": "long"})
	p.until(t, func(m map[string]any) bool { return m["type"] == "message_update" })

	p.closeWrite(t)

	records := p.until(t, func(m map[string]any) bool { return m["type"] == "agent_settled" })
	if len(records) == 0 {
		t.Fatal("no records after closing stdin")
	}
	if aborted, _ := records[len(records)-1]["aborted"].(bool); aborted {
		t.Errorf("closing stdin aborted the run instead of letting it finish")
	}
}

// TestServeMalformedCommandIsAnswered keeps one bad line from killing the peer.
func TestServeMalformedCommandIsAnswered(t *testing.T) {
	p := startPeer(t, ScriptOK, 0, "")
	p.sendRaw(t, `{"id":`)
	resp := p.until(t, func(m map[string]any) bool { return m["type"] == "response" })
	if success, _ := resp[0]["success"].(bool); success {
		t.Fatalf("malformed command was accepted: %v", resp[0])
	}
	// The peer must still work afterwards.
	p.send(t, map[string]any{"id": "2", "type": "get_state"})
	if success, _ := p.response(t, "2")["success"].(bool); !success {
		t.Fatal("peer stopped answering after a malformed command")
	}
}

// TestServeUnknownCommand checks an unsupported command is rejected politely.
func TestServeUnknownCommand(t *testing.T) {
	p := startPeer(t, ScriptOK, 0, "")
	p.send(t, map[string]any{"id": "1", "type": "definitely_not_a_pi_command"})
	resp := p.response(t, "1")
	if success, _ := resp["success"].(bool); success {
		t.Fatalf("unknown command accepted: %v", resp)
	}
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "unsupported") {
		t.Errorf("error = %q", msg)
	}
}

// TestServeCompaction checks the compaction records and the appended entry.
func TestServeCompaction(t *testing.T) {
	p := startPeer(t, ScriptOK, 0, "")
	p.send(t, map[string]any{"id": "1", "type": "compact", "customInstructions": "keep the tests"})
	resp := p.response(t, "1")
	if success, _ := resp["success"].(bool); !success {
		t.Fatalf("compact failed: %v", resp)
	}
	if got := obj(resp["data"])["summary"]; got != "keep the tests" {
		t.Errorf("summary = %v", got)
	}

	p.send(t, map[string]any{"id": "2", "type": "get_tree"})
	tree := obj(p.response(t, "2")["data"])
	roots := arr(tree["tree"])
	if len(roots) != 1 {
		t.Fatalf("compaction entry missing from the tree: %v", tree)
	}
}

// TestChunksAreRuneSafe guards the multi-byte split: a delta must never contain
// half a rune, because the assembled text would be corrupted.
func TestChunksAreRuneSafe(t *testing.T) {
	const text = "思考中🧠"
	for _, n := range []int{1, 2, 3, 5, 8} {
		var joined string
		for _, chunk := range chunks(text, n) {
			joined += chunk
			if !utf8Valid(chunk) {
				t.Fatalf("n=%d produced an invalid UTF-8 chunk %q", n, chunk)
			}
		}
		if joined != text {
			t.Fatalf("n=%d rejoined to %q, want %q", n, joined, text)
		}
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}
