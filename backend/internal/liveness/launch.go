package liveness

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"time"
)

// LauncherCommand is the argv[1] that turns the control-plane binary into the
// agent launcher. It is a mode of the same executable rather than a second
// artifact so every deployment that can run the control plane can already
// launch an agent with a marker: no extra file to install, no extra file to
// forget to install.
const LauncherCommand = "agent-launcher"

// SupervisorConfig describes the marker one agent process owns.
type SupervisorConfig struct {
	// Root is the sandbox workspace the marker lives in. It is the host path of
	// <sandbox> — the same directory the container mounts at /workspace — so the
	// launcher writes the file the probe reads through the sandbox API.
	Root string
	// SessionID names the marker inside Root. Required.
	SessionID string
	// Interval is how often the marker is rewritten while the agent runs. Zero
	// uses DefaultBeat.
	Interval time.Duration
	// Now overrides the clock. Tests set it.
	Now func() time.Time
	// Logger receives the launcher's own failures. Defaults to slog.Default().
	Logger *slog.Logger
	// Stdin/Stdout/Stderr are handed to the agent. Nil means inherit this
	// process's own — which is what passes the RPC pipe of the bridge straight
	// through the launcher to Pi.
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

func (c SupervisorConfig) normalise() (SupervisorConfig, error) {
	if err := validateSessionID(c.SessionID); err != nil {
		return SupervisorConfig{}, err
	}
	if strings.TrimSpace(c.Root) == "" {
		return SupervisorConfig{}, errors.New("liveness: the launcher needs the sandbox workspace root")
	}
	if c.Interval <= 0 {
		c.Interval = DefaultBeat
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Stdin == nil {
		c.Stdin = os.Stdin
	}
	if c.Stdout == nil {
		c.Stdout = os.Stdout
	}
	if c.Stderr == nil {
		c.Stderr = os.Stderr
	}
	return c, nil
}

// Supervise runs argv as the agent and keeps this session's heartbeat marker
// fresh for as long as that process is alive.
//
// The launcher is deliberately a separate process from the control plane: the
// marker has to describe the agent, not the control plane. When the control
// plane restarts, this process and the agent keep running, so the marker keeps
// being written and the new control plane's probe sees a live remote session
// instead of an orphan. When the agent exits, the marker is removed, so the next
// probe says "dead" without waiting for the lease to run out.
//
// The agent inherits the launcher's stdio, so the RPC stream is unchanged: the
// bridge writes to the launcher's stdin and reads the launcher's stdout, and Pi
// holds the same two file descriptors.
func Supervise(ctx context.Context, cfg SupervisorConfig, argv []string) (int, error) {
	cfg, err := cfg.normalise()
	if err != nil {
		return 0, err
	}
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return 0, errors.New("liveness: the launcher needs an agent command to run")
	}
	marker, err := MarkerPath(cfg.Root, cfg.SessionID)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(markerDir(marker), 0o755); err != nil {
		return 0, fmt.Errorf("liveness: create the marker directory for session %s: %w", cfg.SessionID, err)
	}

	// CommandContext, not Command: a cancelled deployment must take the agent
	// with it. On Linux the child also dies with this process (see
	// childProcAttr), which is what keeps a killed launcher from leaving an
	// unmarked agent behind.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = cfg.Stdin, cfg.Stdout, cfg.Stderr
	cmd.SysProcAttr = childProcAttr()
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("liveness: start the agent %q: %w", argv[0], err)
	}

	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	if err := writeMarker(marker, cfg.Now(), pid); err != nil {
		// Without a marker the probe cannot see this agent, so running it would
		// only produce an unmarked remote session. Fail loudly instead.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return 0, err
	}

	stop := make(chan struct{})
	var beats sync.WaitGroup
	beats.Add(1)
	go func() {
		defer beats.Done()
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := writeMarker(marker, cfg.Now(), pid); err != nil {
					// One missed beat is not fatal: the lease is four beats
					// long. Keep the agent; say so once per failure.
					cfg.Logger.Warn("liveness: could not refresh the agent heartbeat",
						"session", cfg.SessionID, "marker", marker, "error", err.Error())
				}
			}
		}
	}()

	waitErr := cmd.Wait()
	close(stop)
	beats.Wait()
	// The agent is gone; so is its claim to be alive. Remove the marker so the
	// very next probe answers "dead" instead of waiting TTL out.
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		cfg.Logger.Warn("liveness: could not remove the agent heartbeat",
			"session", cfg.SessionID, "marker", marker, "error", err.Error())
	}

	var exit *exec.ExitError
	switch {
	case waitErr == nil:
		return 0, nil
	case errors.As(waitErr, &exit):
		return exit.ExitCode(), nil
	default:
		return 0, fmt.Errorf("liveness: wait for the agent: %w", waitErr)
	}
}

// MaybeLaunch is the entry point the control-plane binary calls first: when argv
// asks for the launcher it runs the agent and returns its exit code with
// handled=true, and the server never starts.
func MaybeLaunch(ctx context.Context, argv []string) (code int, handled bool) {
	if len(argv) < 2 || argv[1] != LauncherCommand {
		return 0, false
	}
	flags := flag.NewFlagSet(LauncherCommand, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	root := flags.String("root", "", "sandbox workspace the heartbeat marker lives in")
	session := flags.String("session", "", "session id the marker names")
	interval := flags.Duration("interval", DefaultBeat, "how often the marker is rewritten while the agent runs")
	if err := flags.Parse(argv[2:]); err != nil {
		return 2, true
	}
	if len(flags.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "liveness: agent-launcher needs an agent command after --")
		return 2, true
	}
	signals, stop := signal.NotifyContext(ctx, terminationSignals()...)
	defer stop()
	code, err := Supervise(signals, SupervisorConfig{
		Root:      *root,
		SessionID: *session,
		Interval:  *interval,
		Logger:    slog.Default(),
	}, flags.Args())
	if err != nil {
		slog.Error("liveness: agent launcher failed", "session", *session, "error", err.Error())
		return 1, true
	}
	return code, true
}

// writeMarker stamps the heartbeat file. Content is for a human reading the file
// during an incident; the probe only uses the modification time, which is what a
// heartbeat *is*.
func writeMarker(marker string, now time.Time, pid int) error {
	payload := fmt.Sprintf("%s pid=%d\n", now.UTC().Format(time.RFC3339Nano), pid)
	if err := os.WriteFile(marker, []byte(payload), 0o644); err != nil {
		return fmt.Errorf("liveness: write the heartbeat marker %s: %w", marker, err)
	}
	return nil
}

func markerDir(marker string) string {
	for i := len(marker) - 1; i >= 0; i-- {
		if os.IsPathSeparator(marker[i]) {
			return marker[:i]
		}
	}
	return "."
}
