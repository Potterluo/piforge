package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Adversarial round 5, findings 8 and 11: the guards in this package described
// argv, and argv is not what git executes. `.git/config` is tenant-writable, and
// `core.fsmonitor` ran a command through an ordinary `git status` while
// `core.sshCommand` ran one through `git push`. These tests are the same two
// attacks, one layer down: the marker must not appear.

// configure runs `git -C dir config key value` directly (the test's own git, not
// the Runner) so the repository is configured exactly as a tenant's agent would.
func configure(t *testing.T, dir, key, value string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "config", key, value)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config %s: %v\n%s", key, err, out)
	}
}

// markerScript writes a shell script that creates the marker file when it runs,
// and returns the command string a repository would put in a configuration key.
func markerScript(t *testing.T, path string) string {
	t.Helper()
	return "sh -c 'printf RAN > " + filepath.ToSlash(path) + "; exit 1'"
}

func TestRepoConfigCannotRunACommandThroughStatus(t *testing.T) {
	cl, dir := newRepo(t)
	marker := filepath.Join(t.TempDir(), "fsmonitor-marker.txt")
	configure(t, dir, "core.fsmonitor", markerScript(t, marker))

	if _, err := cl.Status(context.Background(), dir); err != nil {
		t.Fatalf("status: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("core.fsmonitor ran a command through git status: %s exists", marker)
	}
}

func TestRepoConfigCannotRunACommandThroughPush(t *testing.T) {
	cl, dir := newRepo(t)
	marker := filepath.Join(t.TempDir(), "sshcmd-marker.txt")
	// A port nothing listens on: the push must fail, and the point is that it
	// fails by running the deployment's ssh rather than the repository's command.
	configure(t, dir, "remote.origin.url", "ssh://git@127.0.0.1:1/acme/repo.git")
	configure(t, dir, "core.sshCommand", markerScript(t, marker))

	if _, err := cl.Push(context.Background(), dir); err == nil {
		t.Fatal("a push to a closed port succeeded")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("core.sshCommand ran a command through git push: %s exists", marker)
	}
}

// The environment is the other half: GIT_CONFIG_COUNT / GIT_CONFIG_KEY_n are a
// documented way to inject configuration without touching a file, and
// GIT_SSH_COMMAND is a documented way to name a command. All of it must be gone
// before git starts.
func TestRunnerDropsEveryInheritedGitVariable(t *testing.T) {
	cl := newClient(t)
	hostile := map[string]string{
		"GIT_DIR":                   "/tmp/elsewhere",
		"GIT_WORK_TREE":             "/tmp/elsewhere",
		"GIT_CONFIG_COUNT":          "1",
		"GIT_CONFIG_KEY_0":          "core.fsmonitor",
		"GIT_CONFIG_VALUE_0":        "sh -c 'touch /tmp/pwned'",
		"GIT_SSH_COMMAND":           "/tmp/pwned-ssh",
		"GIT_ASKPASS":               "/tmp/pwned-askpass",
		"GIT_TRACE":                 "/tmp/pwned-trace",
		"GIT_EXEC_PATH":             "/tmp/pwned-exec",
		"GIT_HTTP_FOLLOW_REDIRECTS": "1",
		"SSH_ASKPASS":               "/tmp/pwned-ssh-askpass",
	}
	for k, v := range hostile {
		t.Setenv(k, v)
	}

	env := cl.Env()
	joined := strings.Join(env, "\n")
	pair := map[string]bool{}
	for _, kv := range env {
		pair[kv] = true
	}
	for key, value := range hostile {
		// GIT_SSH_COMMAND is set by this deployment, so the name is expected;
		// the inherited *pair* is what must not survive.
		if pair[key+"="+value] {
			t.Fatalf("the inherited %s=%s survived into the environment git runs with:\n%s", key, value, joined)
		}
	}
	for _, want := range []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_HTTP_FOLLOW_REDIRECTS=0",
		"GIT_SSH_COMMAND=",
		"GIT_PAGER=",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the environment git runs with is missing %q:\n%s", want, joined)
		}
	}
}

// The `-c` list is only worth anything if it is on the command line, before the
// subcommand, and if every key that makes git start a program is in it.
func TestHardenedConfigIsOnEveryCommandLine(t *testing.T) {
	var got []string
	cl := New(Config{
		Exec: func(ctx context.Context, dir string, stdin []byte, argv []string) (Result, error) {
			got = append([]string{}, argv...)
			return Result{}, nil
		},
	})
	if _, err := cl.Run(context.Background(), "/repo", "status", "--porcelain"); err != nil {
		t.Fatalf("run: %v", err)
	}

	want := []string{
		"core.fsmonitor=false",
		"core.hooksPath=",
		"core.sshCommand=",
		"credential.helper=",
		"protocol.ext.allow=never",
		"protocol.file.allow=",
		"http.followRedirects=false",
	}
	for _, key := range want {
		if !hasConfig(got, key) {
			t.Fatalf("the command line does not pin %s: %v", key, got)
		}
	}
	// Before the subcommand: a `-c` after it is an argument to the command, not
	// configuration.
	statusAt := indexOf(got, "status")
	for i := 0; i < len(got); i++ {
		if got[i] == "-c" && i > statusAt {
			t.Fatalf("a -c pair follows the subcommand: %v", got)
		}
	}
}

// The sandbox's git runs inside the container, where a host path for ssh would
// break a legitimate clone. That is the one difference between the two
// hardenings, and it must stay: the rest of the list is identical.
func TestRemoteHardeningDoesNotCarryAHostPath(t *testing.T) {
	remote := RemoteHardened()
	if remote.SSHCommand != "ssh" {
		t.Fatalf("a sandbox's core.sshCommand = %q, want the container's own ssh", remote.SSHCommand)
	}
	if strings.Contains(remote.HooksPath, `\`) {
		t.Fatalf("a sandbox's core.hooksPath is a Windows path: %q", remote.HooksPath)
	}
	for _, key := range []string{"core.fsmonitor=false", "http.followRedirects=false"} {
		if !hasConfig(remote.Args(), key) {
			t.Fatalf("the sandbox hardening is missing %s: %v", key, remote.Args())
		}
	}
}

func hasConfig(argv []string, prefix string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-c" && strings.HasPrefix(argv[i+1], prefix) {
			return true
		}
	}
	return false
}

func indexOf(argv []string, value string) int {
	for i, item := range argv {
		if item == value {
			return i
		}
	}
	return -1
}
