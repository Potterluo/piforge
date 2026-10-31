package liveness

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// The heartbeat marker.
//
// # Layout
//
//	<workspace>/.piforge/live/<session-id>        (container: /workspace/...)
//
// It is at the sandbox root, not inside a worktree: a worktree is a git checkout
// and a file that changes every few seconds there would show up in every status,
// diff and commit the user makes. The sandbox root is bind-mounted into the
// container, so the same bytes are visible to the agent's launcher (which runs
// beside the agent) and to the control plane's probe (which asks the sandbox
// over its API).
//
// # Why a marker instead of a process search
//
// `pgrep -f <session marker>` was the other candidate. It is worse on every axis
// that matters here: the pattern also matches the grep itself and any shell that
// mentions the path, a killed-but-not-yet-reaped process still matches, the
// sandbox image must expose a process table, and none of it says *which* agent
// last served the session. A file has an explicit writer (the agent's launcher),
// an explicit end (removed when the agent exits) and an age, which is what makes
// a stale marker distinguishable from a live one.
//
// # The lease
//
// The launcher rewrites the file every DefaultBeat while the agent process is
// alive. The probe treats a marker older than DefaultTTL as dead. The gap is
// deliberate: four missed beats, so a scheduler hiccup or a loaded host does not
// turn into "interrupt a live run", while a launcher that died is still detected
// inside one watchdog interval's worth of minutes.
const (
	// MarkerDir is the marker directory relative to the sandbox workspace.
	MarkerDir = ".piforge/live"
	// DefaultBeat is how often the agent launcher rewrites the marker.
	DefaultBeat = 15 * time.Second
	// DefaultTTL is how old a marker may be before the probe calls it dead.
	DefaultTTL = 2 * time.Minute
)

// MarkerState is what a reader observed about one session's marker.
type MarkerState struct {
	// Present reports that the marker file exists.
	Present bool
	// ModTime is when the marker was last written. Only meaningful when Present.
	ModTime time.Time
}

// MarkerReader reads one session's marker from the workspace that holds it.
//
// Two implementations exist: aioMarker asks the sandbox over its shell API (the
// container providers), and the tests use fakes. The interface is this small on
// purpose: everything else — the TTL, the "alive means fresh" rule — is one
// implementation in Sandbox, so the local and remote paths cannot drift.
type MarkerReader interface {
	Marker(ctx context.Context, sessionID string) (MarkerState, error)
	// Describe names the transport for a startup log ("sandbox-api").
	Describe() string
}

// SandboxConfig configures the marker-backed probe.
type SandboxConfig struct {
	// Reader reads the marker. Required; a nil reader makes every answer an
	// error, which the reconciler treats as unknown.
	Reader MarkerReader
	// TTL is how old a marker may be and still count as alive. Zero uses
	// DefaultTTL. A negative value disables the age check (any marker counts),
	// which no deployment should want — it is here so a test can isolate the
	// presence rule.
	TTL time.Duration
	// Now overrides the clock. Tests set it.
	Now func() time.Time
}

// Sandbox is the container providers' liveness source: a heartbeat marker the
// agent's launcher keeps fresh inside the sandbox workspace.
type Sandbox struct {
	cfg SandboxConfig
}

var _ interface {
	Alive(ctx context.Context, sessionID string) (bool, error)
} = (*Sandbox)(nil)

// NewSandbox builds the marker-backed probe.
func NewSandbox(cfg SandboxConfig) *Sandbox {
	if cfg.TTL == 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Sandbox{cfg: cfg}
}

// Alive reports whether the sandbox holds a fresh marker for sessionID.
//
// A missing marker is a definitive "dead": the launcher creates the file before
// it starts the agent and removes it when the agent exits, so a missing file
// means no launcher-run agent exists for this session in this sandbox. A reader
// that cannot answer — the sandbox is unreachable, the shell call failed, the
// session id is unusable — is an error, and the reconciler turns that into
// "unknown" rather than "dead".
func (s *Sandbox) Alive(ctx context.Context, sessionID string) (bool, error) {
	if s == nil || s.cfg.Reader == nil {
		return false, errors.New("liveness: no sandbox marker reader is configured")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	state, err := s.cfg.Reader.Marker(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("liveness: read the marker for session %s: %w", sessionID, err)
	}
	if !state.Present {
		return false, nil
	}
	if state.ModTime.IsZero() {
		return false, fmt.Errorf("liveness: session %s has a marker with no modification time", sessionID)
	}
	if s.cfg.TTL < 0 {
		return true, nil
	}
	age := s.cfg.Now().Sub(state.ModTime)
	// A marker in the future is a clock skew, not a dead agent: both clocks read
	// the same kernel or a host the operator already trusts for scheduling, and
	// interrupting on skew would be the mistake this package exists to avoid.
	if age <= s.cfg.TTL {
		return true, nil
	}
	return false, nil
}

// MarkerRelPath is the marker's path relative to the sandbox workspace, with
// forward slashes so the container path and the host path agree.
func MarkerRelPath(sessionID string) (string, error) {
	if err := validateSessionID(sessionID); err != nil {
		return "", err
	}
	return path.Join(MarkerDir, sessionID), nil
}

// MarkerPath is the marker's path on a filesystem whose root is the sandbox
// workspace: the host's <sandbox>/... for the launcher, /workspace/... inside
// the container for the probe.
func MarkerPath(root, sessionID string) (string, error) {
	rel, err := MarkerRelPath(sessionID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(root) == "" {
		return "", errors.New("liveness: a sandbox workspace root is required")
	}
	return filepath.Join(root, filepath.FromSlash(rel)), nil
}

var unsafeSessionIDChar = func(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	case r == '-', r == '_', r == '.':
		return false
	default:
		return true
	}
}

// validateSessionID keeps the marker a single path segment under MarkerDir: the
// id comes from the database, and a marker path must never be able to escape the
// sandbox it belongs to. Rejecting here turns a bad id into an error, which the
// reconciler reads as "unknown" — never as "dead".
func validateSessionID(sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("liveness: a session id is required")
	}
	if sessionID == "." || sessionID == ".." || len(sessionID) > 128 {
		return fmt.Errorf("liveness: %q is not a usable session id", sessionID)
	}
	for _, r := range sessionID {
		if unsafeSessionIDChar(r) {
			return fmt.Errorf("liveness: session id %q contains %q", sessionID, string(r))
		}
	}
	return nil
}
