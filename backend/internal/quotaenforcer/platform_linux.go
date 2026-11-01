//go:build linux

package quotaenforcer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Measure walks the workspace and returns its apparent size in bytes — the same
// number the control plane's `find -printf '%s'` walk reports, and the same
// number `du -sb` prints, so the enforcer and the admin screen cannot disagree
// about how big a sandbox is.
//
// Apparent size rather than allocated blocks is deliberate: it counts a large
// sparse file as large, which is the direction a quota should err in, and it does
// not lag behind the writes the way the filesystem's free-block counter does
// (ext4 keeps a burst in the page cache and charges the bitmap on writeback).
//
// The walk is bounded by the caller's interval rather than by a timeout: if it
// takes long, NextInterval backs the loop off, which is cheaper than cancelling
// a half-finished du every tick.
func Measure(ctx context.Context, root string) (int64, error) {
	return duBytes(ctx, root)
}

// MeasureRoot measures the container's root filesystem, which is where /tmp,
// /var/tmp and /home/gem live. -x keeps the walk on one device, so the workspace
// volume (a different device, mounted at /workspace) is not counted twice: it is
// measured by Measure. The image's own files are part of the number and are
// subtracted through the boot baseline, not filtered out here — a filter list is
// a thing that goes stale the moment the image grows a directory.
func MeasureRoot(ctx context.Context, root string) (int64, error) {
	return duBytes(ctx, root)
}

// ReadBaseline reads the size the container's writable layers had at boot.
//
// A missing file, an empty one or garbage all return 0, and the caller treats 0
// as "no baseline": the second term is then off rather than counting the image as
// the tenant's usage.
func ReadBaseline(path string) int64 {
	if strings.TrimSpace(path) == "" {
		return 0
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

// duBytes is the one walk both measurements use.
func duBytes(ctx context.Context, root string) (int64, error) {
	cmd := exec.CommandContext(ctx, "du", "-sbx", root)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("du -sbx %s: %w", root, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return 0, fmt.Errorf("du -sbx %s printed %q, want a byte total", root, strings.TrimSpace(string(out)))
	}
	bytes, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("du -sbx %s printed %q: %w", root, fields[0], err)
	}
	return bytes, nil
}

// Writers returns the pids to stop for one over-budget reading: every process
// holding a file below root open for writing, plus the ancestors that could start
// another one.
//
// The writable-fd rule is the narrowest definition of "the process putting bytes
// in the budget", and it is what keeps the enforcer from freezing the sandbox's
// own services: the file/shell API, code-server, Jupyter and the browser are all
// daemons that keep the workspace readable, and on an idle sandbox none of them
// has a writable file open below it (measured: zero on the AIO image the
// deployment runs). A `dd`, `tar`, `npm` or a shell redirect does, which is
// exactly the set that has to stop.
//
// The ancestor walk exists because a burst is usually a *loop*: the shell that
// runs `while :; do dd ...; done` opens a new writer for every file, so freezing
// the dd alone lets the tree keep growing. The walk stops at a listening socket
// so it cannot reach the services above the shell (see the loop below).
//
// This process, its ancestors and anything in its own session are excluded:
// signalling them would either end the enforcer or freeze the shell that started
// it.
func Writers(roots ...string) ([]int, error) {
	cleaned := make([]string, 0, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		root = filepath.Clean(root)
		if root == "" || root == "." {
			continue
		}
		cleaned = append(cleaned, root)
	}
	if len(cleaned) == 0 {
		return nil, fmt.Errorf("quota-enforcer: a workspace root is required")
	}
	self := os.Getpid()
	skip := ancestors(self)
	skip[self] = true
	if pgid := processGroup(self); pgid > 0 {
		// Everything in our own process group, not only our ancestors: a shell
		// that started us may have siblings in it.
		for pid := range groupWith(pgid) {
			skip[pid] = true
		}
	}
	uid := os.Geteuid()
	listening := listeners()

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("read /proc: %w", err)
	}
	seen := map[int]bool{}
	out := make([]int, 0, 8)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || skip[pid] {
			continue
		}
		if uidOf(pid) != uid {
			continue
		}
		if !writesBelow(pid, cleaned...) {
			continue
		}
		// The writer, and the chain that can start another one.
		//
		// Freezing only the process holding the file is not enough, and the bench
		// showed exactly why: `while :; do dd ...; done` opens a *new* `dd` for
		// every file, so a frozen dd is replaced 10 ms later and the tree keeps
		// growing until the escalation kills something. The loop's shell has to
		// stop with it.
		//
		// The walk stops at a process with a listening socket. That is what keeps
		// it away from the sandbox's own services — the file/shell API, code-server,
		// Jupyter, the browser — which are the things that must keep answering:
		// freezing the shell API would also freeze the platform's ability to
		// measure and pause the sandbox. A tool chain (`bash -c`, `npm`, a build)
		// has no listener in it.
		for i, target := 0, pid; i < 8 && target > 1; i++ {
			if skip[target] || uidOf(target) != uid || hasListener(target, listening) {
				break
			}
			if !seen[target] {
				seen[target] = true
				out = append(out, target)
			}
			parent := statField(target, 4)
			if parent <= 1 || parent == target {
				break
			}
			target = parent
		}
	}
	return out, nil
}

