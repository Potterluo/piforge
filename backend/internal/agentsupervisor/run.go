package agentsupervisor

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"
)

// The three commands, parsed and dispatched here so cmd/piforged's main()
// dispatches once, before its own flag set exists.
//
//	agent-supervisor       -root R -session S [-socket P] [-stderr-log P] [-backlog N] -- <agent argv…>
//	agent-supervisor-start -root R -session S [-socket P] [-stderr-log P] [-wait 30s]  -- <agent argv…>
//	agent-attach           -socket P [-session S]
//
// Exit codes are part of the contract, because docker exec reports them and the
// control plane decides what to do next from whether a handshake arrived:
//
//	0   the agent exited cleanly / the attach spliced until both sides finished
//	1   the agent exited with a failure, or the supervisor failed
//	2   usage error
//	3   the socket is held by somebody else (a supervisor already exists)
const (
	ExitUsage      = 2
	ExitBusy       = 3
	ExitNoSupervis = 4
)

// MaybeRun handles the three agent modes, returning handled=false for any other
// argv so the daemon proceeds normally.
func MaybeRun(ctx context.Context, argv []string) (code int, handled bool) {
	if len(argv) < 2 {
		return 0, false
	}
	switch argv[1] {
	case SupervisorCommand:
		return runSupervisorMode(ctx, argv[2:]), true
	case StartCommand:
		return runStartMode(ctx, argv[2:]), true
	case AttachCommand:
		return runAttachMode(argv[2:]), true
	default:
		return 0, false
	}
}

func runSupervisorMode(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet(SupervisorCommand, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "sandbox workspace the agent and its socket live in")
	session := fs.String("session", "", "session id this agent serves")
	socket := fs.String("socket", "", "Unix socket to listen on (default <root>/.piforge/run/<session>.sock)")
	stderrLog := fs.String("stderr-log", "", "file the agent's stderr is written to (default <root>/.piforge/logs/<session>.log)")
	backlog := fs.Int("backlog", DefaultBacklog, "bytes of agent output kept for a reattaching control plane")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	agentArgv := fs.Args()
	if len(agentArgv) == 0 {
		fmt.Fprintln(os.Stderr, "agentsupervisor: agent-supervisor needs the agent command after --")
		return ExitUsage
	}
	// SIGTERM/SIGINT (docker stop, a reaper inside the container) end the agent;
	// a closed *client* connection does not, which is the whole point.
	signals, stop := signal.NotifyContext(ctx, terminationSignals()...)
	defer stop()

	err := Run(signals, Config{
		Root:      *root,
		SessionID: *session,
		Socket:    *socket,
		StderrLog: *stderrLog,
		Backlog:   *backlog,
		Logger:    slog.Default(),
	}, agentArgv)
	if err == nil {
		return 0
	}
	var busy busyError
	if errors.As(err, &busy) {
		slog.Error("agentsupervisor: another supervisor owns this socket", "socket", busy.socket, "error", err.Error())
		return ExitBusy
	}
	slog.Error("agentsupervisor: supervisor stopped", "session", *session, "error", err.Error())
	return 1
}

// busyError marks "somebody is already listening", which the caller reports as
// its own exit code rather than as a generic failure.
type busyError struct{ socket string }

func (b busyError) Error() string { return "another supervisor is already listening on " + b.socket }

