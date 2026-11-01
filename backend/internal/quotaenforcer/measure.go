package quotaenforcer

import (
	"fmt"
	"strconv"
	"strings"
)

// WalkError is a walk that finished but could not read part of the tree.
//
// GNU du exits 1 when any entry was unreadable, and the enforcer runs as the
// sandbox uid (1000) — in the shipped image 17 directories belong to root and
// are mode 0700, so `du -sbx /` *always* exits 1 for it (measured 2026-11-01,
// docs/LINUX-VERIFY.md row L4). Treating that as "no measurement" is what turned
// the whole-container term off on every reading: /tmp had no budget at all, and
// the only trace was a log line repeated once a second that nobody read. The
// bytes the walk could read are still the number the term needs, because the
// boot baseline is measured by the same uid over the same tree and the term is
// the difference between two such walks. What must not happen is counting the
// walk as exact, so the number of unreadable entries travels with it and the
// caller says how much of the tree was invisible.
//
// A partial *workspace* walk is different and is refused (see Measure): the
// workspace is the tenant's own tree, and `chmod 000` on a directory hides its
// bytes from a walk by the same uid, so a partial number there is a number the
// tenant chose.
//
// It lives in a build-tag-free file because the caller that consumes it
// (Run, in quotaenforcer.go) is built for every platform, and the Windows
// development control plane has to compile.
type WalkError struct {
	// Root is the tree the walk ran over.
	Root string
	// Unreadable counts du's stderr lines, one per entry it could not read.
	Unreadable int
	// Bytes is the sum du reported for the part it could read.
	Bytes int64
}

func (e *WalkError) Error() string {
	return fmt.Sprintf("du -sbx %s: %d entries could not be read; %d bytes measured of the reachable tree",
		e.Root, e.Unreadable, e.Bytes)
}

// classifyDu turns one `du -sbx` result into the number and the meaning of the
// exit. It is a function of the raw result so the three outcomes can be tested
// without a filesystem, a uid or a shell: success (0), "some entries were
// unreadable but the total is real" (du's documented exit 1), and a genuine
// failure.
func classifyDu(root string, stdout []byte, stderr string, exitCode int) (int64, error) {
	fields := strings.Fields(string(stdout))
	total, parsed := int64(0), false
	if len(fields) >= 2 {
		if value, perr := strconv.ParseInt(fields[0], 10, 64); perr == nil {
			total, parsed = value, true
		}
	}
	if exitCode != 0 {
		if exitCode == 1 && parsed {
			// Exit 1 is "some entries were unreadable", not "nothing was
			// measured": du printed the total for the tree it could read.
			return total, &WalkError{Root: root, Unreadable: countLines(stderr), Bytes: total}
		}
		return 0, fmt.Errorf("du -sbx %s exited %d, want a byte total", root, exitCode)
	}
	if !parsed {
		if len(fields) == 0 {
			return 0, fmt.Errorf("du -sbx %s printed nothing, want a byte total", root)
		}
		return 0, fmt.Errorf("du -sbx %s printed %q, want a byte total", root, strings.TrimSpace(string(stdout)))
	}
	return total, nil
}

// countLines counts what du complained about. One line per unreadable entry is
// du's shape; the number is for the log, not for arithmetic.
func countLines(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}
