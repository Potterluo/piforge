package pi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Session-file cwd alignment.
//
// Pi does not run its tools in the process working directory. It takes the
// directory from the *session file's header* (`SessionManager.getCwd()`), which
// is why spawning a process with `Dir` set to a branch's worktree was not enough:
//
//   - a fork is written by the parent's process, so its header carries the
//     *parent's* cwd (`<project>/repo`) and the branch's bash tool ran there, on
//     `main`, while the UI showed `piforge/<branch>` and its worktree;
//   - a clone copies the parent's file byte for byte, header included, so it had
//     exactly the same problem;
//   - a control-plane restart or a sandbox resume reopens the same file, so the
//     wrong cwd came back with it.
//
// The fix belongs here, at the one point every session process is created: before
// Pi opens the file, the header is made to describe the checkout the control
// plane is starting the process in. It is idempotent, so a correct file is left
// untouched and a resumed session is corrected at every spawn.

// sessionHeader is the first JSONL line Pi writes for a session. Only the two
// fields this package needs are decoded; everything else is preserved verbatim.
type sessionHeader struct {
	Type string `json:"type"`
	Cwd  string `json:"cwd"`
}

// alignSessionCwd rewrites the `cwd` of a session file's header to worktree and
// reports whether the file changed.
//
// A missing file is not an error: a brand-new session has none yet, and Pi
// creates it with the process's own cwd, which is already the worktree. A file
// whose first line is not a session header (a truncated write, a foreign file)
// is left alone rather than rewritten into something Pi cannot open.
func alignSessionCwd(path, worktree string) (bool, error) {
	if path == "" || worktree == "" {
		return false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read session file: %w", err)
	}
	next, changed, err := AlignSessionBytes(raw, worktree)
	if err != nil || !changed {
		return false, err
	}

	// Write-then-rename: a half-written session file would lose the conversation,
	// and the rename is atomic on both Linux and Windows.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".session-cwd-*")
	if err != nil {
		return false, fmt.Errorf("prepare session file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(next); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("write session file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("chmod session file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close session file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, fmt.Errorf("replace session file: %w", err)
	}
	return true, nil
}

// AlignSessionBytes is alignSessionCwd for a caller that owns the file through
// something other than this process's filesystem — a sandbox's file API, where
// the bytes must be replaced by the account that owns them.
//
// The rule is identical: only a file whose first line is a session header is
// touched, and only the `cwd` field of that line changes. It reports whether the
// bytes differ from what was passed in, so a caller can skip the write.
func AlignSessionBytes(raw []byte, worktree string) ([]byte, bool, error) {
	if len(raw) == 0 || worktree == "" {
		return raw, false, nil
	}
	first, rest, found := bytes.Cut(raw, []byte("\n"))
	if !found {
		// A single line without a newline is still a header Pi can open.
		rest = nil
	}
	var header sessionHeader
	if err := json.Unmarshal(bytes.TrimSpace(first), &header); err != nil {
		return raw, false, nil
	}
	if header.Type != "session" {
		return raw, false, nil
	}
	if header.Cwd == worktree {
		return raw, false, nil
	}

	// Re-encode only this line, keeping every other byte of the file intact:
	// the entries below it are the conversation, and round-tripping them through
	// a decoder would risk changing what the model reads.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(first), &fields); err != nil {
		return raw, false, nil
	}
	encoded, err := json.Marshal(worktree)
	if err != nil {
		return raw, false, fmt.Errorf("encode cwd: %w", err)
	}
	fields["cwd"] = encoded
	line, err := json.Marshal(fields)
	if err != nil {
		return raw, false, fmt.Errorf("encode session header: %w", err)
	}
	// json.Marshal sorts map keys, which is fine for JSON but loses the order Pi
	// wrote. Order is not part of the format; round-tripping the map keeps every
	// field, which is what matters.

	next := make([]byte, 0, len(line)+len(rest)+1)
	next = append(next, line...)
	next = append(next, '\n')
	next = append(next, rest...)
	return next, true, nil
}