func runStartMode(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet(StartCommand, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "sandbox workspace the agent and its socket live in")
	session := fs.String("session", "", "session id this agent serves")
	socket := fs.String("socket", "", "Unix socket the supervisor listens on")
	stderrLog := fs.String("stderr-log", "", "file the agent's stderr is written to")
	backlog := fs.Int("backlog", DefaultBacklog, "bytes of agent output kept for a reattaching control plane")
	wait := fs.Duration("wait", DefaultReadyTimeout, "how long to wait for a fresh supervisor to answer")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	agentArgv := fs.Args()
	if len(agentArgv) == 0 {
		fmt.Fprintln(os.Stderr, "agentsupervisor: agent-supervisor-start needs the agent command after --")
		return ExitUsage
	}
	if err := ValidateSessionID(*session); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return ExitUsage
	}
	if strings.TrimSpace(*root) == "" {
		fmt.Fprintln(os.Stderr, "agentsupervisor: agent-supervisor-start needs -root")
		return ExitUsage
	}
	sock := *socket
	if strings.TrimSpace(sock) == "" {
		sock = SocketPath(*root, *session)
	}
	if !socketReachable(sock) {
		if err := startDetached(SupervisorConfig{
			Root:      *root,
			Session:   *session,
			Socket:    sock,
			StderrLog: *stderrLog,
			Backlog:   *backlog,
		}, agentArgv); err != nil {
			fmt.Fprintln(os.Stderr, "agentsupervisor: could not start a supervisor:", err)
			return 1
		}
		deadline := time.Now().Add(*wait)
		for !socketReachable(sock) {
			if time.Now().After(deadline) {
				fmt.Fprintln(os.Stderr, "agentsupervisor: the supervisor did not open", sock, "in time")
				return 1
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	// One dial, then the same splice as agent-attach: a first launch and a
	// reattach look identical to the caller.
	if err := Attach(sock, *session, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "agentsupervisor:", err)
		return 1
	}
	return 0
}

func runAttachMode(args []string) int {
	fs := flag.NewFlagSet(AttachCommand, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	socket := fs.String("socket", "", "Unix socket the supervisor listens on")
	session := fs.String("session", "", "session id to verify against the supervisor's handshake")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if strings.TrimSpace(*socket) == "" {
		fmt.Fprintln(os.Stderr, "agentsupervisor: agent-attach needs -socket")
		return ExitUsage
	}
	if err := Attach(*socket, *session, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "agentsupervisor:", err)
		return ExitNoSupervis
	}
	return 0
}

// SupervisorConfig is what the detached child is started with.
type SupervisorConfig struct {
	Root      string
	Session   string
	Socket    string
	StderrLog string
	Backlog   int
}

// startDetached starts `piforged agent-supervisor …` in its own session with
// stdio on a log file, so nothing that happens to this process (the docker exec
// stream closing, a signal to its process group) reaches the agent.
func startDetached(cfg SupervisorConfig, agentArgv []string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve this executable: %w", err)
	}
	root := cfg.Root
	if root == "" {
		return errors.New("no workspace root")
	}
	supervisorLog := SupervisorLogPath(root, cfg.Session)
	if err := os.MkdirAll(dirOf(supervisorLog), 0o755); err != nil {
		return fmt.Errorf("create the log directory: %w", err)
	}
	logFile, err := openLog(supervisorLog, maxLogBytes)
	if err != nil {
		return fmt.Errorf("open the supervisor log: %w", err)
	}
	defer logFile.Close()

	args := []string{
		SupervisorCommand,
		"-root", cfg.Root,
		"-session", cfg.Session,
		"-socket", cfg.Socket,
		"-backlog", fmt.Sprint(cfg.Backlog),
	}
	if cfg.StderrLog != "" {
		args = append(args, "-stderr-log", cfg.StderrLog)
	}
	args = append(args, "--")
	args = append(args, agentArgv...)

	cmd := execCommand(self, args)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachedProcAttr()
	// The environment is inherited on purpose: this process was started by the
	// runner with the session's PI_CODING_AGENT_DIR, HOME and gateway token, and
	// the supervisor only has to pass it on to the agent.
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the supervisor: %w", err)
	}
	// Never Wait: the supervisor is meant to outlive this process, and waiting
	// would make it a child whose exit is our exit. Releasing lets the OS reap it
	// when it is done.
	return cmd.Process.Release()
}

func dirOf(p string) string {
	if i := strings.LastIndexAny(p, "/\\"); i > 0 {
		return p[:i]
	}
	return "."
}
