package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// The transcript on disk, read without an agent.
//
// Pi keeps a session's whole conversation in one JSONL file — a header line plus
// one entry per tree node — and that file is the thing the user actually owns.
// The in-process bridge is a *view* of it: while an agent runs, get_entries
// answers from memory, which is faster and includes the message that is still
// streaming.
//
// A paused-and-resumed session has neither: resume restarts the container, no
// prompt has been sent yet, so there is no process and nothing in memory. The
// entries endpoint used to answer `{"entries":[],"leaf_id":""}` for it while
// /stats still counted two turns and the JSONL was intact (adversarial
// acceptance #2, 2026-11-01). The conversation is not lost — only the view is
// empty — so the fix is to read the file, which is where the transcript
// actually lives, instead of reporting that the user has no history.
//
// This file holds the reading side only: the caller supplies the bytes, because
// where the file lives depends on the placement (a host directory, or a path
// inside a sandbox that only the sandbox's API can open).

// transcriptReadTimeout bounds one transcript read. A file read is not a user
// action that deserves a long wait: a sandbox that cannot answer in this window
// is the sandbox watchdog's problem, not this request's.
const transcriptReadTimeout = 10 * time.Second

// entriesFromJSONL turns a session file into the entries endpoint's answer.
//
// The file's first line is a header (`{"type":"session",...}`) and is skipped;
// every other line is one entry, written by Pi in the same shape get_entries
// returns, so the same converter is used. A malformed line is skipped rather
// than fatal: half a conversation is better than a 500, and Pi itself tolerates
// a torn tail.
func entriesFromJSONL(raw []byte, since string) ([]*contracts.TreeEntry, string, error) {
	all := make([]entryWire, 0, 32)
	for _, line := range bytes.Split(raw, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		var entry entryWire
		if err := json.Unmarshal(trimmed, &entry); err != nil {
			continue
		}
		if entry.Type == "session" || entry.ID == "" {
			// The header, or a line this build does not model.
			continue
		}
		all = append(all, entry)
	}
	if len(all) == 0 {
		return []*contracts.TreeEntry{}, "", nil
	}

	leaf := all[len(all)-1].ID
	if since != "" {
		// get_entries' contract: everything appended after the id the client
		// last saw. A cursor the file no longer contains (a truncated or
		// rewritten file) means the client is further behind than the file can
		// prove, and the safe answer is the whole transcript — a reconnect that
		// shows the conversation twice is recoverable, one that shows it empty
		// is not.
		for i, entry := range all {
			if entry.ID == since {
				all = all[i+1:]
				break
			}
		}
	}

	out := make([]*contracts.TreeEntry, 0, len(all))
	for _, entry := range all {
		out = append(out, convertEntry(entry))
	}
	return out, leaf, nil
}

// transcriptEntries reads this session's file and converts it. The error is
// returned unchanged so the caller can decide whether an unreadable transcript
// is worth reporting or should keep the old "no live process" answer.
func (r *Runtime) transcriptEntries(ctx context.Context, sessionID, since string) ([]*contracts.TreeEntry, string, error) {
	readCtx, cancel := context.WithTimeout(ctx, transcriptReadTimeout)
	defer cancel()
	raw, err := r.cfg.TranscriptFor(readCtx, sessionID)
	if err != nil {
		return nil, "", fmt.Errorf("pi: read the transcript of %s: %w", sessionID, err)
	}
	return entriesFromJSONL(raw, since)
}
