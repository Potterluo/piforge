package preflight

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DockerInspector is the preflight's real Inspector: it shells out to the
// docker CLI, which is the same tool the rest of the deployment's scripts use.
//
// The CLI rather than the Engine API on purpose: the failure this catches most
// often is "the daemon the user's docker points at is not the one they think",
// and that is exactly what `docker version` reports.
type DockerInspector struct {
	// Bin is the docker executable (default "docker").
	Bin string
	// Timeout bounds one call (default 60 s: the probe starts a container).
	Timeout time.Duration
}

func (d *DockerInspector) bin() string {
	if strings.TrimSpace(d.Bin) != "" {
		return d.Bin
	}
	return "docker"
}

func (d *DockerInspector) timeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return 60 * time.Second
}

func (d *DockerInspector) run(ctx context.Context, args ...string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	cmd := exec.CommandContext(runCtx, d.bin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if message == "" {
			message = err.Error()
		}
		return stdout.String(), fmt.Errorf("%s %s: %s", d.bin(), strings.Join(args, " "), firstLine(message))
	}
	return stdout.String(), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// EngineVersion asks the daemon, not the client, so a stopped daemon fails here.
func (d *DockerInspector) EngineVersion(ctx context.Context) (string, error) {
	out, err := d.run(ctx, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ImageExists uses `docker image inspect`, which is local-only: the preflight
// must not pull 13 GB to answer a question about what is on this disk.
func (d *DockerInspector) ImageExists(ctx context.Context, ref string) (bool, error) {
	_, err := d.run(ctx, "image", "inspect", ref)
	if err == nil {
		return true, nil
	}
	// "No such image" is the answer, not an error: anything else (a daemon that
	// went away mid-run) has to fail the check.
	if strings.Contains(err.Error(), "No such image") || strings.Contains(err.Error(), "no such image") {
		return false, nil
	}
	return false, err
}

// ImageProbe runs one shell script inside the image and returns its stdout. The
// entrypoint is replaced because the sandbox image's own entrypoint starts a
// desktop and never returns.
func (d *DockerInspector) ImageProbe(ctx context.Context, ref, script string) (string, error) {
	out, err := d.run(ctx, "run", "--rm", "--network", "none", "--entrypoint", "/bin/sh", ref, "-c", script)
	if err != nil {
		return out, err
	}
	return out, nil
}
