package liveness

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/sandbox"
)

// Raw marker answers. They are tokens rather than exit codes because the AIO
// shell endpoint reports the command's exit status separately from its output
// and an empty output plus a non-zero status is the same shape as a broken
// request; a token says which of the three states the filesystem is in.
const (
	tokenNoDir    = "PF_LIVE_NODIR"
	tokenNoMarker = "PF_LIVE_NOMARK"
	tokenNoStat   = "PF_LIVE_NOSTAT"
)

// Shell is the single method the marker reader needs from a sandbox client.
// *aio.Client implements it; tests substitute an HTTP server and the real client
// so the command construction is covered too.
type Shell interface {
	Exec(ctx context.Context, command string, cwd string) (aio.ShellResult, error)
}

// AIOConfig configures the sandbox-API marker reader.
//
// Every dependency is a function because the control plane already owns the
// answers: the store knows which sandbox holds a session and which key opens it,
// and the provider knows the endpoint. Keeping them out of this package is what
// lets the tests point the same code at an httptest sandbox.
type AIOConfig struct {
	// Locate resolves a session to the sandbox that holds its workspace.
	Locate func(ctx context.Context, sessionID string) (string, error)
	// Endpoint is the sandbox's base URL; APIKey is the key its API requires.
	Endpoint func(ctx context.Context, sandboxID string) (string, error)
	APIKey   func(ctx context.Context, sandboxID string) (string, error)
	// Workspace is the workspace path inside the container. Empty uses
	// sandbox.ContainerWorkspace ("/workspace"), the path both container
	// providers mount.
	Workspace string
	// NewShell builds the client. Nil uses aio.New.
	NewShell func(baseURL, apiKey string) (Shell, error)
}

// AIO reads markers through the sandbox's own HTTP API.
//
// This is the same door the file panel and the terminal use, on purpose: the
// sandbox-runner deliberately exposes no exec endpoint (docs/SANDBOX-DEPLOY.md
// §1), and adding one to the lifecycle service for a liveness check would be a
// second, weaker shell. The container already has one.
type AIO struct {
	cfg AIOConfig
}

var _ MarkerReader = (*AIO)(nil)

// NewAIO builds the reader.
func NewAIO(cfg AIOConfig) (*AIO, error) {
	if cfg.Locate == nil {
		return nil, errors.New("liveness: an AIO marker reader needs a session→sandbox locator")
	}
	if cfg.Endpoint == nil {
		return nil, errors.New("liveness: an AIO marker reader needs an endpoint resolver")
	}
	if cfg.APIKey == nil {
		return nil, errors.New("liveness: an AIO marker reader needs an API key resolver")
	}
	if strings.TrimSpace(cfg.Workspace) == "" {
		cfg.Workspace = sandbox.ContainerWorkspace
	}
	if cfg.NewShell == nil {
		cfg.NewShell = func(baseURL, apiKey string) (Shell, error) {
			return aio.New(aio.Config{BaseURL: baseURL, APIKey: apiKey, Root: cfg.Workspace})
		}
	}
	return &AIO{cfg: cfg}, nil
}

// Describe implements MarkerReader.
func (a *AIO) Describe() string { return "sandbox-api" }

// Marker asks the sandbox whether the session's heartbeat file exists and when
// it was last written.
func (a *AIO) Marker(ctx context.Context, sessionID string) (MarkerState, error) {
	rel, err := MarkerRelPath(sessionID)
	if err != nil {
		return MarkerState{}, err
	}
	sandboxID, err := a.cfg.Locate(ctx, sessionID)
	if err != nil {
		return MarkerState{}, fmt.Errorf("locate the sandbox for session %s: %w", sessionID, err)
	}
	endpoint, err := a.cfg.Endpoint(ctx, sandboxID)
	if err != nil {
		return MarkerState{}, fmt.Errorf("endpoint of sandbox %s: %w", sandboxID, err)
	}
	key, err := a.cfg.APIKey(ctx, sandboxID)
	if err != nil {
		return MarkerState{}, fmt.Errorf("api key of sandbox %s: %w", sandboxID, err)
	}
	shell, err := a.cfg.NewShell(endpoint, key)
	if err != nil {
		return MarkerState{}, fmt.Errorf("sandbox client for %s: %w", sandboxID, err)
	}

	dir := path.Join(a.cfg.Workspace, path.Dir(rel))
	file := path.Join(a.cfg.Workspace, rel)
	// One call, no shell interpolation of the id: both paths are quoted, and the
	// id was already validated as a single segment.
	command := "d=" + aio.Quote(dir) + "; f=" + aio.Quote(file) +
		"; if [ ! -d \"$d\" ]; then echo " + tokenNoDir +
		"; elif [ ! -f \"$f\" ]; then echo " + tokenNoMarker +
		"; else stat -c %Y -- \"$f\" 2>/dev/null || echo " + tokenNoStat + "; fi"
	result, err := shell.Exec(ctx, command, a.cfg.Workspace)
	if err != nil {
		return MarkerState{}, fmt.Errorf("ask sandbox %s: %w", sandboxID, err)
	}
	return parseMarkerAnswer(result.Output)
}

// parseMarkerAnswer turns the shell answer into a MarkerState.
//
// Anything that is not one of the three tokens or an integer timestamp is an
// error: a garbled answer must be "unknown", never "dead", because "dead"
// interrupts a run.
func parseMarkerAnswer(output string) (MarkerState, error) {
	trimmed := strings.TrimSpace(output)
	switch trimmed {
	case tokenNoDir, tokenNoMarker:
		return MarkerState{Present: false}, nil
	case tokenNoStat:
		return MarkerState{}, errors.New("the sandbox could not stat the marker file")
	case "":
		return MarkerState{}, errors.New("the sandbox answered nothing")
	}
	seconds, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return MarkerState{}, fmt.Errorf("unexpected answer from the sandbox %q", trimExcerpt(trimmed))
	}
	return MarkerState{Present: true, ModTime: time.Unix(seconds, 0)}, nil
}

func trimExcerpt(s string) string {
	const max = 120
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
