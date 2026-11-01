package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Hardening: git is a command interpreter as well as a client.
//
// Every guard in this package until now described *argv*: a tenant value is one
// element, never a command line, so it cannot become an option or a shell word.
// The adversarial round of 2026-11-01 (e2e/.artifacts/adversarial5/
// REPORT-adversarial5-git-and-paths.md, findings 8 and 11) showed why that is
// only half the question: `.git/config` is tenant-writable, and git executes
// configuration. `core.fsmonitor` ran an arbitrary binary through an ordinary
// `GET /git/status`; `core.sshCommand` ran one through `POST /git/push`.
//
// The answer is not another argv check — it is to make the configuration
// deployment-owned. Git applies `-c key=value` at the *command* scope, which
// outranks every file: the repository's `.git/config`, the worktree config, the
// user's `~/.gitconfig` and the system file. So every invocation this package
// makes carries the keys below, and the inherited environment is filtered to
// drop every `GIT_*` variable an agent could have influenced (GIT_CONFIG_COUNT,
// GIT_CONFIG_KEY_n, GIT_SSH_COMMAND, GIT_ASKPASS, GIT_TRACE, …).
//
// What this does not do: it does not make git safe to run on a repository whose
// *content* is hostile in a way git itself must execute — hooks are off, but a
// `git checkout` can still write files anywhere inside the checkout. That is the
// tenant's own tree, and the boundary for it is the sandbox, not this file.

// Hardened is the configuration the control plane imposes on a git process.
//
// The zero value is not "no hardening": every field falls back to a value that
// closes the hole, so an empty Hardened is safe rather than open.
type Hardened struct {
	// HooksPath replaces `core.hooksPath`. A directory that does not exist (or
	// that this process made and nobody else can write) means no hook runs.
	HooksPath string
	// SSHCommand replaces `core.sshCommand`. For the host it is the absolute
	// path resolved once at start-up; for a sandbox it is the name `ssh` inside
	// the container, whose PATH is the image's.
	SSHCommand string
	// FileAllow is `protocol.file.allow`: "user" keeps a local path a caller
	// named on the command line working (the fixture repositories the product
	// supports) while refusing the file protocol from inside a repository's own
	// configuration (a submodule URL, an insteadOf rewrite).
	FileAllow string
}

// hooksDirSuffix names the empty directory this process creates for
// `core.hooksPath`. It is created once, 0700, with a random middle so a tenant
// sharing the machine's temp directory cannot plant a hook in it.
const hooksDirPrefix = "piforge-empty-hooks-"

// unavailable names a command that does not exist: when the deployment has no
// ssh, an ssh remote fails closed with git's own "cannot run" message instead of
// falling back to whatever the repository's config says.
const unavailable = "piforge-command-not-available"

var (
	once           sync.Once
	hostHardened   *Hardened
	remoteHardened *Hardened
)

// DefaultHardened is the configuration for a git process this host runs.
//
// The ssh command is resolved to an absolute path at start-up, so a `ssh`
// earlier in PATH — a file the tenant can write in host placement — is never the
// binary git executes. When ssh cannot be found, the value names a command that
// does not exist: an ssh remote then fails closed rather than inheriting the
// repository's `core.sshCommand`.
func DefaultHardened() *Hardened {
	once.Do(func() {
		hostHardened = &Hardened{
			HooksPath:  emptyHooksDir(),
			SSHCommand: resolvedSSH(),
			FileAllow:  "user",
		}
		// The sandbox's git runs inside the container: a host path in
		// `core.hooksPath` names nothing there, which is exactly right (no
		// hooks), but a host path in `core.sshCommand` would break a legitimate
		// ssh clone made inside the sandbox. And the tenant already has a shell
		// in that container, so executing the container's own ssh on the
		// container's own keys is not an escalation.
		remoteHardened = &Hardened{
			HooksPath:  "/dev/null/piforge-empty-hooks",
			SSHCommand: "ssh",
			FileAllow:  "user",
		}
	})
	return hostHardened
}

// RemoteHardened is the configuration for git that runs inside a sandbox.
func RemoteHardened() *Hardened {
	DefaultHardened()
	return remoteHardened
}

// resolvedSSH is the absolute path of ssh, or a name that cannot be run.
func resolvedSSH() string {
	path, err := exec.LookPath("ssh")
	if err != nil || strings.TrimSpace(path) == "" {
		return unavailable
	}
	abs, aerr := filepath.Abs(path)
	if aerr != nil {
		return unavailable
	}
	// GIT_SSH_COMMAND and core.sshCommand are run through a shell, and Git for
	// Windows runs that shell with a POSIX view of the path, where a backslash
	// is an escape. Forward slashes work in both worlds.
	return filepath.ToSlash(abs)
}

// emptyHooksDir creates the directory `core.hooksPath` points at: empty, 0700,
// and named with a random suffix so its name cannot be guessed by another
// process on the same machine.
func emptyHooksDir() string {
	dir, err := os.MkdirTemp("", hooksDirPrefix)
	if err != nil {
		// Falling back to a path that does not exist still disables hooks; it is
		// only weaker in that a tenant who can write to the parent could create
		// it. Say so where an operator will see it.
		return filepath.Join(os.TempDir(), hooksDirPrefix+"unavailable", "hooks")
	}
	_ = os.Chmod(dir, 0o700)
	return filepath.ToSlash(dir)
}

