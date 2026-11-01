// Command piforged is the PiForge control plane.
//
// It owns every piece of state: users, projects, sessions, the append-only
// event log and the SSE fan-out. It also owns the only two things Pi refuses to
// do — sandboxes and credentials — but it never executes agent output itself;
// that happens inside a sandbox process.
//
// Wiring lives here rather than in an init graph so the dependency order is
// readable top to bottom: store → sandbox → git/fs → runtime → auth → router.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/piforge/piforge/backend/internal/agentsupervisor"
	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/bootstrap"
	"github.com/piforge/piforge/backend/internal/browser"
	"github.com/piforge/piforge/backend/internal/capabilities"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/crypto"
	"github.com/piforge/piforge/backend/internal/diskquota"
	"github.com/piforge/piforge/backend/internal/fsx"
	"github.com/piforge/piforge/backend/internal/gitx"
	"github.com/piforge/piforge/backend/internal/httpapi"
	"github.com/piforge/piforge/backend/internal/isolate"
	"github.com/piforge/piforge/backend/internal/janitor"
	"github.com/piforge/piforge/backend/internal/liveness"
	"github.com/piforge/piforge/backend/internal/llmgw"
	"github.com/piforge/piforge/backend/internal/materialize"
	"github.com/piforge/piforge/backend/internal/mcpstage"
	"github.com/piforge/piforge/backend/internal/pi"
	"github.com/piforge/piforge/backend/internal/pimodel"
	"github.com/piforge/piforge/backend/internal/provision"
	"github.com/piforge/piforge/backend/internal/quotaenforcer"
	"github.com/piforge/piforge/backend/internal/reaper"
	"github.com/piforge/piforge/backend/internal/reconcile"
	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/snapshot"
	"github.com/piforge/piforge/backend/internal/store"
	"github.com/piforge/piforge/backend/internal/tenantlimits"
	"github.com/piforge/piforge/backend/internal/term"
	"github.com/piforge/piforge/backend/internal/workspaceremote"
)

func main() {
	// The agent launcher is a mode of this binary: `piforged agent-launcher
	// -root <sandbox> -session <id> -- <agent argv>`. It runs before flag parsing
	// because its argv belongs to the agent, not to the server. See
	// internal/liveness for why a marker needs its own process.
	if code, handled := liveness.MaybeLaunch(context.Background(), os.Args); handled {
		os.Exit(code)
	}
	// The stage-3 supervisor family: `agent-supervisor` (the detached process
	// that owns an agent inside the sandbox), `agent-supervisor-start` (start it
	// if needed, then attach) and `agent-attach` (connect to one that is already
	// there). All three run *inside the sandbox*; see internal/agentsupervisor.
	if code, handled := agentsupervisor.MaybeRun(context.Background(), os.Args); handled {
		os.Exit(code)
	}
	// The in-sandbox disk enforcer: `quota-enforcer -limit <bytes> -root
	// /workspace`. It is started by the sandbox image at boot (deploy/
	// quota-enforcer.sh, RUN_HOOK_PRE_SERVICES), not by the control plane, which
	// is why it lives in this binary and not behind a provider call: a runner
	// deployment has no exec path (docs/SANDBOX-DEPLOY.md §6).
	if code, handled := quotaenforcer.MaybeRun(context.Background(), os.Args); handled {
		os.Exit(code)
	}
	if err := run(); err != nil {
		slog.Error("piforged exited with an error", "err", err)
		os.Exit(1)
	}
}

type options struct {
	addr       string
	dataDir    string
	dev        bool
	useFakePi  bool
	fakePiBin  string
	piBin      string
	logLevel   string
	seedAdmin  string
	seedDemo   string
	secureCook bool
	// gatewayURL is the base URL the agent is told to use for model traffic.
	// It must be reachable from inside the sandbox, which is why it is
	// configurable rather than derived from the listen address.
	gatewayURL      string
	defaultProvider string
	// chrome is the browser panel's executable. Empty disables the feature,
	// which the API reports as a typed 422 instead of a broken socket.
	chrome          string
	headlessBrowser bool
	// isolation names the process jail for agent processes.
	isolation       string
	isolationBinary string
	// browserProxy is handed to Chrome via --proxy-server.
	browserProxy string
	// browserIdleTimeout stops a sandbox browser that no viewer has been
	// attached to for this long. Zero uses the manager's 60s default.
	browserIdleTimeout time.Duration
	// browserTakeoverTTL is how long a browser takeover lease survives without
	// a heartbeat (FR-11.3). Zero uses the httpapi default.
	browserTakeoverTTL time.Duration
	// agentIdleTTL is how long an agent process may sit unused before it is stopped.
	agentIdleTTL time.Duration
	// sandboxIdlePause / sandboxReclaimAfter are the two FR-5.3 thresholds.
	// Zero disables each stage.
	sandboxIdlePause    time.Duration
	sandboxReclaimAfter time.Duration
	// sandboxReapInterval is how often the sandbox reaper looks for candidates.
	sandboxReapInterval time.Duration
	// sandboxSweep is how often orphaned sandbox directories are swept. Zero
	// disables the sweep; the local provider always runs one at startup unless
	// this is zero.
	sandboxSweep time.Duration
	// sandboxBrowser is "auto" (the flag's default), "host" or "remote". It holds
	// the resolved placement by the time anything reads it: auto follows the
	// provider, which is remote under docker and host under local.
	sandboxBrowser string
	// sandboxTerminal behaves like sandboxBrowser: remote runs the shell inside the
	// docker sandbox instead of on the host.
	sandboxTerminal string
	// sandboxFiles behaves like sandboxBrowser: remote reads and writes the
	// workspace through the sandbox's file API instead of the host directory.
	sandboxFiles  string
	sandboxAPIKey string
	// sandboxKind picks the sandbox provider: "local" (directories), "docker"
	// (this process runs the Docker CLI) or "runner" (a sandbox-runner service
	// runs it; this process speaks HTTP).
	sandboxKind    string
	sandboxImage   string
	sandboxNetwork string
	sandboxCPUs    string
	sandboxMemory  string
	// sandboxPids caps a sandbox's process count (docker --pids-limit). The three
	// limits are the *default* every tenant gets; -tenant-limits-file and
	// -tenant-max-sandboxes decide per tenant (internal/tenantlimits).
	sandboxPids int
	// sandboxNetworkMode is "shared" (every sandbox on -sandbox-network) or
	// "tenant" (a bridge per tenant, which is deny-by-default between tenants).
	sandboxNetworkMode string
	// sandboxJoinContainers names the containers joined to every tenant bridge:
	// the control plane and, under the runner provider, the runner itself.
	sandboxJoinContainers []string
	// sandboxJoinContainersRaw is what the flag carries; the list is parsed once,
	// at startup, so a trailing comma is not a container named "".
	sandboxJoinContainersRaw string
	// sandboxTenantNetworkPrefix overrides the name prefix of a tenant's bridge.
	sandboxTenantNetworkPrefix string
	// tenantLimitsFile is the JSON policy named by PIFORGE_TENANT_LIMITS_FILE.
	tenantLimitsFile string
	// tenantMaxSandboxes is how many sandboxes one tenant may have at once (0
	// disables the guard). A per-tenant entry in the file overrides it.
	tenantMaxSandboxes int
	// tenantEgressFile is the JSON policy named by PIFORGE_TENANT_EGRESS_FILE: what
	// each tenant's sandbox may reach beyond the control plane and the gateway.
	tenantEgressFile string
	// tenantEgressMode is the deployment-wide fallback mode ("none" or "open") used
	// when no file is configured. "allowlist" is refused at startup: this release
	// cannot enforce a hostname filter on a bridge (docs/EXECUTION-BOUNDARY.md §12.5).
	tenantEgressMode string
	// agentInSandbox is gone as a *choice* (stage 4 of
	// docs/EXECUTION-BOUNDARY.md): a container placement always runs the agent
	// inside its sandbox, and -sandbox local always runs it here. The old knob is
	// still registered, and only so that a deployment which turns isolation off
	// gets a refusal naming the flag instead of a silent downgrade; see
	// rejectDisabledIsolation.
	agentInSandbox bool
	// sandboxAgentLauncher and sandboxAgentPi name the binaries *inside* the
	// sandbox: the control-plane binary that provides `agent-launcher` (the
	// heartbeat supervisor) and the agent itself.
	sandboxAgentLauncher string
	sandboxAgentPi       string
	// sandboxRunnerURL and sandboxRunnerToken address the sandbox-runner service
	// under -sandbox runner; they are rejected under any other provider.
	sandboxRunnerURL   string
	sandboxRunnerToken string
	// webUpstream makes this process serve the UI as well as the API, which is
	// how a container exposes a single port.
	webUpstream string
	// parallelLimit caps how many branches of one session run at once (FR-3.3).
	// It is validated before anything is created; see resolveParallelLimit.
	parallelLimit int
	// sandboxDiskQuota is the per-sandbox disk budget in bytes (FR-5.8). Zero
	// disables enforcement; the admin report still measures.
	sandboxDiskQuota int64
	// gatewayTokenTTL is how long a sandbox's model gateway token lives. The
	// default (internal/llmgw.DefaultTTL) is one hour; a deployment whose agent
	// turns legitimately run longer can raise it, at the cost of a longer window
	// in which a leaked token is spendable.
	gatewayTokenTTL time.Duration
	// skillMaxBytes caps the total a single skill import may expand (FR-5.8).
	skillMaxBytes int64
	// sessionReconcileInterval is how often the watchdog compares persisted
	// "running" rows with the in-process agent registry. Zero keeps the boot
	// pass but disables the periodic one.
	sessionReconcileInterval time.Duration
}

// defineParallelLimit registers the FR-3.3 knob on fs. It lives in one function so
// the daemon and the flag-resolution test register the very same flag.
func defineParallelLimit(fs *flag.FlagSet, dst *int) {
	fs.IntVar(dst, "parallel-limit", httpapi.DefaultParallelLimit,
		fmt.Sprintf("max branches of one session running at once, 1-%d (env PIFORGE_PARALLEL_LIMIT)", httpapi.MaxParallelLimit))
}

// resolveParallelLimit reads the parsed flag plus the environment into the value
// the router enforces. The flag wins when it was passed explicitly; otherwise
// PIFORGE_PARALLEL_LIMIT supplies the value. A 0, an out-of-range number or a
// non-integer is a startup error rather than a silent default: an operator who
// configured a cap must be able to trust that this deployment runs with it.
func resolveParallelLimit(fs *flag.FlagSet, value *int) (int, error) {
	setByFlag := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "parallel-limit" {
			setByFlag = true
		}
	})
	return httpapi.ResolveParallelLimit(*value, setByFlag, os.Getenv("PIFORGE_PARALLEL_LIMIT"))
}

// resolveByteBudget resolves a byte-valued budget from the flag, the environment
// or the value the flag was registered with.
//
// It is deliberately stricter than envInt: a budget of 0 means "disabled" and is
// a real setting, so it must not be silently replaced by the default, and a typo
// in the environment and a negative number are startup errors. An operator who
// configured a ceiling has to be able to trust that the deployment runs with it.
func resolveByteBudget(fs *flag.FlagSet, name, envKey string, value int64, allowZero bool) (int64, error) {
	setByFlag := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			setByFlag = true
		}
	})
	raw := ""
	if !setByFlag {
		raw = strings.TrimSpace(os.Getenv(envKey))
	}
	if raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s=%q is not a number of bytes", envKey, raw)
		}
		value = n
	}
	if value < 0 {
		return 0, fmt.Errorf("%s must not be negative (0 disables)", name)
	}
	if value == 0 && !allowZero {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return value, nil
}