// listeners returns the socket inodes that are in LISTEN state, from /proc/net/tcp
// and /proc/net/tcp6. Both files share the layout: the local address in field 1
// and the state in field 3, as hex, with the rest of the columns after it.
func listeners() map[string]bool {
	out := map[string]bool{}
	for _, file := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		lines := strings.Split(string(raw), "\n")
		for _, line := range lines[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			if fields[3] != "0A" { // TCP_LISTEN
				continue
			}
			out[fields[9]] = true
		}
	}
	return out
}

// hasListener reports whether pid holds one of the listening sockets.
func hasListener(pid int, listening map[string]bool) bool {
	if len(listening) == 0 {
		return false
	}
	dir := fmt.Sprintf("/proc/%d/fd", pid)
	fds, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join(dir, fd.Name()))
		if err != nil || !strings.HasPrefix(target, "socket:[") {
			continue
		}
		inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
		if listening[inode] {
			return true
		}
	}
	return false
}

// ancestors returns pid and its parents, up to init. /proc/<pid>/stat field 4 is
// the parent pid (field 1 is the pid, 2 the comm in parentheses, 3 the state).
func ancestors(pid int) map[int]bool {
	out := map[int]bool{}
	for i := 0; i < 64 && pid > 1; i++ {
		out[pid] = true
		ppid := statField(pid, 4)
		if ppid <= 0 || ppid == pid {
			break
		}
		pid = ppid
	}
	return out
}

// processGroup is /proc/<pid>/stat field 5: the process group, which is the unit
// "stop everything this job started" actually means (`kill -- -PGID`).
func processGroup(pid int) int { return statField(pid, 5) }

// groupWith returns the pids in one process group.
func groupWith(pgid int) map[int]bool {
	out := map[int]bool{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if processGroup(pid) == pgid {
			out[pid] = true
		}
	}
	return out
}

// statField reads one 1-based field of /proc/<pid>/stat (the fields after the
// command name, which may itself contain spaces and is skipped).
func statField(pid, index int) int {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	// The command is parenthesised; fields after it start at field 3 (state).
	text := string(raw)
	close := strings.LastIndex(text, ")")
	if close < 0 {
		return 0
	}
	rest := strings.Fields(text[close+1:])
	// rest[0] is state (field 3).
	offset := index - 3
	if offset < 0 || offset >= len(rest) {
		return 0
	}
	value, err := strconv.Atoi(rest[offset])
	if err != nil {
		return 0
	}
	return value
}

// uidOf reads the real uid from /proc/<pid>/status.
func uidOf(pid int) int {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return -1
		}
		uid, err := strconv.Atoi(fields[1])
		if err != nil {
			return -1
		}
		return uid
	}
	return -1
}

// writesBelow reports whether pid has a regular file below one of the roots open
// for writing. The second root is "/" when the container's writable layers are
// measured, which is every file outside the workspace — the listener guard below
// is what keeps the sandbox's own services out of that set.
func writesBelow(pid int, roots ...string) bool {
	dir := fmt.Sprintf("/proc/%d/fd", pid)
	fds, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join(dir, fd.Name()))
		if err != nil {
			continue
		}
		if !belowAny(target, roots) {
			continue
		}
		if !writableFD(pid, fd.Name()) {
			continue
		}
		// A directory or a device is not a writer; a regular file is.
		if info, err := os.Stat(target); err != nil || info.IsDir() {
			continue
		}
		return true
	}
	return false
}

// belowAny reports whether an absolute path is inside one of the roots. A root of
// "/" matches everything, which is what "the container's writable layers" means.
func belowAny(target string, roots []string) bool {
	for _, root := range roots {
		if root == string(os.PathSeparator) {
			return strings.HasPrefix(target, string(os.PathSeparator))
		}
		if strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// writableFD reads the open flags out of /proc/<pid>/fdinfo/<fd> (octal) and
// reports whether they include write access.
func writableFD(pid int, fd string) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/fdinfo/%s", pid, fd))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "flags:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return false
		}
		flags, err := strconv.ParseUint(fields[1], 8, 32)
		if err != nil {
			return false
		}
		const oWRONLY, oRDWR = 0x1, 0x2
		return flags&(oWRONLY|oRDWR) != 0
	}
	return false
}

// processAlive reports whether a pid still exists (a zombie counts: it is still
// an entry in the table, and signalling it is harmless).
func processAlive(pid int) bool {
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}

// signalProcess freezes or kills one pid.
func signalProcess(pid int, action Action) error {
	switch action {
	case ActionFreeze:
		return syscall.Kill(pid, syscall.SIGSTOP)
	case ActionKill:
		return syscall.Kill(pid, syscall.SIGKILL)
	default:
		return nil
	}
}

// isolate puts the enforcer in its own session, so nothing the tenant does to
// "its" process group can reach it and nothing it does can reach itself.
func isolate() {
	_, _ = syscall.Setsid()
}
