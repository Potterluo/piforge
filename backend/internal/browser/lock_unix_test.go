//go:build linux || darwin

package browser

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// deadPID returns the pid of a process that has already exited and been reaped,
// which is what a lock left by a killed browser names.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}
	return cmd.Process.Pid
}

// TestZombieSingletonLockIsCleared is the required recovery: a browser that was
// killed leaves SingletonLock pointing at a pid that no longer exists, and the
// next launch must clear it instead of inheriting the profile as "in use".
func TestZombieSingletonLockIsCleared(t *testing.T) {
	profile := t.TempDir()
	host, err := os.Hostname()
	if err != nil {
		t.Skipf("no hostname: %v", err)
	}
	pid := deadPID(t)

	lockPath := filepath.Join(profile, singletonLockName)
	if err := os.Symlink(fmt.Sprintf("%s-%d", host, pid), lockPath); err != nil {
		t.Fatalf("create the lock: %v", err)
	}
	for _, name := range []string{singletonCookieName, singletonSocketName} {
		if err := os.Symlink("/nonexistent", filepath.Join(profile, name)); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	if err := prepareProfile(profile); err != nil {
		t.Fatalf("prepareProfile with a zombie lock: %v", err)
	}
	for _, name := range []string{singletonLockName, singletonCookieName, singletonSocketName} {
		if _, err := os.Lstat(filepath.Join(profile, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s survived the cleanup (err=%v)", name, err)
		}
	}
}

// TestLiveBrowserLockIsNotCleared: the lock of a browser that is still running
// must not be cleared, and the refusal must name the file so the panel can say
// what to close.
func TestLiveBrowserLockIsNotCleared(t *testing.T) {
	profile := t.TempDir()
	host, err := os.Hostname()
	if err != nil {
		t.Skipf("no hostname: %v", err)
	}
	socketPath := filepath.Join(t.TempDir(), "SingletonSocket")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("no unix sockets on this machine: %v", err)
	}
	defer listener.Close()

	lockPath := filepath.Join(profile, singletonLockName)
	if err := os.Symlink(fmt.Sprintf("%s-%d", host, os.Getpid()), lockPath); err != nil {
		t.Fatalf("create the lock: %v", err)
	}
	if err := os.Symlink(socketPath, filepath.Join(profile, singletonSocketName)); err != nil {
		t.Fatalf("create the socket link: %v", err)
	}

	err = prepareProfile(profile)
	var lockErr *LockError
	if !errors.As(err, &lockErr) {
		t.Fatalf("prepareProfile = %v, want a *LockError", err)
	}
	if lockErr.Path != lockPath || !strings.Contains(err.Error(), lockPath) {
		t.Fatalf("the error does not name the lock: %v", err)
	}
	if _, statErr := os.Lstat(lockPath); statErr != nil {
		t.Fatalf("the live lock was removed: %v", statErr)
	}
}

// TestLockFromAnotherHostIsReported: a pid from another host cannot be checked
// from here, which is exactly when Chromium refuses to start. Fail with the
// path instead of pretending the launch can proceed.
func TestLockFromAnotherHostIsReported(t *testing.T) {
	profile := t.TempDir()
	lockPath := filepath.Join(profile, singletonLockName)
	if err := os.Symlink("another-host-4242", lockPath); err != nil {
		t.Fatalf("create the lock: %v", err)
	}

	err := prepareProfile(profile)
	var lockErr *LockError
	if !errors.As(err, &lockErr) {
		t.Fatalf("prepareProfile = %v, want a *LockError", err)
	}
	if lockErr.Path != lockPath || !strings.Contains(err.Error(), "another-host") {
		t.Fatalf("the error does not explain the foreign lock: %v", err)
	}
	if _, statErr := os.Lstat(lockPath); statErr != nil {
		t.Fatalf("the foreign lock was removed: %v", statErr)
	}
}

// A pid that exists is not proof of a browser: an unrelated process can inherit
// the number. Chromium takes over in that case (verified against the real
// binary), so the launch does too rather than refusing forever.
func TestLockWhosePIDWasReusedIsCleared(t *testing.T) {
	profile := t.TempDir()
	host, err := os.Hostname()
	if err != nil {
		t.Skipf("no hostname: %v", err)
	}
	lockPath := filepath.Join(profile, singletonLockName)
	if err := os.Symlink(fmt.Sprintf("%s-%d", host, os.Getpid()), lockPath); err != nil {
		t.Fatalf("create the lock: %v", err)
	}

	if err := prepareProfile(profile); err != nil {
		t.Fatalf("prepareProfile: %v", err)
	}
	if _, err := os.Lstat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the stale lock was not cleared (err=%v)", err)
	}
}

// A lock that is not the shape Chromium writes cannot be reasoned about, so it
// is reported rather than removed.
func TestUnreadableLockTargetIsReported(t *testing.T) {
	profile := t.TempDir()
	lockPath := filepath.Join(profile, singletonLockName)
	if err := os.Symlink("not-a-hostname-pid", lockPath); err != nil {
		t.Fatalf("create the lock: %v", err)
	}
	err := prepareProfile(profile)
	var lockErr *LockError
	if !errors.As(err, &lockErr) {
		t.Fatalf("prepareProfile = %v, want a *LockError", err)
	}
	if _, statErr := os.Lstat(lockPath); statErr != nil {
		t.Fatalf("an unreadable lock was removed: %v", statErr)
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