func run() error {
	var opt options
	flag.StringVar(&opt.addr, "addr", envOr("PIFORGE_ADDR", "127.0.0.1:8080"), "listen address")
	flag.StringVar(&opt.dataDir, "data", envOr("PIFORGE_DATA", "./data"), "data directory")
	flag.BoolVar(&opt.dev, "dev", false, "development mode: verbose logging, insecure cookies over http")
	flag.BoolVar(&opt.useFakePi, "fake-pi", false, "drive the built-in fake Pi peer instead of a real pi binary")
	flag.StringVar(&opt.fakePiBin, "fake-pi-bin", "", "path to the fake Pi binary (optional)")
	flag.StringVar(&opt.piBin, "pi", envOr("PIFORGE_PI", "pi"), "path to the real pi binary")
	flag.StringVar(&opt.logLevel, "log", envOr("PIFORGE_LOG", "info"), "log level: debug|info|warn|error")
	flag.StringVar(&opt.seedAdmin, "seed-admin-email", envOr("PIFORGE_SEED_EMAIL", ""), "seed admin email")
	flag.StringVar(&opt.seedDemo, "seed-demo-email", envOr("PIFORGE_SEED_USER_EMAIL", ""), "seed demo user email")
	flag.BoolVar(&opt.secureCook, "secure-cookies", false, "mark session cookies Secure (required behind TLS)")
	flag.StringVar(&opt.webUpstream, "web-upstream", envOr("PIFORGE_WEB_UPSTREAM", ""),
		"Next.js server to proxy page requests to (single-port deployment)")
	flag.StringVar(&opt.gatewayURL, "gateway-url", envOr("PIFORGE_GATEWAY_URL", ""),
		"public base URL of the model gateway handed to agents (default http://<addr>)")
	flag.StringVar(&opt.defaultProvider, "default-provider", envOr("PIFORGE_DEFAULT_PROVIDER", "shared-anthropic"),
		"provider used when a session has no model_ref")
	defineParallelLimit(flag.CommandLine, &opt.parallelLimit)
	// FR-5.8: the two resource guards. Bytes rather than a human size so the
	// value the server compares against is the value the operator wrote; the
	// help text says what the default is in human terms.
	flag.Int64Var(&opt.sandboxDiskQuota, "sandbox-disk-quota",
		diskquota.DefaultQuotaBytes,
		"per-sandbox disk budget in bytes, measured on the checkout; 0 disables (env PIFORGE_SANDBOX_DISK_QUOTA, default 2 GiB)")
	flag.DurationVar(&opt.gatewayTokenTTL, "gateway-token-ttl", envDuration("PIFORGE_GATEWAY_TOKEN_TTL", llmgw.DefaultTTL),
		"how long a sandbox's model gateway token lives (env PIFORGE_GATEWAY_TOKEN_TTL; one hour default): it is revoked when the session's agent stops, and this is the backstop for a token that outlived the process")
	flag.Int64Var(&opt.skillMaxBytes, "skill-max-bytes",
		httpapi.DefaultSkillMaxBytes,
		"total bytes one skill import may expand on disk; 0 is refused (env PIFORGE_SKILL_MAX_BYTES, default 32 MiB)")
	flag.DurationVar(&opt.agentIdleTTL, "agent-idle-ttl", envDuration("PIFORGE_AGENT_IDLE_TTL", 30*time.Minute),
		"stop an agent process after it has been idle this long")
	flag.DurationVar(&opt.sandboxIdlePause, "sandbox-idle-pause", envDuration("PIFORGE_SANDBOX_IDLE_PAUSE", 30*time.Minute),
		"pause a READY sandbox after it has been idle this long (0 disables)")
	flag.DurationVar(&opt.sandboxReclaimAfter, "sandbox-reclaim-after", envDuration("PIFORGE_SANDBOX_RECLAIM_AFTER", 336*time.Hour),
		"reclaim a PAUSED sandbox after it has been idle this long, snapshotting first (0 disables)")
	flag.DurationVar(&opt.sandboxReapInterval, "sandbox-reap-interval", envDuration("PIFORGE_SANDBOX_REAP_INTERVAL", time.Minute),
		"how often the sandbox idle reaper checks for candidates")
	flag.DurationVar(&opt.sandboxSweep, "sandbox-sweep", envDuration("PIFORGE_SANDBOX_SWEEP", 10*time.Minute),
		"how often sandbox directories with no project row are removed, at startup and then on this interval (0 disables, local provider only)")
	flag.DurationVar(&opt.sessionReconcileInterval, "session-reconcile-interval",
		envDuration("PIFORGE_SESSION_RECONCILE_INTERVAL", 30*time.Second),
		"how often sessions that claim a run is in flight are checked against the live agent registry (0 disables the periodic pass; the startup pass always runs)")
	flag.StringVar(&opt.sandboxKind, "sandbox", envOr("PIFORGE_SANDBOX", "local"),
		"sandbox provider: local (directories), docker (one container per project, CLI here) or runner (a sandbox-runner service runs the CLI)")
	flag.StringVar(&opt.sandboxBrowser, "sandbox-browser", envOr("PIFORGE_SANDBOX_BROWSER", "auto"),
		"browser placement: auto (container providers: remote, local: host), host, or remote (inside the sandbox container)")
	flag.StringVar(&opt.sandboxTerminal, "sandbox-terminal", envOr("PIFORGE_SANDBOX_TERMINAL", "auto"),
		"terminal placement: auto (container providers: remote, local: host), host, or remote (inside the sandbox container)")
	flag.StringVar(&opt.sandboxFiles, "sandbox-files", envOr("PIFORGE_SANDBOX_FILES", "auto"),
		"workspace files: auto (container providers: remote, local: host), host, or remote (read and written through the sandbox)")
	flag.StringVar(&opt.sandboxAPIKey, "sandbox-api-key", envOr("PIFORGE_SANDBOX_API_KEY", ""),
		"fixed API key for every sandbox (debugging override; empty gives each sandbox its own key)")
	flag.StringVar(&opt.sandboxImage, "sandbox-image", envOr("PIFORGE_SANDBOX_IMAGE", ""),
		"image for the docker sandbox provider")
	flag.StringVar(&opt.sandboxNetwork, "sandbox-network", envOr("PIFORGE_SANDBOX_NETWORK", ""),
		"docker network for sandboxes (empty: bridge)")
	flag.StringVar(&opt.sandboxCPUs, "sandbox-cpus", envOr("PIFORGE_SANDBOX_CPUS", ""),
		"default CPU limit per sandbox, for every tenant without an override (env PIFORGE_SANDBOX_CPUS)")
	flag.StringVar(&opt.sandboxMemory, "sandbox-memory", envOr("PIFORGE_SANDBOX_MEMORY", ""),
		"default memory limit per sandbox, e.g. 2g, for every tenant without an override (env PIFORGE_SANDBOX_MEMORY)")
	flag.IntVar(&opt.sandboxPids, "sandbox-pids", envInt("PIFORGE_SANDBOX_PIDS", 0),
		"default process limit per sandbox (docker --pids-limit), 0 is unlimited; the fork-bomb guard (env PIFORGE_SANDBOX_PIDS)")
	flag.StringVar(&opt.sandboxNetworkMode, "sandbox-network-mode", envOr("PIFORGE_SANDBOX_NETWORK_MODE", "shared"),
		"how sandboxes join the network: shared (all on -sandbox-network) or tenant (a bridge per tenant; deny-by-default between tenants) (env PIFORGE_SANDBOX_NETWORK_MODE)")
	flag.StringVar(&opt.sandboxJoinContainersRaw, "sandbox-join-containers", envOr("PIFORGE_SANDBOX_JOIN_CONTAINERS", ""),
		"comma-separated containers joined to every tenant bridge (the control plane and the runner), required with -sandbox-network-mode tenant (env PIFORGE_SANDBOX_JOIN_CONTAINERS)")
	flag.StringVar(&opt.sandboxTenantNetworkPrefix, "sandbox-tenant-network-prefix", envOr("PIFORGE_SANDBOX_TENANT_NETWORK_PREFIX", ""),
		"name prefix of a tenant's Docker network (default piforge-t-) (env PIFORGE_SANDBOX_TENANT_NETWORK_PREFIX)")
	flag.StringVar(&opt.tenantLimitsFile, "tenant-limits-file", envOr("PIFORGE_TENANT_LIMITS_FILE", ""),
		"JSON file of per-tenant quota overrides; the flags above are its default (env PIFORGE_TENANT_LIMITS_FILE)")
	flag.IntVar(&opt.tenantMaxSandboxes, "tenant-max-sandboxes", envInt("PIFORGE_TENANT_MAX_SANDBOXES", 0),
		"how many sandboxes one tenant may have at once, 0 disables the guard (env PIFORGE_TENANT_MAX_SANDBOXES)")
	flag.StringVar(&opt.tenantEgressFile, "tenant-egress-file", envOr("PIFORGE_TENANT_EGRESS_FILE", ""),
		"JSON file of the per-tenant egress policy (none/allowlist/open, per account); with -sandbox-network-mode tenant its default is closed (env PIFORGE_TENANT_EGRESS_FILE)")
	flag.StringVar(&opt.tenantEgressMode, "tenant-egress-mode", envOr("PIFORGE_TENANT_EGRESS_MODE", ""),
		`fallback egress mode for every tenant when no file is configured: "none" (no internet, the gateway stays reachable) or "open" (explicit); "allowlist" is refused (env PIFORGE_TENANT_EGRESS_MODE)`)
	flag.StringVar(&opt.sandboxRunnerURL, "sandbox-runner-url", envOr("PIFORGE_SANDBOX_RUNNER_URL", ""),
		"base URL of the sandbox-runner service, with -sandbox runner (e.g. http://sandbox-runner:7801)")
	flag.StringVar(&opt.sandboxRunnerToken, "sandbox-runner-token", envOr("PIFORGE_SANDBOX_RUNNER_TOKEN", ""),
		"shared bearer token of the sandbox-runner service, with -sandbox runner")
	// Deprecated, and deliberately still parsed: stage 4 removed the choice, and a
	// deployment that asks for the old, unsandboxed shape must fail loudly rather
	// than run a multi-tenant instance with the agent beside the control plane.
	// Passing `true` is accepted as a no-op so an existing compose file or script
	// keeps working. See rejectDisabledIsolation.
	flag.BoolVar(&opt.agentInSandbox, "agent-in-sandbox", envOr("PIFORGE_AGENT_IN_SANDBOX", "1") == "1",
		"deprecated and always on for docker/runner: the agent runs inside the sandbox. `false` is refused at startup (env PIFORGE_AGENT_IN_SANDBOX)")
	flag.StringVar(&opt.sandboxAgentLauncher, "sandbox-agent-launcher", envOr("PIFORGE_SANDBOX_AGENT_LAUNCHER", "/opt/piforge/piforged"),
		"path of the piforged binary inside the sandbox (it provides agent-launcher: the heartbeat supervisor)")
	flag.StringVar(&opt.sandboxAgentPi, "sandbox-agent-pi", envOr("PIFORGE_SANDBOX_AGENT_PI", "pi"),
		"path of the pi executable inside the sandbox")
	flag.StringVar(&opt.chrome, "chrome", envOr("PIFORGE_CHROME", ""),
		"chrome/chromium binary for the browser panel (empty disables it)")
	flag.BoolVar(&opt.headlessBrowser, "headless-browser", envOr("PIFORGE_HEADLESS_BROWSER", "1") != "0",
		"run the sandbox browser headless")
	flag.StringVar(&opt.browserProxy, "browser-proxy", envOr("PIFORGE_BROWSER_PROXY", ""),
		"proxy the sandbox browser should use (host:port)")
	flag.DurationVar(&opt.browserIdleTimeout, "browser-idle-timeout", envDuration("PIFORGE_BROWSER_IDLE_TIMEOUT", browser.DefaultIdleTimeout),
		"stop a sandbox browser after this long with no viewer attached (0: 60s default, negative disables)")
	flag.DurationVar(&opt.browserTakeoverTTL, "browser-takeover-ttl", envDuration("PIFORGE_BROWSER_TAKEOVER_TTL", 45*time.Second),
		"how long a browser takeover lease lives without a heartbeat (FR-11.3)")
	flag.StringVar(&opt.isolation, "isolation", envOr("PIFORGE_ISOLATION", "none"),
		"process jail for agent processes: none|bubblewrap|firejail")
	flag.StringVar(&opt.isolationBinary, "isolation-binary", envOr("PIFORGE_ISOLATION_BINARY", ""),
		"explicit path to the jail launcher")
	flag.Parse()

	// FR-3.3: the cap is configuration, not a constant. Resolve and validate it
	// before anything is created, so a bad value fails the startup instead of
	// leaving a server that quietly runs with the default.
	parallelLimit, err := resolveParallelLimit(flag.CommandLine, &opt.parallelLimit)
	if err != nil {
		return fmt.Errorf("invalid parallel limit: %w", err)
	}
	// FR-5.8: both budgets are validated before anything is created, for the same
	// reason the parallel limit is.
	if opt.sandboxDiskQuota, err = resolveByteBudget(flag.CommandLine, "sandbox-disk-quota",
		"PIFORGE_SANDBOX_DISK_QUOTA", opt.sandboxDiskQuota, true); err != nil {
		return fmt.Errorf("invalid sandbox disk quota: %w", err)
	}
	if opt.skillMaxBytes, err = resolveByteBudget(flag.CommandLine, "skill-max-bytes",
		"PIFORGE_SKILL_MAX_BYTES", opt.skillMaxBytes, false); err != nil {
		return fmt.Errorf("invalid skill size cap: %w", err)
	}

	// Normalise the provider name before anything compares it: placement resolution
	// ignores case, and the wiring below does not, so "-sandbox Docker" must mean
	// the same thing in both places.
	opt.sandboxKind = normalizedSandboxKind(opt.sandboxKind)
	// The runner credentials and the provider have to agree before anything is
	// created: a typo here would otherwise start a process that looks healthy and
	// fails on the first session.
	if err := validateSandboxFlags(opt.sandboxKind, opt.sandboxRunnerURL, opt.sandboxRunnerToken); err != nil {
		return err
	}
	// Where the agent runs is not a decision any more: a container placement runs
	// it inside the sandbox, and -sandbox local runs it here. What is left is the
	// refusal — the retired switch may be present in an operator's compose file or
	// startup script, and "off" must fail the start instead of quietly producing a
	// multi-tenant instance whose agent can read /data.
	isolationWarning, err := rejectDisabledIsolation(flag.CommandLine, opt.agentInSandbox, opt.sandboxKind)
	if err != nil {
		return err
	}
	opt.agentInSandbox = isContainerSandbox(opt.sandboxKind)

	// Where the browser, the terminal and the file panel look is one decision, not
	// three. Resolve it before anything is created: a contradictory combination
	// must fail here, not surface later as a panel that disagrees with the agent
	// about what is in the workspace.
	resolved, err := resolveSandboxPlacement(opt.sandboxKind, opt.sandboxBrowser, opt.sandboxTerminal, opt.sandboxFiles)
	if err != nil {
		return err
	}
	// From here on the three fields hold resolved values, so every later check is
	// about where a feature actually is, never about how it was requested.
	opt.sandboxBrowser, opt.sandboxTerminal, opt.sandboxFiles =
		string(resolved.Browser), string(resolved.Terminal), string(resolved.Files)

	// The quota policy and the network mode are configuration, and configuration
	// that cannot mean anything must fail the start: a tenant that quietly runs
	// unlimited is the failure mode both of these exist to prevent.
	opt.sandboxJoinContainers = splitList(opt.sandboxJoinContainersRaw)
	if err := validateTenantWiring(opt.sandboxKind, opt.sandboxNetworkMode, opt.sandboxJoinContainers, opt.tenantMaxSandboxes); err != nil {
		return err
	}
	networkMode, err := sandbox.ParseNetworkMode(opt.sandboxNetworkMode)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(opt.dataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	// Stage 2 of the execution boundary removes the shared volume: with a
	// container placement the workspace is a volume of the sandbox's own, so
	// there is no host directory for the file panel (or the skill projection) to
	// read. A deployment that pins the panel to the host would show an empty tree
	// next to a running agent, which is the one combination that must not start.
	if err := validateContainerWorkspace(opt.sandboxKind, opt.sandboxFiles); err != nil {
		return err
	}

	logger := newLogger(opt)
	logger.Info("starting piforged",
		"addr", opt.addr, "data", abs(opt.dataDir), "dev", opt.dev, "fake_pi", opt.useFakePi)
	logger.Info("sandbox placement resolved",
		"sandbox", opt.sandboxKind,
		"browser", opt.sandboxBrowser,
		"terminal", opt.sandboxTerminal,
		"files", opt.sandboxFiles)
	// A retired switch that was exported in this deployment's environment is worth
	// one line: it is one of the few keys in a compose file that no longer does
	// anything (stage 4 of docs/EXECUTION-BOUNDARY.md).
	if isolationWarning != "" {
		logger.Warn(isolationWarning, "sandbox", opt.sandboxKind)
	}
	// The cap is invisible otherwise: parallel runs are opt-in per user, and the
	// only other place it surfaces is a refusal. Say it once at startup.
	logger.Info("parallel branch limit", "branches", parallelLimit, "max", httpapi.MaxParallelLimit)
	// The disk budget is the other invisible guard: it only speaks when it refuses
	// a run, and an operator reading a 507 needs to know what the deployment's
	// value is without guessing at the default.
	quotaLabel := diskquota.HumanBytes(opt.sandboxDiskQuota)
	if opt.sandboxDiskQuota == 0 {
		quotaLabel = "disabled"
	}
	logger.Info("sandbox disk quota",
		"limit", quotaLabel, "bytes", opt.sandboxDiskQuota,
		"skill_import_max", diskquota.HumanBytes(opt.skillMaxBytes), "skill_import_bytes", opt.skillMaxBytes)

	// FR-14.2 without CAP_SYS_ADMIN: the default mechanism is a read-only copy,
	// and the one capability that can silently defeat it is CAP_DAC_OVERRIDE —
	// a root process holding it ignores the 0444 that marks a copied skill
	// read-only. It cannot be given up from inside the process (Linux lets a
	// thread change only its own capability set, and Go runs goroutines on many
	// threads), so the deployment drops it. Say which mechanism this deployment
	// will therefore report, and what to do when it is the unwelcome answer.
	switch {
	case capabilities.HasSysAdmin():
		logger.Info("skill mounts use read-only bind mounts",
			"mechanism", "bind-ro",
			"reason", "CAP_SYS_ADMIN is present: the kernel enforces the mount and sessions share one snapshot")
	case capabilities.DACOverrideEffective():
		logger.Warn("skill mounts will report copy, not copy-ro",
			"reason", "this root process still carries CAP_DAC_OVERRIDE, so 0444 does not stop writes",
			"fix", "start the container with --cap-drop=DAC_OVERRIDE (docker-compose.yml does this by default)")
	default:
		logger.Info("skill mounts use read-only copies",
			"mechanism", "copy-ro",
			"reason", "no CAP_SYS_ADMIN and no CAP_DAC_OVERRIDE: file modes are enforced by the platform")
	}

	// A mixed deployment is allowed, never accidental. Saying it out loud is the
	// difference between a bug report about "the panel shows the wrong file" and a
	// log line that described the deployment before it did any work.
	if resolved.Mixed() {
		logger.Warn("sandbox placement is mixed: these features still run on the host",
			"sandbox", opt.sandboxKind,
			"host", strings.Join(resolved.HostFeatures(), ", "),
			"remote", strings.Join(resolved.RemoteFeatures(), ", "),
			"detail", "the agent works inside the container, so a host-side panel is a second view of one workspace; pass remote for every feature, or use -sandbox local for all of them")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 1. Persistence.
	st, err := store.Open(ctx, filepath.Join(opt.dataDir, "piforge.db"))
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	if err := seed(ctx, st, opt, logger); err != nil {
		return err
	}

	// The tenant quota policy, resolved once: the flags are its default, the file
	// (when configured) its overrides. It is built after the store because a policy
	// may name an account by email, and an unreadable or impossible policy must
	// fail the start here rather than leave a tenant unlimited at the first
	// session.
	defaultLimits := tenantlimits.Limits{
		CPUs:   opt.sandboxCPUs,
		Memory: opt.sandboxMemory,
		Pids:   opt.sandboxPids,
		// The concurrency guard's default. A tenant entry in the file can override
		// it, which is why it is part of the same policy rather than a second flag
		// read in a second place.
		MaxSandboxes: opt.tenantMaxSandboxes,
	}
	tenants, err := newTenantResolver(st, defaultLimits, opt.tenantLimitsFile)
	if err != nil {
		return fmt.Errorf("tenant limits: %w", err)
	}
	// Say the policy once, at boot: every later refusal quotes one of these
	// numbers, and an operator reading a refusal should not have to guess which
	// file produced it. The numbers come from the *resolved* policy, not from the
	// flags: a limits file that overrides the default would otherwise make this
	// line describe a deployment that is not running.
	effective := tenants.policy.Default()
	logger.Info("tenant quota policy",
		"source", tenants.policy.Source(),
		"default_cpus", emptyAs(effective.CPUs, "unlimited"),
		"default_memory", emptyAs(effective.Memory, "unlimited"),
		"default_pids", effective.Pids,
		"default_max_sandboxes", effective.MaxSandboxes,
		"overrides", len(tenants.policy.Tenants()),
		"network_mode", string(networkMode))
	if effective.Sandbox().IsZero() && effective.MaxSandboxes == 0 {
		logger.Warn("no tenant quota is configured: every sandbox may use the whole host",
			"fix", "set -sandbox-cpus/-sandbox-memory/-sandbox-pids and -tenant-max-sandboxes, or -tenant-limits-file")
	}

	// The tenant egress policy, resolved once and checked before any container
	// exists: a mode this deployment cannot apply (an allowlist, "none" on a
	// shared bridge, "none" on the host placement) fails the boot instead of
	// becoming an admin page that describes a boundary nobody enforces.
	egress, err := resolveTenantEgress(opt.tenantEgressFile, opt.tenantEgressMode, networkMode)
	if err != nil {
		return fmt.Errorf("tenant egress: %w", err)
	}
	if err := checkTenantEgress(opt.sandboxKind, networkMode, egress); err != nil {
		return fmt.Errorf("tenant egress: %w", err)
	}
	tenants.egress = egress
	// Only wire the provider when a policy exists: a nil EgressFor means "this
	// deployment configured nothing" and keeps the historical open shape, while a
	// function that answers nothing means the policy has no answer and closes.
	var egressModeFor func(ctx context.Context, userID string) sandbox.EgressMode
	if egress != nil {
		egressModeFor = tenants.egressFor
		logger.Info("tenant egress policy",
			"source", egress.Source(),
			"default", egress.Default().Describes(),
			"overrides", len(egress.Tenants()),
			"network_mode", string(networkMode),
			"detail", "none = the tenant bridge is created --internal: no route out, the gateway on that bridge stays reachable")
	} else {
		logger.Warn("no tenant egress policy is configured: every sandbox can reach the internet exactly as the host can",
			"fix", "set -tenant-egress-file (the isolated stack mounts deploy/tenant-egress.json) or -tenant-egress-mode",
			"note", "with -sandbox-network-mode tenant the default is closed even without a file")
	}

	// A run left "running" belongs to a command whose output died with the
	// previous process. This daemon is the only writer, so settling it here is
	// honest; leaving it running would make the test panel lie forever.
	if n, err := st.SettleOrphanTestRuns(ctx, time.Now()); err != nil {
		logger.Warn("orphan test runs not settled", "error", err.Error())
	} else if n > 0 {
		logger.Info("settled orphan test runs", "count", n)
	}

	// 2. Secrets and the model gateway.
	//
	// Keys are sealed at rest and only opened here, on the egress path. The
	// agent process never sees a real key: it gets a short-lived signed token
	// and a base URL pointing at this process (PRD §5.7.2).
	box, err := crypto.NewFromEnv(opt.dataDir)
	if err != nil {
		return fmt.Errorf("init secret box: %w", err)
	}

	// A deployment that sets PIFORGE_BOOTSTRAP_PROVIDER should not be dead on
	// arrival: create the shared provider once, never overwrite an existing one,
	// and never put the key in the log (see internal/bootstrap).
	setupDefaults, err := bootstrap.FromEnv()
	if err != nil {
		return fmt.Errorf("bootstrap provider: %w", err)
	}
	if !setupDefaults.Empty() {
		if _, _, berr := bootstrap.Ensure(ctx, st, box, logger, setupDefaults, os.Getenv(bootstrap.EnvKey)); berr != nil {
			// Not fatal: the deployment still works, and an operator can add the
			// provider in the UI. Saying so once beats a crash loop whose message
			// is buried in a restart.
			logger.Warn("bootstrap provider was not created", "error", berr.Error())
		}
	}
	// The deployment's fallback provider is configuration too. Saying it at boot
	// turns "the first send failed for no visible reason" into a log line an
	// operator can act on; it is never fatal, because the provider can be added
	// from the UI while this process runs.
	if warning, werr := defaultProviderWarning(ctx, st, opt.defaultProvider); werr != nil {
		logger.Warn("could not check the default provider", "error", werr.Error())
	} else if warning != "" {
		logger.Warn(warning, "default_provider", opt.defaultProvider)
	}
	// The gateway's HMAC key is *persisted*, and that is a stage-3 requirement,
	// not a convenience. A token is minted when an agent starts and lives in that
	// agent's models.json; an agent that now outlives the control plane (see
	// internal/agentsupervisor) keeps using the token it was started with, so a
	// randomly regenerated key on every boot would make every surviving agent
	// start failing its model calls after a restart — the exact opposite of what
	// the supervisor is for. Stored beside the database, 0600, backed up with it.
	signingKey, err := loadOrCreateSigningKey(filepath.Join(opt.dataDir, "gateway.key"))
	if err != nil {
		return fmt.Errorf("init gateway signer: %w", err)
	}
	logger.Info("model gateway tokens survive a restart",
		"key", abs(filepath.Join(opt.dataDir, "gateway.key")),
		"detail", "the signing key is on the data volume, so an agent that outlived this process keeps working")
	signer, err := llmgw.NewSigner(signingKey, opt.gatewayTokenTTL)
	if err != nil {
		return fmt.Errorf("init gateway signer: %w", err)
	}
	// Revocation is durable, not a process-local set. Adversarial acceptance #2
	// found the difference the hard way: pause revoked the token correctly, and
	// a control-plane restart inside the one-hour TTL re-enabled every token
	// minted before it, because the generation counter started again from zero.
	// store/gateway_revocations.go is the authority now; the signer's map is a
	// fast path over it.
	//
	// The signing key was already persisted for the same class of reason (an
	// agent that outlives this process keeps using its token). The two now
	// agree: the signature survives a restart *and* so does the revocation.
	signer.UseRevocations(st)
	signer.SetLogger(func(format string, args ...any) { logger.Warn(fmt.Sprintf(format, args...)) })
	if count, cerr := st.GatewayRevocationCount(ctx); cerr == nil {
		logger.Info("model gateway revocations are durable",
			"table", "gateway_revocations",
			"on_record", count,
			"detail", "a revoked token stays revoked across a restart; a row is swept when the last token it covers expires")
	}
	// The sweep. A revocation row is needed only while a token minted before it
	// can still be presented (one TTL, plus the extension a resumed session
	// applies), so the table stays proportional to the revocations that are
	// still load-bearing rather than to every stop the deployment ever had.
	go sweepGatewayRevocations(ctx, signer, logger)
	// Say the number out loud: "how long is a leaked sandbox token worth" is a
	// security property of the deployment, and it is now one hour rather than
	// half a day.
	logger.Info("model gateway token lifetime",
		"ttl", signer.TTL().String(),
		"revoked_on", "the session's agent stopping (pause, archive, delete, model switch); not on shutdown, because a container agent outlives this process",
		"detail", "the token is minted per agent spawn and ends with the process; the TTL is the backstop")
	gateway, err := llmgw.New(llmgw.Config{
		Store:  st,
		Box:    box,
		Signer: signer,
		Logger: func(format string, args ...any) { logger.Debug(fmt.Sprintf(format, args...)) },
	})
	if err != nil {
		return fmt.Errorf("init model gateway: %w", err)
	}
	gatewayBase := strings.TrimRight(firstNonEmpty(opt.gatewayURL, "http://"+opt.addr), "/")

	// 3. Sandboxes. The local provider runs on the host; the docker provider runs
	// the Docker CLI here; the runner provider asks a service that owns the CLI.
	// Swapping between them is a flag and one branch here, nowhere else.
	var sb contracts.SandboxProvider
	// localSandboxRoot is set only by the local provider. It is the directory
	// layout the orphan sweep understands (sandboxes/<user>/<project>); a
	// container provider keeps its state somewhere else, so no sweep runs.
	localSandboxRoot := ""
	// remoteSB is the lifecycle-independent half the panels use (the sandbox's API
	// endpoint and the host→container path map). It is nil under -sandbox local,
	// which is how the checks below know there is no container.
	var remoteSB sandbox.Remote
	switch opt.sandboxKind {
	case "docker":
		// One container per (user, project): the isolation boundary matches the tenancy
		// boundary the rest of the product already uses. The AIO image publishes one
		// HTTP port, and the checkout is provisioned through that API (as the sandbox
		// user), so the port is published even when every panel feature stays on the
		// host: without it Ensure could not create /workspace/repo. The browser and
		// terminal reach the same endpoint when they are remote.
		dockerProvider, derr := sandbox.NewDocker(sandbox.DockerConfig{
			Image:    opt.sandboxImage,
			Network:  opt.sandboxNetwork,
			CPUs:     opt.sandboxCPUs,
			Memory:   opt.sandboxMemory,
			Pids:     opt.sandboxPids,
			DiskBytes: opt.sandboxDiskQuota,
			DataRoot: filepath.Join(opt.dataDir, "sandboxes"),
			APIPort:  8080,
			APIKey:   opt.sandboxAPIKey,
			// A per-tenant quota and a per-tenant bridge, both resolved here rather
			// than inside the provider: the policy is the deployment's, and the
			// provider only applies what it is given.
			LimitsFor: tenants.sandboxLimits,
			// And the egress policy, applied when this process creates the
			// tenant's bridge: `--internal` for "none", an ordinary bridge for
			// "open". Nil keeps the historical open shape (logged above).
			EgressFor:           egressModeFor,
			NetworkMode:         networkMode,
			TenantNetworkPrefix: opt.sandboxTenantNetworkPrefix,
			JoinContainers:      opt.sandboxJoinContainers,
			// Stage 2: the agent runs inside the sandbox, so its workspace is a
			// volume of its own that this process never mounts. Stage 4 removed the
			// switch that used to share it: there is one placement for a container
			// provider now.
			DetachedWorkspace: true,
		})
		if derr != nil {
			return fmt.Errorf("init docker sandbox provider: %w", derr)
		}
		sb = dockerProvider
		remoteSB = dockerProvider
	case "runner":
		// The same container rules, performed by a process that owns the Docker
		// socket. This process needs neither the CLI nor a daemon: it has one HTTP
		// contract, a token, and no way to run anything on the host.
		//
		// The DataRoot is this process's view of the shared sandbox directory. The
		// runner reports the same path in RootDir, which is what makes RootDir,
		// ContainerPath and provision's host git keep working unchanged.
		runnerProvider, rerr := sandbox.NewRunner(sandbox.RunnerConfig{
			BaseURL:  opt.sandboxRunnerURL,
			Token:    opt.sandboxRunnerToken,
			DataRoot: filepath.Join(opt.dataDir, "sandboxes"),
			Image:    opt.sandboxImage,
			APIKey:   opt.sandboxAPIKey,
			CPUs:     opt.sandboxCPUs,
			Memory:   opt.sandboxMemory,
			Pids:     opt.sandboxPids,
			// The disk budget travels with the create for the same reason, and it
			// is the number this process measures the workspace against.
			DiskBytes: opt.sandboxDiskQuota,
			// The tenant's limits travel with the create, because the runner is a
			// shared service that must not have to know which tenant it is serving.
			LimitsFor: tenants.sandboxLimits,
			// And the network mode travels too: the runner owns the Docker CLI, so
			// it is the process that creates the per-tenant bridges.
			TenantNetworks: networkMode == sandbox.NetworkTenant,
			// The egress mode travels for the same reason: the bridge the runner
			// creates is the enforcement, and this process is where the policy is
			// read. The runner refuses a mode it cannot apply instead of building
			// a bridge with the wrong posture.
			EgressFor: egressModeFor,
			Network:   opt.sandboxNetwork,
			APIPort:   8080,
			// Stage 2: the create request asks the runner for a workspace volume
			// of this sandbox's own, which no service in the deployment mounts.
			// Stage 4 removed the switch: a runner placement is always detached.
			DetachedWorkspace: true,
		})
		if rerr != nil {
			return fmt.Errorf("init runner sandbox provider: %w", rerr)
		}
		sb = runnerProvider
		remoteSB = runnerProvider
		logger.Info("sandbox lifecycle runs through the runner",
			"url", opt.sandboxRunnerURL,
			"detail", "no docker CLI and no daemon are needed in this process")
	default:
		localRoot := filepath.Join(opt.dataDir, "sandboxes")
		local, lerr := sandbox.NewLocal(sandbox.LocalConfig{
			Root:          localRoot,
			InitialBranch: "main",
		})
		if lerr != nil {
			return fmt.Errorf("init sandbox provider: %w", lerr)
		}
		sb = local
		localSandboxRoot = localRoot
	}

	// Stage 1 of docs/EXECUTION-BOUNDARY.md: the agent process runs *inside* the
	// sandbox, not beside this one. The provider has to be able to attach a byte
	// stream to a process in the container; local has no container, which is why
	// this is the one thing the provider still gets asked for. Stage 4 removed the
	// branch that used to make it a choice: if a container provider cannot attach,
	// there is no fallback to run beside this process any more, so the start fails.
	var agentAttacher sandbox.Attacher
	if opt.agentInSandbox {
		attacher, ok := sb.(sandbox.Attacher)
		if !ok {
			return fmt.Errorf("-sandbox %s has no attach verb, which every container placement needs: the agent runs inside its own sandbox (docs/EXECUTION-BOUNDARY.md §10)", opt.sandboxKind)
		}
		agentAttacher = attacher
	}

	// 3. Git and the filesystem. fsx takes gitx as its git backend so file
	// listings and diffs agree about what is modified.
	git := gitx.New(gitx.Config{})
	fs := fsx.New(fsx.Config{
		MaxEntriesPerDir: 2000,
		SearchLimit:      500,
		Git:              git,
		GitState:         gitLetters(git),
		Audit: func(ev fsx.AuditEvent) {
			logger.Debug("file mutation", "path", ev.Path, "action", ev.Action, "bytes", ev.Size)
		},
	})

	// The panel's file tree, preview and editor read the host directory unless
	// asked otherwise. With a docker sandbox, "otherwise" is the default: the agent
	// edits files inside the container, so the panel must ask the container too
	// or the two views drift. Git goes through the same bridge, because the two
	// sides disagreeing about what is staged is the same bug one layer up.
	//
	// resolveSandboxPlacement already rejected remote without a container, so the
	// only thing left to guard is the provider itself.
	var files contracts.Workspace = fs
	// fileBackend is the sandbox-API view of a checkout. It is built when the file
	// panel is remote (the default under a container provider) and is reused by the
	// provisioner: the skill projection writes through the same client the panel
	// reads with, so there is one answer per sandbox about where the bytes are.
	var fileBackend *sandboxFileBackend
	if opt.sandboxFiles == "remote" {
		if remoteSB == nil {
			return fmt.Errorf("remote workspace files need a container sandbox provider (-sandbox docker or -sandbox runner)")
		}
		fileBackend = &sandboxFileBackend{provider: remoteSB, store: st, keys: sandboxKeys{store: st, fixed: opt.sandboxAPIKey}}
		// OwnedTree follows the workspace shape: a container placement's
		// workspace (stage 2) is built and read by the sandbox user alone, so the
		// panel's git needs no `safe.directory`. Stage 4 removed the shared-volume
		// deployment that was the other case.
		executor := workspaceremote.ShellExecutor{Backend: fileBackend, OwnedTree: true}
		remoteFiles, rerr := workspaceremote.New(workspaceremote.Config{
			Backend: fileBackend,
			// Same runner, same parsing: only the machine executing git changes.
			Git: gitx.New(gitx.Config{Exec: executor.Run}),
			Audit: func(ev fsx.AuditEvent) {
				logger.Debug("file mutation via sandbox", "path", ev.Path, "action", ev.Action, "bytes", ev.Size)
			},
		})
		if rerr != nil {
			return fmt.Errorf("init remote workspace: %w", rerr)
		}
		files = remoteFiles
		logger.Info("workspace files are read and written through the sandbox",
			"sandbox", opt.sandboxKind)
	}

	// 4. The agent runtime.
	launcher, err := isolate.New(isolate.Options{
		Kind:   opt.isolation,
		Binary: opt.isolationBinary,
		// The agent writes its worktree and its session file; everything else
		// on the host is read-only inside the jail.
		WritableDirs: []string{opt.dataDir},
		ShareNetwork: true,
	})
	if err != nil {
		return err
	}
	if launcher.Name() != "none" {
		logger.Info("agent processes run inside a jail",
			"launcher", launcher.Name(),
			"covers", "agent process, terminal shells, sandbox browser")
	}

	// setupFor renders each session's model configuration and the environment
	// that points Pi at the gateway. It runs at spawn time, so a provider change
	// applies to the next session without touching a running sandbox.
	resolvedModels := struct {
		sync.Mutex
		bySession map[string]string
	}{bySession: map[string]string{}}

	setupFor := func(spec contracts.SessionSpec) agentSetup {
		providerName, modelID := splitModelRef(spec.ModelRef, opt.defaultProvider)

		models := []string{}
		api := "openai-completions"
		if provider, _, perr := st.ResolveProvider(ctx, spec.UserID, providerName); perr == nil {
			api = piAPI(provider.API)
			for _, m := range provider.Models {
				if strings.TrimSpace(m.ID) != "" {
					models = append(models, m.ID)
				}
			}
		}
		if modelID != "" {
			// Prepend the pinned model unless the provider already lists it, so
			// models.json does not carry the same id twice.
			dupe := false
			for _, existing := range models {
				if existing == modelID {
					dupe = true
					break
				}
			}
			if !dupe {
				models = append([]string{modelID}, models...)
			}
		}
		if len(models) == 0 {
			logger.Warn("no models are configured for this session's provider",
				"session", spec.SessionID, "provider", providerName)
			return agentSetup{}
		}

		token, terr := signer.Issue(spec.SessionID, spec.UserID, providerName, modelID, time.Now())
		if terr != nil {
			logger.Warn("cannot mint a model gateway token", "session", spec.SessionID, "error", terr.Error())
			return agentSetup{}
		}
		gatewayURL := gatewayBase + "/gw/llm/" + token

		res, werr := pimodel.Write(filepath.Join(opt.dataDir, "pi-agents", spec.SessionID), pimodel.Spec{
			GatewayURL:   gatewayURL,
			Token:        token,
			Models:       models,
			APIKind:      api,
			ProviderName: providerName,
		})
		if werr != nil {
			logger.Warn("cannot write the pi model configuration",
				"session", spec.SessionID, "error", werr.Error())
			return agentSetup{}
		}

		resolvedModels.Lock()
		resolvedModels.bySession[spec.SessionID] = res.Model
		resolvedModels.Unlock()

		// MCP servers reach the agent the only way Pi accepts them: a file in the
		// session's agent directory. For a container placement the file holds a
		// broker URL and a revocable per-session token instead of a credential —
		// see mcpEntriesForSession.
		entries, merr := mcpEntriesForSession(ctx, st, spec, opt.agentInSandbox, gatewayBase, logger)
		if merr != nil {
			logger.Warn("cannot prepare MCP servers for this session",
				"session", spec.SessionID, "error", merr.Error())
		} else if _, werr := pimodel.WriteMCP(res.Dir, entries); werr != nil {
			logger.Warn("cannot write the MCP configuration",
				"session", spec.SessionID, "error", werr.Error())
		}

		// A sidecar shares its parent's checkout, so it must not be able to write
		// to it (FR-4.2). The restriction is passed to Pi as a tool denylist for
		// this run, not merely recorded as an intention.
		args := pi.ArgsForKind(res.Args, spec.Kind)
		// Tell Pi exactly where the platform-mounted skills are. Project-local
		// discovery would need project trust, and the platform does not trust a
		// user's repository with the right to load extensions just to get skills.
		// An explicit `--skill` is the narrow grant: these directories, no more.
		if spec.SkillsDir != "" {
			if info, serr := os.Stat(spec.SkillsDir); serr == nil && info.IsDir() {
				args = append(args, "--skill", spec.SkillsDir)
			}
		}

		return agentSetup{
			Args: args,
			// PI_CODING_AGENT_DIR is the authoritative pointer; the *_BASE_URL
			// variables stay for clients that read them directly.
			//
			// No GIT_CONFIG_* entries: they existed to tell a host-side agent that
			// a container-owned checkout belonged to another account, and stage 4
			// deleted that execution. Inside the sandbox the agent runs as the
			// account that owns the tree, so there is no "dubious ownership" to
			// lift (sandboxAgentEnv used to strip them for that reason).
			Env: []string{
				"PI_CODING_AGENT_DIR=" + res.Dir,
				"ANTHROPIC_BASE_URL=" + gatewayURL,
				"ANTHROPIC_API_KEY=" + token,
				"OPENAI_BASE_URL=" + gatewayURL + "/v1",
				"OPENAI_API_KEY=" + token,
				"PIFORGE_GATEWAY_TOKEN=" + token,
			},
		}
	}

	// Pi knows the model under the generated gateway provider, not under the
	// user's provider name, so the reference has to be translated before it is
	// sent over RPC.
	modelFor := func(spec contracts.SessionSpec, ref string) string {
		resolvedModels.Lock()
		defer resolvedModels.Unlock()
		if model, ok := resolvedModels.bySession[spec.SessionID]; ok && model != "" {
			return model
		}
		return ref
	}

	// One resolver for every sandbox API call this process makes: the file panel,
	// the terminal, the browser, the worktree request and — for a container
	// placement — the model configuration written through the file API all present
	// the same key the container was created with.
	keys := sandboxKeys{store: st, fixed: opt.sandboxAPIKey}

	// Stage 1 of docs/EXECUTION-BOUNDARY.md. For a container placement the agent is
	// a process inside the sandbox and this one only owns its stdio; the heartbeat
	// launcher travels in the sandbox-side argv (agent_sandbox.go). Stage 4 deleted
	// the other arm — wrapping a locally spawned agent in the same launcher — which
	// only existed for the placement that no longer exists.
	var agent *sandboxAgent
	if opt.agentInSandbox {
		agent, err = newSandboxAgent(remoteSB, agentAttacher, keys, st, setupFor, opt, logger)
		if err != nil {
			return err
		}
		logger.Info("the agent runs inside the sandbox",
			"sandbox", opt.sandboxKind,
			"user", agentSandboxUser,
			"pi", opt.sandboxAgentPi,
			"launcher", opt.sandboxAgentLauncher,
			"detail", "RPC over the sandbox exec stream; the provider key never enters the container")
	}

	// The stop hook ends a session's model-gateway token when its agent goes
	// away — with one deliberate exception (a control-plane shutdown, where a
	// container agent is expected to keep running), documented on
	// revocationHook in gateway_revocations.go.
	revocations := &revocationHook{signer: signer}
	rt, err := newRuntime(opt, logger, setupFor, modelFor, launcher, agent, revocations.onStop)
	if err != nil {
		return err
	}
	defer rt.Close()
	// Registered after `defer rt.Close()` so it runs *before* it (defers are
	// LIFO): rt.Close stops every live session, and those stops must not be
	// persisted as revocations for agents that are meant to survive.
	defer revocations.drain()

	// 5. Terminals. The manager owns the PTY processes; the HTTP layer only
	// relays bytes (PRD §7.6.1).
	browserCfg := browser.Config{
		Binary:   opt.chrome,
		Headless: opt.headlessBrowser,
		Proxy:    opt.browserProxy,
		Launcher: launcher,
		// The panel is lazy-mounted, so the only thing that can keep a sandbox
		// Chrome alive after a session page closes is this policy. It is
		// configurable because a test harness wants seconds, not a minute.
		IdleTimeout: opt.browserIdleTimeout,
		Logger:      logger,
	}
	// keys was resolved above, before the agent wiring: the sandbox-side model
	// configuration uses the same per-sandbox key the panels do.
	if remoteSB != nil {
		if strings.TrimSpace(opt.sandboxAPIKey) != "" {
			logger.Warn("sandbox API keys are fixed by -sandbox-api-key for every sandbox; this is for debugging one deployment, not for tenants",
				"sandbox", opt.sandboxKind,
				"detail", "leave -sandbox-api-key empty to let each sandbox get its own key, which is what keeps one leaked key from opening every tenant")
		} else {
			logger.Info("sandbox API keys are per sandbox",
				"sandbox", opt.sandboxKind,
				"detail", "each container gets a 32-byte random key at creation, sealed in the sandboxes row")
		}
	}

	// With a docker sandbox that runs an AIO image, the browser lives in that container:
	// the panel then shows the same machine the agent edits, which is the whole point of
	// moving the sandbox into a container. This is the resolved placement, so it is
	// remote by default under docker — not only when the flag is spelled out.
	if opt.sandboxBrowser == "remote" {
		browserCfg.RemoteAPIKey = opt.sandboxAPIKey
		browserCfg.RemoteKey = keys.For
		browserCfg.Remote = func(ctx context.Context, sandboxID string) (string, string, error) {
			if remoteSB == nil {
				return "", "", fmt.Errorf("remote browser needs a container sandbox provider")
			}
			endpoint, err := remoteSB.Endpoint(ctx, sandboxID)
			if err != nil {
				return "", "", err
			}
			apiKey, kerr := keys.For(ctx, sandboxID)
			if kerr != nil {
				return "", "", kerr
			}
			client, cerr := aio.New(aio.Config{BaseURL: endpoint, APIKey: apiKey})
			if cerr != nil {
				return "", "", cerr
			}
			info, ierr := client.BrowserInfo(ctx)
			if ierr != nil {
				return "", "", ierr
			}
			return info.CDPURL, endpoint, nil
		}
	}
	browsers := browser.NewManager(browserCfg)
	browsers.StartIdleReaper()
	defer browsers.CloseAll()
	if browsers.IdleTimeout() > 0 {
		logger.Info("sandbox browsers are stopped when idle",
			"idle_timeout", browsers.IdleTimeout().String(),
			"detail", "a browser starts only when someone selects the panel; with no viewer attached it is stopped after this window")
	}

	// With a docker sandbox that runs an AIO image, the terminal lives in that
	// container too: the shell sees the same filesystem the agent edits. Like the
	// browser, this is the resolved placement, so docker gets it by default.
	termCfg := term.Config{
		RingBytes: 4 << 20,
		Launcher:  launcher,
		// A terminal outlives the socket that opened it — that is what lets a
		// reconnect replay its scrollback — so a long-lived process accumulates
		// one per tab ever opened. The default cap (64) is a per-node safety
		// limit; the e2e suite opens far more terminals than that in one run,
		// hence the knob.
		MaxSessions: envInt("PIFORGE_TERM_MAX_SESSIONS", 0),
		// The key is a worktree id: the HTTP layer already proved that the
		// caller owns the session which owns this worktree.
		WorkDir: func(ctx context.Context, worktreeID string) (string, error) {
			return st.WorktreePathByID(ctx, worktreeID)
		},
	}
	if opt.sandboxTerminal == "remote" {
		termCfg.Remote = func(ctx context.Context, worktreeID string) (*aio.Client, string, string, error) {
			if remoteSB == nil {
				return nil, "", "", fmt.Errorf("remote terminal needs a container sandbox provider")
			}
			worktree, werr := st.WorktreeByID(ctx, worktreeID)
			if werr != nil {
				logger.Warn("cannot resolve the worktree for a remote terminal",
					"worktree", worktreeID, "error", werr.Error())
				return nil, "", "", werr
			}
			endpoint, eerr := remoteSB.Endpoint(ctx, worktree.SandboxID)
			if eerr != nil {
				logger.Warn("cannot reach the sandbox for a remote terminal; falling back to a host PTY",
					"sandbox", worktree.SandboxID, "error", eerr.Error())
				return nil, "", "", eerr
			}
			cwd, cerr := remoteSB.ContainerPath(worktree.Path)
			if cerr != nil {
				logger.Warn("cannot map the worktree into the sandbox; falling back to a host PTY",
					"worktree", worktree.Path, "error", cerr.Error())
				return nil, "", "", cerr
			}
			apiKey, kerr := keys.For(ctx, worktree.SandboxID)
			if kerr != nil {
				logger.Warn("cannot resolve the sandbox's API key; falling back to a host PTY",
					"sandbox", worktree.SandboxID, "error", kerr.Error())
				return nil, "", "", kerr
			}
			client, aerr := aio.New(aio.Config{
				BaseURL: endpoint,
				APIKey:  apiKey,
				Root:    "/workspace",
			})
			if aerr != nil {
				logger.Warn("cannot build the sandbox terminal client", "error", aerr.Error())
				return nil, "", "", aerr
			}
			// A fresh id per tab: the image treats create as idempotent and keeps
			// an exited session wedged, so reusing a tab's old id would replay
			// stale output or a dead shell.
			suffix := make([]byte, 8)
			if _, rerr := rand.Read(suffix); rerr != nil {
				return nil, "", "", fmt.Errorf("remote terminal session id: %w", rerr)
			}
			return client, "pf-term-" + hex.EncodeToString(suffix), cwd, nil
		}
	}
	terminals := term.New(termCfg)
	defer terminals.CloseAll()

	// 6. Working directories.
	// One path decides where a session's frozen skill tree lives, for the
	// provisioner that writes it and the delete path that reclaims it.
	skillSnapshotDir := filepath.Join(opt.dataDir, "skill-snapshots")
	// The same seam the file panel uses, for the same reason: with a detached
	// workspace the projection is a request to the sandbox, and a sandbox whose
	// files must be written has to be addressed through its own API and key.
	var workspaceFiles func(context.Context, *contracts.Sandbox) (materialize.FileSystem, error)
	if fileBackend != nil {
		workspaceFiles = fileBackend.files
	}
	prov := provision.New(provision.Config{
		Store:   st,
		Sandbox: sb,
		Git:     git,
		// The concurrency guard reads the same policy the providers were given, so
		// "how many sandboxes may this tenant have" has one answer in this process.
		LimitsFor: tenants.policyFor,
		// Uploaded bundles live here; without it provisioning would mount
		// nothing and every skill would silently be metadata only.
		SkillsDir: filepath.Join(opt.dataDir, "skills"),
		// Frozen skill snapshots live outside every checkout, so a bind mount's
		// source has no second, agent-writable path (FR-14.2).
		SnapshotDir:    skillSnapshotDir,
		WorkspaceFiles: workspaceFiles,
	})

	// 6. Authentication.
	authn, err := auth.New(st, st, auth.Config{
		SessionTTL: 24 * time.Hour,
		Secure:     opt.secureCook,
	})
	if err != nil {
		return fmt.Errorf("init auth: %w", err)
	}

	// 7. HTTP.
	//
	// Reconciliation is built before the router because the delete guard uses the
	// same instance: "running" must mean "an agent serves it", not "a row says
	// so", on both the boot path and the on-demand path.
	//
	// Which authority answers that question depends on where the agent lives. A
	// local agent is this process's child, so the in-process registry is the
	// whole truth. A container agent outlives this process, so the probe also
	// reads the heartbeat marker from the sandbox; without that, every live
	// remote session would be marked interrupted after a control-plane restart.
	livenessProbe := sessionLivenessProbe(opt, st, rt, remoteSB, keys, logger)
	rec := reconcile.New(reconcile.Config{
		Store:   st,
		Probe:   livenessProbe,
		Runtime: rt,
		Logger:  logger,
		Now:     time.Now,
	})
	handler := httpapi.NewServer(httpapi.Deps{
		Store:           st,
		Auth:            authn,
		Runtime:         rt,
		Workspace:       files,
		DefaultProvider: opt.defaultProvider,
		ParallelLimit:   parallelLimit,
		// The panels have to answer "can this MCP server reach the agent at all?",
		// and the answer follows the placement: a container deployment runs every
		// agent in its sandbox, so a stdio server cannot be staged there
		// (internal/mcpstage).
		AgentIsolated:    opt.agentInSandbox,
		SandboxDiskQuota: opt.sandboxDiskQuota,
		SkillMaxBytes:    opt.skillMaxBytes,
		// The quota is measured where the workspace is. With a container provider
		// that is inside the sandbox: the reported root is /workspace, a path this
		// process cannot open, and walking it here is how the admin page came to
		// report 0 B / over=false for a sandbox holding 3 GiB. One measurer feeds
		// the admin figure, the run-boundary refusal and the periodic watchdog, so
		// the three cannot disagree.
		MeasureSandbox: sandboxDiskMeasurer(remoteSB, keys),
		// And the kill that makes "the watchdog stops the agent" true for a
		// container placement, where the agent outlives this process's stream.
		StopAgent: sandboxAgentStopper(remoteSB, keys),
		// And the liveness probe the watchdog needs: `true` through the sandbox's
		// own API, bounded to a few seconds. A fork storm that exhausted the
		// sandbox's pids cgroup leaves it unable to fork, so the only honest
		// question is "can it still run a command" — and the answer has to come
		// from the same door the agent uses.
		ProbeSandbox: sandboxProbe(remoteSB, keys),
		// The quota policy and the network mode are what the admin status page
		// reports next to each tenant's sandbox count: "what limit is this tenant
		// running with" has to be answerable without reading the deployment file.
		TenantLimits:       tenants.policy,
		SandboxNetworkMode: string(networkMode),
		// The egress policy, so the admin screen can answer "who can reach the
		// internet" per tenant — the same reason the quota is reported there.
		TenantEgress: egress,
		// The mode is only a boundary when something creates a container for it.
		SandboxEgressApplied: opt.sandboxKind != "local",
		Sandbox:              sb,
		Secrets:              box,
		WebUpstream:          opt.webUpstream,
		SkillsDir:            filepath.Join(opt.dataDir, "skills"),
		SnapshotDir:          skillSnapshotDir,
		AgentDir:             filepath.Join(opt.dataDir, "pi-agents"),
		ArtifactsDir:         filepath.Join(opt.dataDir, "artifacts"),
		SandboxSnapshotDir:   filepath.Join(opt.dataDir, "snapshots"),
		DataDir:              abs(opt.dataDir),
		LLMGateway:           gateway,
		Browser:              browsers,
		Term:                 termAdapter{m: terminals},
		Provisioner:          prov,
		SetupDefaults:        setupDefaults,
		BrowserTakeoverTTL:   opt.browserTakeoverTTL,
		Reconciler:           rec,
		Probe:                livenessProbe,
		Now:                  time.Now,
		Logger:               logger,
	})

	srv := &http.Server{
		Addr:              opt.addr,
		Handler:           handler.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: the SSE endpoint is a long-lived stream.
		IdleTimeout: 120 * time.Second,
	}

	errCh := make(chan error, 1)

	// Restart reconciliation, before the port is open. A process that was running
	// a turn when the previous control plane died leaves sessions.status='running'
	// and runs.status='started' with no agent behind either. Nothing else repairs
	// that: the UI shows a run that never ends, and the delete guard refuses the
	// project forever. The pass is bounded and never fatal — a failure is logged
	// and the server still starts, because a stale row must not keep the
	// deployment down.
	bootCtx, bootCancel := context.WithTimeout(ctx, 30*time.Second)
	report, rerr := rec.Run(bootCtx, reconcile.ReasonRestart, "startup")
	bootCancel()
	if rerr != nil {
		logger.Warn("session reconciliation at startup failed; stale rows were left as they are",
			"error", rerr.Error())
	}
	logger.Info("session reconciliation at startup",
		"scanned", report.Scanned,
		"interrupted", report.Interrupted,
		"runs", report.Runs,
		"kept_live", report.Live,
		"watchdog", opt.sessionReconcileInterval.String())
	go rec.Start(ctx, opt.sessionReconcileInterval)

	// The periodic half of the disk quota. The run boundary refuses the *next*
	// turn; this is what stops the current one, and what makes the admin figure a
	// measurement instead of a walk of a directory that does not exist here. It
	// no-ops (loudly) when no quota is configured or nothing can measure a
	// sandbox from inside.
	handler.StartDiskWatch(ctx)

	// The liveness half: a sandbox whose pids cgroup is exhausted cannot fork,
	// so `docker exec`, the platform's own `du`, and the agent all fail while the
	// container is still "running" — the only recovery the product used to offer
	// was a manual pause+resume (adversarial acceptance #2). This probes every
	// running sandbox through its own API and performs that pause+resume itself,
	// telling the session before and after. It no-ops (loudly) for a deployment
	// that cannot run a command inside a sandbox.
	handler.StartSandboxWatch(ctx)

	// Stage 3: after reconciliation has decided which runs are really alive,
	// reconnect to them. The agents kept running inside their sandboxes while this
	// process was gone; without this pass their events would accumulate in the
	// supervisor's replay buffer and their runs would never settle. It runs after
	// the boot pass on purpose — a session whose agent is provably gone has
	// already been marked interrupted and is not reattached — and it runs *before*
	// the port opens, so a client asking /status in the first second gets the
	// reattached truth instead of a process-less zero.
	if opt.agentInSandbox {
		reattachCtx, reattachCancel := context.WithTimeout(ctx, 60*time.Second)
		reattached, rerr := handler.ReattachLiveSessions(reattachCtx)
		reattachCancel()
		if rerr != nil {
			logger.Warn("could not reattach to agents that outlived the previous process",
				"error", rerr.Error())
		}
		logger.Info("reattached to surviving agents",
			"count", reattached,
			"detail", "stage 3: an agent that outlived this process keeps its run")
	}

	// Idle agents are stopped on their own (FR-5.x): a session keeps its process warm
	// for speed, but nothing else bounded how many of those a long-lived deployment
	// accumulates. The default is generous; the e2e harness sets it low so a suite of
	// eighty sessions does not leave eighty children behind.
	go rt.StartIdleReaper(ctx, opt.agentIdleTTL, time.Minute)

	// Sandboxes idle-pause and are reclaimed on their own (FR-5.3). The two
	// thresholds are independent so an operator can pause without ever deleting,
	// or leave a long PAUSED window. Reclaim always writes the manifest first;
	// the reaper refuses to destroy if that step fails.
	if opt.sandboxIdlePause > 0 || opt.sandboxReclaimAfter > 0 {
		snapshots, serr := snapshot.New(snapshot.Config{
			Root:  filepath.Join(opt.dataDir, "snapshots"),
			Store: st,
		})
		if serr != nil {
			return fmt.Errorf("init sandbox snapshot writer: %w", serr)
		}
		rp := reaper.New(reaper.Config{
			Store:        st,
			Sandbox:      sb,
			Snapshots:    snapshots,
			Logger:       logger,
			Now:          time.Now,
			PauseAfter:   opt.sandboxIdlePause,
			ReclaimAfter: opt.sandboxReclaimAfter,
			Interval:     opt.sandboxReapInterval,
			// Pausing is not just a label. The manual pause handler already
			// stops the agents on a sandbox; the background pause must do the
			// same or a "PAUSED" sandbox keeps executing work.
			OnPaused: func(pctx context.Context, s *contracts.Sandbox) {
				stopAgentsOnSandbox(pctx, logger, st, rt, s)
			},
		})
		logger.Info("sandbox idle policy enabled",
			"pause_after", opt.sandboxIdlePause.String(),
			"reclaim_after", opt.sandboxReclaimAfter.String(),
			"interval", opt.sandboxReapInterval.String(),
			"snapshots", abs(filepath.Join(opt.dataDir, "snapshots")))
		go rp.Start(ctx)
	} else {
		logger.Info("sandbox idle policy disabled (both thresholds are 0)")
	}

	// Orphaned sandbox directories are swept at startup and then periodically.
	// A project delete now refuses to remove the row when the directory cannot
	// be destroyed, so new orphans should not appear; this pass is what cleans
	// the ones older builds left behind (and any a crash between the two
	// filesystem steps could produce). The layout is the local provider's, so a
	// container provider gets no sandbox sweep.
	//
	// The same pass sweeps FR-14.2's per-session skill snapshots: the delete
	// handlers remove them eagerly, and this catches what a crash, an ephemeral
	// session's expiry or an older build left behind. It runs for every provider
	// because the snapshot root is the control plane's own data directory.
	if opt.sandboxSweep > 0 {
		orphans := janitor.New(janitor.Config{
			Root:          localSandboxRoot,
			Projects:      st,
			SnapshotRoot:  skillSnapshotDir,
			AgentRoot:     filepath.Join(opt.dataDir, "pi-agents"),
			ArtifactsRoot: filepath.Join(opt.dataDir, "artifacts"),
			SnapshotsRoot: filepath.Join(opt.dataDir, "snapshots"),
			SkillsRoot:    filepath.Join(opt.dataDir, "skills"),
			Sessions:      st,
			Sandboxes:     st,
			Skills:        st,
			SnapshotRows:  st,
			Logger:        logger,
			Interval:      opt.sandboxSweep,
		})
		// Empty means this provider keeps no sandbox directories here; saying
		// "the current directory" for it would read as if it did.
		sandboxRootLabel := ""
		if localSandboxRoot != "" {
			sandboxRootLabel = abs(localSandboxRoot)
		}
		logger.Info("orphaned runtime directories are swept",
			"sandbox_root", sandboxRootLabel,
			"skill_snapshot_root", abs(skillSnapshotDir),
			"agents_root", abs(filepath.Join(opt.dataDir, "pi-agents")),
			"artifacts_root", abs(filepath.Join(opt.dataDir, "artifacts")),
			"sandbox_snapshot_root", abs(filepath.Join(opt.dataDir, "snapshots")),
			"skills_root", abs(filepath.Join(opt.dataDir, "skills")),
			"interval", opt.sandboxSweep.String())
		go orphans.Start(ctx)
	} else {
		logger.Info("janitor sweep disabled", "flag", "-sandbox-sweep=0")
	}

	// Scratch sessions (PRD FR-1.10) expire on their own: a quick question should
	// not need a cleanup step from the user.
	go func() {
		sweep := func() {
			n, err := st.SweepEphemeral(ctx, time.Now().Add(-6*time.Hour))
			if err != nil {
				logger.Warn("could not sweep ephemeral sessions", "error", err.Error())
				return
			}
			if n > 0 {
				logger.Info("swept ephemeral sessions", "count", n)
			}
		}
		sweep()
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()

	go func() {
		logger.Info("listening", "url", "http://"+opt.addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// termAdapter narrows *term.Manager to the interface httpapi declares, so the
// HTTP package never imports the terminal implementation.
type termAdapter struct{ m *term.Manager }

func (a termAdapter) Open(ctx context.Context, sandboxID, tabID, shell string) (*httpapi.TermSession, error) {
	info, err := a.m.Open(ctx, sandboxID, tabID, shell)
	if err != nil {
		return nil, err
	}
	return &httpapi.TermSession{
		TabID: info.TabID, Shell: info.Shell, PID: info.PID,
		Start: info.Start, End: info.End,
	}, nil
}

func (a termAdapter) Write(tabID string, p []byte) error { return a.m.Write(tabID, p) }
func (a termAdapter) Resize(tabID string, cols, rows int) error {
	return a.m.Resize(tabID, cols, rows)
}
func (a termAdapter) Close(tabID string) error { return a.m.Close(tabID) }
func (a termAdapter) Read(tabID string, from int64) ([]byte, int64, bool) {
	return a.m.Read(tabID, from)
}
func (a termAdapter) Info(tabID string) (httpapi.TermSession, bool) {
	info, ok := a.m.Info(tabID)
	if !ok {
		return httpapi.TermSession{}, false
	}
	return httpapi.TermSession{
		TabID: info.TabID, Shell: info.Shell, PID: info.PID,
		Start: info.Start, End: info.End, Err: info.Err,
		Retrying: info.Retrying, RetrySeq: info.RetrySeq, RetryWhy: info.RetryWhy,
	}, true
}

// CloseSandbox closes every terminal tab opened against one worktree. A project
// delete calls it for each of its sessions before removing the sandbox: a shell
// is a process whose cwd is the checkout, so it is one of the things that makes
// `os.RemoveAll` fail on Windows.
func (a termAdapter) CloseSandbox(worktreeID string) int { return a.m.CloseSandbox(worktreeID) }

// stopAgentsOnSandbox stops every agent process whose worktree belongs to one
// sandbox. It mirrors the manual pause handler: "paused" should describe the
// machine, not only the row. Errors are logged and do not fail the transition;
// a process that will not die is the agent reaper's problem next.
func stopAgentsOnSandbox(ctx context.Context, logger *slog.Logger, st *store.Store, rt *pi.Runtime, sb *contracts.Sandbox) {
	sessions, err := st.ListSessions(ctx, sb.UserID, store.SessionFilter{Limit: 500})
	if err != nil {
		logger.Warn("sandbox idle: cannot list sessions to stop agents",
			"sandbox", sb.ID, "error", err.Error())
		return
	}
	for _, sess := range sessions {
		if sess.WorktreeID == "" {
			continue
		}
		wt, werr := st.WorktreeByID(ctx, sess.WorktreeID)
		if werr != nil || wt.SandboxID != sb.ID {
			continue
		}
		if serr := rt.Stop(ctx, sess.ID); serr != nil {
			logger.Warn("sandbox idle: could not stop an agent on the paused sandbox",
				"sandbox", sb.ID, "session", sess.ID, "error", serr.Error())
		}
	}
}

// newRuntime builds the agent runtime. With -fake-pi it drives the fake peer
// that ships with the repo, which is how development and the e2e suite run
// without a model account.
//
// There are two executions, and each one is the whole of its placement: a
// container provider passes the sandbox agent (Dial, so the process is spawned
// inside the sandbox and this one only owns its stdio), and -sandbox local passes
// nil, so the agent is this process's own child and the in-process registry is the
// whole truth. Stage 4 removed the third shape — a locally spawned agent wrapped
// in the sandbox heartbeat launcher — because no placement produces it any more.
func newRuntime(opt options, logger *slog.Logger, setup func(contracts.SessionSpec) agentSetup, modelFor func(contracts.SessionSpec, string) string, launcher isolate.Launcher, agent *sandboxAgent, onStop func(sessionID string)) (*pi.Runtime, error) {
	bin := opt.piBin
	args := []string{"--mode", "rpc"}

	if opt.useFakePi {
		resolved, err := resolveFakePi(opt)
		if err != nil {
			return nil, err
		}
		bin = resolved
		// The fake peer has no CLI of its own beyond these flags.
		args = []string{"-delay", envOr("PIFORGE_FAKE_PI_DELAY_MS", "5")}
		logger.Info("using the fake pi peer", "binary", resolved)
	}

	cfg := pi.RuntimeConfig{
		Config: pi.Config{
			BinaryPath: bin,
			Args:       args,
			Stderr:     os.Stderr,
		},
		Launcher: launcher,
		ModelFor: modelFor,
		ArgsFor: func(spec contracts.SessionSpec) []string {
			return setup(spec).Args
		},
		EnvFor: func(spec contracts.SessionSpec) []string {
			return setup(spec).Env
		},
		// Every path that stops an agent ends its model gateway token too (see
		// pi.RuntimeConfig.OnStop). It is the difference between "the session
		// ended" and "the bearer credential the sandbox holds is still good for
		// the rest of its TTL".
		OnStop: onStop,
	}
	if agent != nil {
		// The process is not ours to spawn: Dial starts it in the sandbox and
		// returns the stream, which is the same shape the bridge already accepts
		// from a child's pipes. The worktree translation has to be in place before
		// the session file's cwd header is written (AgentWorktree), and the
		// session file itself moves into the workspace both sides can see.
		cfg.Dial = agent.Dial
		cfg.AgentWorktree = func(spec contracts.SessionSpec) string {
			return agent.ContainerWorktree(context.Background(), spec)
		}
		cfg.SessionFileFor = agent.SessionFile
		// And the same file is readable without an agent: a resumed session has
		// a transcript on disk and no process, and the entries endpoint must not
		// answer that with an empty conversation.
		cfg.TranscriptFor = agent.Transcript
		// The agent's stderr is a file inside the workspace now, not a pipe this
		// process owns: the Logs panel reads it through the sandbox API.
		cfg.LogsFor = agent.Logs
		// A jail wraps a local argv; there is no local argv here. The isolation
		// boundary is the container the process now runs in.
		cfg.Launcher = nil
	}
	return pi.NewRuntime(cfg), nil
}

// agentSetup is per-session launch configuration: the model selection Pi needs
// and the environment that points it at the gateway.
type agentSetup struct {
	Env  []string
	Args []string
}

// piAPI maps a stored provider api onto the name Pi uses in models.json.
func piAPI(api string) string {
	switch api {
	case "anthropic-messages":
		return "anthropic-messages"
	case "openai-responses":
		return "openai-responses"
	default:
		return "openai-completions"
	}
}

// splitModelRef splits "provider/model" into its parts, falling back to the
// configured default provider when a session has no explicit model.
func splitModelRef(ref, fallback string) (string, string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fallback, ""
	}
	if provider, model, ok := strings.Cut(ref, "/"); ok {
		if provider == "" {
			return fallback, model
		}
		return provider, model
	}
	return ref, ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// resolveFakePi finds (or builds) the fake peer binary.
//
// Build order: explicit flag → explicit env → a sibling binary next to this
// executable → `go build` into the data dir. The last step needs a Go toolchain
// and only matters for `go run ./cmd/piforged` during development.
func resolveFakePi(opt options) (string, error) {
	if opt.fakePiBin != "" {
		return opt.fakePiBin, nil
	}
	if v := os.Getenv("PIFORGE_FAKE_PI"); v != "" {
		return v, nil
	}

	exe := "fakepi"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if self, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(self), exe)
		if _, err := os.Stat(sibling); err == nil {
			return sibling, nil
		}
	}

	out := filepath.Join(opt.dataDir, exe)
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}

	// Fall back to building it. This only works when the source tree is
	// reachable from the working directory, which is the `go run` case.
	cmd := exec.Command("go", "build", "-o", out, "./cmd/fakepi")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build the fake pi peer (%s): %w — pass -fake-pi-bin to point at a prebuilt one", out, err)
	}
	return out, nil
}

// logSeededAccounts says which first-run accounts were created, and never says
// their password.
//
// The container log is copied into tickets and CI output; a generated password
// printed once there is a credential leak, not a convenience. An operator who
// needs a password sets PIFORGE_SEED_PASSWORD / PIFORGE_SEED_USER_PASSWORD (the
// deployment docs show how), or resets it from the admin users page.
func logSeededAccounts(logger *slog.Logger, res *store.SeedResult) {
	if res.Admin != nil {
		logger.Info("created the first admin account", "email", res.Admin.Email)
	}
	if res.Demo != nil {
		logger.Info("created the demo account", "email", res.Demo.Email)
	}
	logger.Info("seeded the first accounts")
}

// seed creates the first accounts. It is idempotent: an existing admin is left
// alone, so restarting the server never resets a password.
func seed(ctx context.Context, st *store.Store, opt options, logger *slog.Logger) error {
	res, err := st.EnsureAdminSeed(ctx, store.SeedOptions{
		AdminEmail:    opt.seedAdmin,
		DemoEmail:     opt.seedDemo,
		AdminPassword: envOr("PIFORGE_SEED_PASSWORD", ""),
		DemoPassword:  envOr("PIFORGE_SEED_USER_PASSWORD", ""),
		HashPassword:  auth.HashPassword,
		SharedProvider: &store.ProviderInput{
			Name:    "shared-anthropic",
			API:     "anthropic-messages",
			BaseURL: "",
			Models: []contracts.ModelSpec{{
				ID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5",
				ContextWindow: 200000, MaxTokens: 16384, Reasoning: true,
			}},
		},
	})
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	if !res.Created {
		return nil
	}

	logSeededAccounts(logger, res)

	// A deployment with no skills makes the feature look broken: the page says "write
	// one" and there is nothing to try. Three shared skills ship with the binary
	// (FR-14.7), installed once and left alone afterwards so an operator's edits survive.
	if res.Created {
		skillsDir := filepath.Join(opt.dataDir, "skills")
		if err := os.MkdirAll(skillsDir, 0o700); err != nil {
			return fmt.Errorf("prepare the skills directory: %w", err)
		}
		created, serr := st.EnsureBuiltinSkills(ctx, res.Admin.ID, func(skillID string, files map[string]string) (string, string, error) {
			return writeBundle(skillsDir, skillID, files)
		})
		if serr != nil {
			logger.Warn("could not install the built-in skills", "error", serr.Error())
		} else if created > 0 {
			logger.Info("installed the built-in skills", "count", created)
		}
	}
	return nil
}

// writeBundle materialises one skill's files under the skills root and returns the bundle
// key and content hash, using the same layout the HTTP handler writes.
func writeBundle(skillsRoot, skillID string, files map[string]string) (string, string, error) {
	root := filepath.Join(skillsRoot, skillID)
	staging := root + ".tmp"
	if err := os.RemoveAll(staging); err != nil {
		return "", "", err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	hasher := sha256.New()
	for _, rel := range names {
		target := filepath.Join(staging, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", "", err
		}
		if err := os.WriteFile(target, []byte(files[rel]), 0o644); err != nil {
			return "", "", err
		}
		fmt.Fprintf(hasher, "%s\x00%s\x00", rel, files[rel])
	}
	if err := os.Rename(staging, root); err != nil {
		return "", "", err
	}
	return skillID, hex.EncodeToString(hasher.Sum(nil)), nil
}

// gitLetters adapts git status into the per-path letters the file tree shows.
func gitLetters(git *gitx.Runner) func(ctx context.Context, root string) (map[string]string, error) {
	return func(ctx context.Context, root string) (map[string]string, error) {
		st, err := git.Status(ctx, root)
		if err != nil || st == nil {
			// A sandbox that is not a repo (yet) simply has no letters.
			return nil, nil
		}
		out := make(map[string]string, len(st.Changes))
		for _, c := range st.Changes {
			out[c.Path] = letter(c.Index, c.Worktree)
		}
		for _, c := range st.Conflicts {
			out[c] = "!"
		}
		return out, nil
	}
}

func letter(index, worktree string) string {
	pick := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" || s == "." {
			return ""
		}
		return string(s[0])
	}
	if i := pick(index); i != "" && i != "?" {
		return i
	}
	if w := pick(worktree); w != "" {
		if w == "?" {
			return "U"
		}
		return w
	}
	return ""
}

func newLogger(opt options) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(opt.logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	if opt.dev && opt.logLevel == "" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envDuration reads a duration from the environment, falling back to def when it is
// missing or unparseable — a typo in an env var should not stop the server.
func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// envInt reads a positive integer from the environment, falling back to def
// when it is missing, unparseable or not positive. Zero means "unset", which is
// what the config structs treat as "use the default".
func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func abs(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// loadOrCreateSigningKey returns the deployment's gateway HMAC key, creating it
// on first boot.
//
// It is a 32-byte secret on the data volume (0600), the same shape as master.key
// and with the same consequence: back it up with the database, or every
// outstanding agent token dies with the volume. The file exists because tokens
// must outlive the process that issued them — see the call site.
func loadOrCreateSigningKey(path string) ([]byte, error) {
	if raw, err := os.ReadFile(path); err == nil {
		if len(raw) < 32 {
			return nil, fmt.Errorf("the gateway signing key at %s is %d bytes, not 32", path, len(raw))
		}
		return raw[:32], nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read the gateway signing key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate the gateway signing key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create the data directory for the gateway signing key: %w", err)
	}
	// Write through a temporary file so a crash cannot leave a half-written key
	// that every later boot would reject.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, key, 0o600); err != nil {
		return nil, fmt.Errorf("write the gateway signing key: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, fmt.Errorf("install the gateway signing key: %w", err)
	}
	return key, nil
}

// mcpEntriesForSession renders the MCP configuration Pi reads for one session.
//
// Two shapes, one function, because the difference is *what the sandbox is
// allowed to hold*, not how a file is written:
//
//   - local and shared-volume deployments: the server's own credentials,
//     decrypted here and written as environment variables (the historical
//     shape). The agent runs beside this process, so those credentials were
//     never further from the control plane than the agent itself.
//   - a container placement (stage 4: every docker/runner deployment): an HTTP MCP
//     server is **brokered**. The sandbox gets `<gateway>/gw/mcp/<token>`; the
//     control plane injects the real credential on egress, and the token is
//     per-session and revocable (DELETE /v1/sessions/{id}/mcp-token). A **stdio**
//     server is not staged — the same decision `internal/mcpstage` gives the panel
//     and the mount manifest, so the log line here cannot disagree with them
//     (docs/EXECUTION-BOUNDARY.md §11.1).
func mcpEntriesForSession(ctx context.Context, st *store.Store, spec contracts.SessionSpec, sandboxed bool, gatewayBase string, logger *slog.Logger) ([]pimodel.MCPEntry, error) {
	servers, err := st.ListMCPServers(ctx, spec.UserID)
	if err != nil {
		return nil, err
	}
	entries := make([]pimodel.MCPEntry, 0, len(servers))
	for _, server := range servers {
		if !server.Enabled {
			continue
		}
		entry := pimodel.MCPEntry{
			Name:      server.Name,
			Transport: string(server.Transport),
			Command:   server.Command,
			Args:      server.Args,
			URL:       server.URL,
		}
		if omission, blocked := mcpstage.Unavailable(server, sandboxed); blocked {
			// The panel and the manifest already say this; the log is for whoever
			// reads the container output instead of the UI.
			logger.Warn("an MCP server is not staged into the sandbox",
				"session", spec.SessionID, "server", server.Name, "transport", server.Transport,
				"code", omission.Code, "reason", omission.Reason)
			continue
		}
		if sandboxed {
			token, terr := st.EnsureMCPToken(ctx, spec.UserID, spec.SessionID, server.ID)
			if terr != nil {
				if errors.Is(terr, store.ErrMCPTokenRevoked) {
					logger.Warn("this session's MCP access was revoked; the server is not staged again",
						"session", spec.SessionID, "server", server.Name)
				} else {
					logger.Warn("cannot mint an MCP broker token",
						"session", spec.SessionID, "server", server.Name, "error", terr.Error())
				}
				continue
			}
			// The token is the path, not a header: Pi's mcp.json has a url field
			// and no place to put an Authorization header, and a token in a URL
			// the sandbox owns is exactly the revocable credential this is for.
			entry.URL = strings.TrimRight(gatewayBase, "/") + httpapi.MCPGatewayPrefix + token
			entry.Env = nil
			entries = append(entries, entry)
			continue
		}
		if server.HasSecrets {
			secret, serr := st.MCPServerSecrets(ctx, spec.UserID, server.ID)
			if serr != nil {
				_ = serr // logged by the caller; a missing credential is not fatal to the session
			} else if env := pimodel.ParseMCPSecrets(secret); len(env) > 0 {
				entry.Env = env
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
