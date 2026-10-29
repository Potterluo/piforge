//go:build windows

package browser

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// clearStaleSingletonLock removes a Chromium singleton that no browser is
// holding any more.
//
// On Windows the lock is a file Chromium keeps open for as long as the browser
// runs, with delete sharing off; the OS releases the handle when the process
// dies. So "can the file be removed" is the liveness test: a live browser makes
// the remove fail with a sharing violation, and a dead one leaves a file nobody
// owns. A failure is reported with the path, which is what the operator has to
// delete by hand.
func clearStaleSingletonLock(profile string) error {
	lockPath := filepath.Join(profile, singletonLockName)
	if err := os.Remove(lockPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return &LockError{
			Path:   lockPath,
			Reason: "a browser is still holding it, so this launch will not take the profile over",
			Err:    err,
		}
	}
	return removeLockFiles(profile, lockPath)
}