// Args is the `-c` list every invocation on a tenant repository carries.
//
// The keys, and what each one stops:
//
//	core.fsmonitor=false          a command run while git refreshes the index
//	core.hooksPath=…              pre-commit, post-checkout, fsmonitor-watchman, …
//	core.sshCommand=…             a command run instead of ssh (and GIT_SSH_COMMAND)
//	core.pager= / core.askPass=   a program run to render or to prompt
//	credential.helper=            a program run to answer a credential request
//	commit.gpgSign/tag.gpgSign    gpg.program, run by an ordinary commit
//	core.gitProxy=                a command run for the git:// protocol
//	protocol.ext.allow=never      git-remote-ext: `ext::sh -c …`
//	protocol.file.allow=user      a file:// or path remote from *configuration*
//	http.followRedirects=false    a second URL the guard never saw (finding 1)
//	submodule/fetch recurse=false a fetch the caller did not ask for
//
// Order matters in one way: these are prepended to argv, so they must come
// before the subcommand, and a caller must not pass its own `-c` for one of
// these keys (a later `-c` wins in git).
func (h *Hardened) Args() []string {
	hooks := strings.TrimSpace(h.HooksPath)
	if hooks == "" {
		hooks = filepath.Join(os.TempDir(), hooksDirPrefix+"unavailable", "hooks")
	}
	ssh := strings.TrimSpace(h.SSHCommand)
	if ssh == "" {
		ssh = unavailable
	}
	allow := strings.TrimSpace(h.FileAllow)
	if allow != "never" {
		allow = "user"
	}
	return []string{
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=" + hooks,
		"-c", "core.sshCommand=" + ssh,
		"-c", "core.pager=",
		"-c", "core.askPass=",
		"-c", "core.gitProxy=",
		"-c", "credential.helper=",
		"-c", "commit.gpgSign=false",
		"-c", "tag.gpgSign=false",
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.file.allow=" + allow,
		"-c", "http.followRedirects=false",
		"-c", "submodule.recurse=false",
		"-c", "fetch.recurseSubmodules=false",
	}
}

// envDrop is every inherited variable that can steer git away from the
// configuration above. `GIT_*` covers GIT_DIR, GIT_WORK_TREE, GIT_CONFIG,
// GIT_CONFIG_COUNT/KEY_n/VALUE_n, GIT_SSH, GIT_SSH_COMMAND, GIT_ASKPASS,
// GIT_TRACE*, GIT_EXEC_PATH and the rest; the two non-GIT names are the other
// ways to make git start a program.
var envDrop = []string{
	"GIT_",
	"SSH_ASKPASS",
	"PAGER",
}

// Env is the environment for one invocation: base with the steering variables
// removed, plus the values this deployment owns.
func (h *Hardened) Env(base []string) []string { return h.EnvWith(base) }

// EnvWith is Env with caller-supplied KEY=VALUE pairs inserted before the
// deployment's own, so an extra can add a variable but cannot undo one of the
// guarantees above (a later duplicate wins in exec).
func (h *Hardened) EnvWith(base []string, extra ...string) []string {
	out := make([]string, 0, len(base)+len(extra)+8)
	for _, kv := range base {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		key := strings.ToUpper(kv[:i])
		skip := false
		for _, prefix := range envDrop {
			if strings.HasPrefix(key, prefix) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, extra...)
	out = append(out,
		// No credential prompt can block a request, and no helper answers one.
		"GIT_TERMINAL_PROMPT=0",
		// A read-only status must not take a lock the agent is holding.
		"GIT_OPTIONAL_LOCKS=0",
		// The second half of http.followRedirects=false: a redirect is a refusal,
		// not a fetch of a URL the policy never checked.
		"GIT_HTTP_FOLLOW_REDIRECTS=0",
		// A pager is a program git starts; none is needed with a pipe.
		"GIT_PAGER=",
		"PAGER=",
		"LC_ALL=C",
		"LANG=C",
	)
	if ssh := strings.TrimSpace(h.SSHCommand); ssh != "" {
		out = append(out, "GIT_SSH_COMMAND="+ssh)
	}
	return out
}

// HardenedArgs is DefaultHardened().Args() for a caller that runs git itself.
func HardenedArgs() []string { return DefaultHardened().Args() }

// HardenedEnv is the sanitized environment for a caller that runs git itself.
func HardenedEnv() []string { return DefaultHardened().Env(os.Environ()) }

// HardenedEnvFor is HardenedEnv plus extra KEY=VALUE pairs, for a caller that
// needs one more variable (an author identity, a trace of its own).
func HardenedEnvFor(extra ...string) []string {
	return DefaultHardened().EnvWith(os.Environ(), extra...)
}

// IsWindows reports the path semantics these guards are reasoned about. It is
// exported so a caller can say "this refusal is a Windows one" in a message.
func IsWindows() bool { return runtime.GOOS == "windows" }
