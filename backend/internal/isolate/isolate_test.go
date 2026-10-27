package isolate

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNoneIsAPassThrough(t *testing.T) {
	argv := []string{"pi", "--mode", "rpc"}
	if got := (None{}).Wrap(argv); strings.Join(got, " ") != strings.Join(argv, " ") {
		t.Fatalf("None rewrote the command: %v", got)
	}
	if (None{}).Name() != "none" {
		t.Fatal("None has a surprising name")
	}
}

func TestBubblewrapWrapsTheCommand(t *testing.T) {
	got := Bubblewrap{
		WritableDirs: []string{"/data/sandbox"},
		ShareNetwork: true,
	}.Wrap([]string{"pi", "--mode", "rpc"})
	joined := strings.Join(got, " ")

	for _, want := range []string{
		"bwrap",
		"--unshare-pid",
		"--die-with-parent",
		"--tmpfs /tmp",
		"--ro-bind-try /usr /usr",
		"--bind /data/sandbox /data/sandbox",
		"-- pi --mode rpc",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("wrapped command is missing %q:\n%s", want, joined)
		}
	}
	// Sharing the network means *not* unsharing it.
	if strings.Contains(joined, "--unshare-net") {
		t.Fatalf("network was unshared despite ShareNetwork:\n%s", joined)
	}
	if got[len(got)-1] != "rpc" {
		t.Fatalf("the original argv is not at the end: %v", got)
	}
}

func TestBubblewrapDropsTheNetworkByDefault(t *testing.T) {
	got := Bubblewrap{}.Wrap([]string{"sh", "-c", "true"})
	if !strings.Contains(strings.Join(got, " "), "--unshare-net") {
		t.Fatalf("network was shared by default:\n%v", got)
	}
}

func TestBubblewrapNeverUsesAShell(t *testing.T) {
	// A command containing shell metacharacters must stay one argv element:
	// joining it into a string is how a jail becomes a shell injection.
	got := Bubblewrap{}.Wrap([]string{"sh", "-c", "echo hi; rm -rf /"})
	if got[len(got)-1] != "echo hi; rm -rf /" {
		t.Fatalf("argv was flattened: %v", got)
	}
}

func TestFirejailWrapsTheCommand(t *testing.T) {
	got := Firejail{WritableDirs: []string{"/data/sb"}, ShareNetwork: false}.Wrap([]string{"pi"})
	joined := strings.Join(got, " ")
	for _, want := range []string{"firejail", "--noroot", "--net=none", "--whitelist=/data/sb", "-- pi"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("firejail command is missing %q:\n%s", want, joined)
		}
	}
}

func TestEmptyArgvIsLeftAlone(t *testing.T) {
	if got := (Bubblewrap{}).Wrap(nil); got != nil {
		t.Fatalf("wrapping nothing produced %v", got)
	}
	if got := (Firejail{}).Wrap(nil); got != nil {
		t.Fatalf("wrapping nothing produced %v", got)
	}
}

func TestNewRejectsUnknownKinds(t *testing.T) {
	if _, err := New(Options{Kind: "chroot-ish"}); err == nil {
		t.Fatal("an unknown launcher was accepted")
	}
}

func TestNewRejectsLaunchersOnOtherPlatforms(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this assertion is about non-linux hosts")
	}
	// Silently falling back to None here would run untrusted code unsandboxed
	// while the operator believes the opposite; refusing is the only honest
	// answer.
	if _, err := New(Options{Kind: "bubblewrap"}); err == nil {
		t.Fatal("bubblewrap was accepted on a non-linux host")
	}
	if _, err := New(Options{Kind: "firejail"}); err == nil {
		t.Fatal("firejail was accepted on a non-linux host")
	}
}

func TestNewValidatesTheBinary(t *testing.T) {
	original := lookPath
	t.Cleanup(func() { lookPath = original })

	if runtime.GOOS != "linux" {
		t.Skip("launchers are linux-only")
	}

	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if _, err := New(Options{Kind: "bubblewrap"}); err == nil {
		t.Fatal("a missing bwrap binary was accepted")
	}

	lookPath = func(string) (string, error) { return "/usr/bin/bwrap", nil }
	if _, err := New(Options{Kind: "bubblewrap"}); err != nil {
		t.Fatalf("a present bwrap binary was rejected: %v", err)
	}

	// An explicit path that does not exist is still an error.
	if _, err := New(Options{Kind: "bubblewrap", Binary: filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Fatal("a bogus explicit binary path was accepted")
	}
}

// TestRealBubblewrapRunsACommand exercises the wrapper against the real thing
// when it exists: building the argv correctly is necessary but not sufficient.
func TestRealBubblewrapRunsACommand(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap is linux-only")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap is not installed")
	}

	dir := t.TempDir()
	wrapped := Bubblewrap{
		WritableDirs: []string{dir},
		ShareNetwork: true,
	}.Wrap([]string{"/bin/sh", "-c", "echo jailed > " + filepath.Join(dir, "out.txt")})

	cmd := exec.Command(wrapped[0], wrapped[1:]...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bwrap run failed: %v\n%s", err, out)
	}
	body, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatalf("the jailed command did not write its file: %v", err)
	}
	if strings.TrimSpace(string(body)) != "jailed" {
		t.Fatalf("output = %q", body)
	}
}
