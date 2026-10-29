package browser

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Chromium guards one profile with a "singleton": SingletonLock, a cookie and a
// socket next to it. A browser that exits normally removes them; one that is
// killed leaves them behind, and the next Chromium then has to decide whether
// the profile is really in use.
//
// That decision is not ours to make up: on POSIX the lock is a symlink to
// "<hostname>-<pid>", and Chromium treats a lock from another host as fatal
// ("The profile appears to be in use by another Chromium process (N) on another
// computer (H)") while it takes over a lock from a dead process on this host by
// itself. What it cannot do is tell the operator which file to remove, and it
// cannot avoid the 30-second "devtools never became ready" wait when a live
// browser swallows a launch. Both are this file's job.
const (
	singletonLockName   = "SingletonLock"
	singletonCookieName = "SingletonCookie"
	singletonSocketName = "SingletonSocket"
)

// LockError is a Chromium profile lock this process will not clear.
//
// Path is the SingletonLock that has to be dealt with — the message names it so
// a stuck panel can say which file to remove instead of "devtools never became
// ready". Reason says what the lock looks like (another host, a live browser,
// an unreadable symlink).
type LockError struct {
	Path   string
	Reason string
	Err    error
}

func (e *LockError) Error() string {
	switch {
	case e.Reason != "" && e.Err != nil:
		return fmt.Sprintf("browser: profile lock %s: %s: %v", e.Path, e.Reason, e.Err)
	case e.Reason != "":
		return fmt.Sprintf("browser: profile lock %s: %s", e.Path, e.Reason)
	default:
		return fmt.Sprintf("browser: profile lock %s: %v", e.Path, e.Err)
	}
}

func (e *LockError) Unwrap() error { return e.Err }

// prepareProfile creates the browser's profile directory and clears a lock left
// by a browser that is no longer running.
func prepareProfile(profile string) error {
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return fmt.Errorf("create profile dir: %w", err)
	}
	return clearStaleSingletonLock(profile)
}

// singletonTarget parses the "hostname-pid" a POSIX Chromium writes in the
// lock's symlink target. A hostname may contain dashes, so the pid is the last
// dash-separated field.
func singletonTarget(target string) (host string, pid int, ok bool) {
	target = strings.TrimSpace(target)
	idx := strings.LastIndex(target, "-")
	if idx <= 0 || idx == len(target)-1 {
		return "", 0, false
	}
	pid, err := strconv.Atoi(target[idx+1:])
	if err != nil || pid <= 0 {
		return "", 0, false
	}
	return target[:idx], pid, true
}

// removeLockFiles clears the three singleton links. The lock goes first: it is
// the file that decides whether the next browser starts.
func removeLockFiles(profile, lockPath string) error {
	paths := []string{
		lockPath,
		filepath.Join(profile, singletonCookieName),
		filepath.Join(profile, singletonSocketName),
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return &LockError{Path: lockPath, Reason: "it could not be removed", Err: err}
		}
	}
	return nil
}

// singletonSocketTarget reads the socket link, returning "" when there is none.
func singletonSocketTarget(profile string) string {
	target, err := os.Readlink(filepath.Join(profile, singletonSocketName))
	if err != nil {
		return ""
	}
	return target
}
