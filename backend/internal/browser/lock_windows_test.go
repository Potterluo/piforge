//go:build windows

package browser

import (
	"os"
	"path/filepath"
	"testing"
)

// On Windows the lock is a file Chromium holds open while it runs, so a file
// nobody holds is exactly the zombie case: it must be removed before the next
// launch, or the profile stays "in use" forever.
func TestStaleSingletonLockFileIsRemoved(t *testing.T) {
	profile := t.TempDir()
	lockPath := filepath.Join(profile, singletonLockName)
	if err := os.WriteFile(lockPath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("create the lock: %v", err)
	}
	if err := prepareProfile(profile); err != nil {
		t.Fatalf("prepareProfile: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("the stale lock was not cleared (err=%v)", err)
	}
}

// No lock at all is the normal case and must stay silent.
func TestPrepareProfileWithoutALock(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "profile")
	if err := prepareProfile(profile); err != nil {
		t.Fatalf("prepareProfile: %v", err)
	}
	if info, err := os.Stat(profile); err != nil || !info.IsDir() {
		t.Fatalf("the profile directory was not created: %v", err)
	}
}
