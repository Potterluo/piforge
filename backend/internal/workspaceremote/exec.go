package workspaceremote

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/gitx"
)

// ShellExecutor runs one git invocation inside the sandbox that owns a host
// directory, and reports stdout, stderr and the exit code separately.
//
// Why a shell bridge rather than a git library: the panel's git behaviour is
// defined by internal/gitx (porcelain v2 parsing, path validation, "nothing to
// commit", hunk application through stdin). Re-implementing that against
// another API would produce a second, subtly different git reader, and the
// panel would start disagreeing with the agent about what is staged. The
// container has git 2.34; the commands are the same argv. So gitx keeps its
// parsing and this only changes where the process runs.
//
// Stream separation is deliberate: `git status --porcelain` is parsed from
// stdout, and mixing a git warning into it would silently corrupt the change
// list. Each invocation therefore runs inside a private temp directory, writes
// stdout and stderr to files, and returns them base64 encoded — one HTTP call,
// no ambiguity, and safe for paths and patches that contain newlines.
type ShellExecutor struct {
	// Backend resolves a host directory to the sandbox that holds it.
	Backend Backend
	// Binary is the git executable inside the container; empty means "git".
	Binary string
}

var _ gitx.ExecFunc = ShellExecutor{}.Run

// Run implements gitx.ExecFunc.
func (e ShellExecutor) Run(ctx context.Context, dir string, stdin []byte, argv []string) (gitx.Result, error) {
	if e.Backend == nil {
		return gitx.Result{}, fmt.Errorf("workspaceremote: the git executor has no backend")
	}
	if strings.TrimSpace(dir) == "" {
		return gitx.Result{}, fmt.Errorf("workspaceremote: git needs a workspace directory")
	}
	tgt, err := e.Backend.Target(ctx, dir)
	if err != nil {
		return gitx.Result{}, fmt.Errorf("workspaceremote: resolve %q for git: %w", dir, err)
	}
	bin := e.Binary
	if strings.TrimSpace(bin) == "" {
		bin = "git"
	}

	quoted := make([]string, 0, len(argv)+3)
	quoted = append(quoted, aio.Quote(bin))
	// A checkout provisioned by the control plane arrives through a bind mount,
	// and on a Docker Desktop/drvfs host the mount shows it as owned by root
	// while the sandbox's shell runs as `gem`. Git 2.34 refuses that repository
	// ("detected dubious ownership"), which turned every panel git view into a
	// 500 even though `docker exec -u root git` worked. The path is already the
	// workspace this sandbox owns, so trusting exactly it is the narrow fix:
	// `safe.directory=*` would trust every repository in the container.
	quoted = append(quoted, aio.Quote("-c"), aio.Quote("safe.directory="+tgt.Root))
	for _, arg := range argv {
		quoted = append(quoted, aio.Quote(arg))
	}
	command := "cd -- " + aio.Quote(tgt.Root) + " && " + strings.Join(quoted, " ")

	stdinPath := ""
	if stdin != nil {
		suffix := make([]byte, 8)
		if _, rerr := rand.Read(suffix); rerr != nil {
			return gitx.Result{}, fmt.Errorf("workspaceremote: git stdin: %w", rerr)
		}
		stdinPath = path.Join("/tmp", "pf-git-stdin-"+hex.EncodeToString(suffix))
		if werr := tgt.Client.WriteBytes(ctx, stdinPath, stdin); werr != nil {
			return gitx.Result{}, fmt.Errorf("workspaceremote: git stdin: %w", werr)
		}
		command += " < " + aio.Quote(stdinPath)
	}

	script := []string{
		`d=$(mktemp -d) || exit 97`,
		`{ ` + command + `; } > "$d/o" 2> "$d/e"`,
		`rc=$?`,
		`printf 'PFEXIT:%d\n' "$rc"`,
		`printf 'PFOUT:'; base64 < "$d/o" | tr -d '\n'; printf '\n'`,
		`printf 'PFERR:'; base64 < "$d/e" | tr -d '\n'; printf '\n'`,
	}
	if stdinPath != "" {
		script = append(script, "rm -f -- "+aio.Quote(stdinPath))
	}
	// No `exit`: the image runs each exec in a persistent shell session, and an
	// explicit exit wedges that session (probed on 1.11.0: the request never
	// answers). The last command's status is irrelevant — the git exit code is
	// already captured in PFEXIT.
	script = append(script, `rm -rf "$d"`)

	res, err := tgt.Client.Exec(ctx, strings.Join(script, "; "), tgt.Root)
	if err != nil {
		return gitx.Result{}, fmt.Errorf("workspaceremote: git %s: %w", strings.Join(argv, " "), err)
	}

	result := gitx.Result{}
	seenExit := false
	for _, line := range strings.Split(res.Output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "PFEXIT:"):
			if _, serr := fmt.Sscanf(strings.TrimPrefix(line, "PFEXIT:"), "%d", &result.ExitCode); serr != nil {
				return gitx.Result{}, fmt.Errorf("workspaceremote: git exit line %q: %w", line, serr)
			}
			seenExit = true
		case strings.HasPrefix(line, "PFOUT:"):
			raw, derr := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "PFOUT:"))
			if derr != nil {
				return gitx.Result{}, fmt.Errorf("workspaceremote: decode git stdout: %w", derr)
			}
			result.Stdout = string(raw)
		case strings.HasPrefix(line, "PFERR:"):
			raw, derr := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "PFERR:"))
			if derr != nil {
				return gitx.Result{}, fmt.Errorf("workspaceremote: decode git stderr: %w", derr)
			}
			result.Stderr = string(raw)
		}
	}
	if !seenExit {
		return gitx.Result{}, fmt.Errorf("workspaceremote: git %s: the sandbox answered without an exit code: %s",
			strings.Join(argv, " "), truncateOutput(res.Output))
	}
	if result.ExitCode != 0 {
		return result, &gitx.CommandError{
			Args:   argv,
			Code:   result.ExitCode,
			Stderr: result.Stderr,
			Stdout: result.Stdout,
		}
	}
	return result, nil
}

func truncateOutput(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
