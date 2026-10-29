//go:build linux || darwin

package browser

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"time"
)

// lockProbeTimeout bounds the check of a Chromium singleton socket. A browser
// that is running accepts the connection at once; the timeout only exists so a
// socket in a strange state cannot stall a launch.
const lockProbeTimeout = 300 * time.Millisecond

// clearStaleSingletonLock removes a Chromium singleton left by a browser that is
// no longer running, and refuses (with the lock's path) when it cannot prove
// that.
//
// The three cases, in the order Chromium itself would decide them:
//
//   - the lock names another host: its pid means nothing in this namespace, so
//     "the owner is gone" cannot be proved here. Chromium refuses to start
//     anyway; failing with the path is more useful than a 30-second timeout.
//   - a live browser is listening on the singleton socket: clearing would put a
//     second Chromium on a profile in use. Refuse, naming the lock.
//   - nothing is listening (the owner died, or its pid was reused by an
//     unrelated process): clear the three links.
//
// A pid that merely exists is not proof of a browser: an unrelated process can
// inherit the number, and Chromium itself takes over in that case.
func clearStaleSingletonLock(profile string) error {
	lockPath := filepath.Join(profile, singletonLockName)
	target, err := os.Readlink(lockPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return &LockError{Path: lockPath, Reason: "it is not a symlink this process can read", Err: err}
	}
	host, pid, ok := singletonTarget(target)
	if !ok {
		return &LockError{Path: lockPath, Reason: fmt.Sprintf("it points at %q, not at hostname-pid", target)}
	}
	if local, herr := os.Hostname(); herr == nil && host != local {
		return &LockError{
			Path:   lockPath,
			Reason: fmt.Sprintf("it was left by %s (pid %d), another machine whose processes cannot be checked from here", host, pid),
		}
	}
	if socket := singletonSocketTarget(profile); socket != "" && socketAnswers(socket) {
		return &LockError{
			Path:   lockPath,
			Reason: fmt.Sprintf("a browser is still listening on %s (pid %d)", socket, pid),
		}
	}
	return removeLockFiles(profile, lockPath)
}

// socketAnswers reports whether something is accepting connections on the
// singleton socket. The connection is closed immediately: Chromium's protocol
// treats a client that says nothing as a failed notification, which is its
// normal handling of a second instance that changed its mind.
func socketAnswers(path string) bool {
	conn, err := net.DialTimeout("unix", path, lockProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
