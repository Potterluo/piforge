// Command sandbox-runner owns the Docker CLI for a PiForge deployment.
//
// It exists so the control plane does not have to: /var/run/docker.sock is host
// root, and the control plane is the process users and models reach
// (docs/SANDBOX-DEPLOY.md §1). This service is the only one that mounts the
// socket, it exposes six JSON operations instead of the Docker API, and it
// performs them with the same internal/sandbox.Docker provider the single-host
// deployment uses — there is one implementation of the container rules, not two.
//
//	RUNNER_TOKEN=... SANDBOX_DATA_ROOT=/data/sandboxes \
//	  sandbox-runner -addr 0.0.0.0:7801
//
// The data root must be the same path the control plane sees (one shared volume):
// the workspace is a bind mount, so the runner and the control plane have to name
// the same directory or each writes a tree the other never reads.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/sandboxrunner"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "sandbox-runner:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		addr = flag.String("addr", envOr("SANDBOX_RUNNER_ADDR", "127.0.0.1:7801"),
			"address to listen on (the control plane must be able to reach it)")
		token = flag.String("token", envOr("RUNNER_TOKEN", ""),
			"shared bearer token every request must present (required)")
		dataRoot = flag.String("data-root", envOr("SANDBOX_DATA_ROOT", ""),
			"directory holding one workspace per (user, project); must be the control plane's sandbox root (required)")
		image = flag.String("image", envOr("SANDBOX_IMAGE", ""),
			"default sandbox image; a create request may name another one")
		network = flag.String("network", envOr("SANDBOX_NETWORK", ""),
			"Docker network sandboxes join (empty: bridge); ignored when -network-mode tenant")
		networkMode = flag.String("network-mode", envOr("SANDBOX_NETWORK_MODE", "shared"),
			"how sandboxes join the network: shared (all on -network) or tenant (a bridge per tenant, the containers in -join-containers attached to each)")
		tenantNetworkPrefix = flag.String("tenant-network-prefix", envOr("SANDBOX_TENANT_NETWORK_PREFIX", ""),
			"name prefix of a tenant's bridge (default piforge-t-)")
		joinContainers = flag.String("join-containers", envOr("SANDBOX_JOIN_CONTAINERS", ""),
			"comma-separated containers joined to every tenant bridge: the control plane (panels and the model gateway) and this runner (the checkout)")
		cpus = flag.String("cpus", envOr("SANDBOX_CPUS", ""),
			"default CPU limit per sandbox, when a create request carries none")
		memory = flag.String("memory", envOr("SANDBOX_MEMORY", ""),
			"default memory limit per sandbox, e.g. 2g, when a create request carries none")
		pids = flag.Int("pids", envInt("SANDBOX_PIDS", 0),
			"default process limit per sandbox (docker --pids-limit); 0 leaves it unlimited")
		apiPort = flag.Int("api-port", envInt("SANDBOX_API_PORT", 8080),
			"container port that serves the sandbox HTTP API")
		apiKey = flag.String("api-key", envOr("SANDBOX_API_KEY", ""),
			"fixed SANDBOX_API_KEY for every container and for reaching their API (debugging override; empty gives each container its own key)")
		containerEndpoint = flag.Bool("container-endpoint", envBool("SANDBOX_CONTAINER_ENDPOINT", false),
			"report http://<container>:<api-port> on the sandbox network instead of a published loopback port")
		workspaceVolume = flag.String("workspace-volume", envOr("SANDBOX_WORKSPACE_VOLUME", ""),
			"Docker volume that holds the data root; mounts each sandbox's directory from it instead of bind-mounting a host path (required when this process is itself a container)")
		workspaceVolumeRoot = flag.String("workspace-volume-root", envOr("SANDBOX_WORKSPACE_VOLUME_ROOT", ""),
			"where that volume is mounted in this process, so a sandbox's subpath can be computed (required with -workspace-volume)")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if strings.TrimSpace(*token) == "" {
		return fmt.Errorf("-token (RUNNER_TOKEN) is required: a runner without one is a container-creating HTTP service open to its network")
	}
	mode, err := sandbox.ParseNetworkMode(*networkMode)
	if err != nil {
		return err
	}
	if mode == sandbox.NetworkTenant && len(splitList(*joinContainers)) == 0 {
		// A tenant bridge nothing else joins is a sandbox that cannot reach the
		// control plane's gateway or have its checkout provisioned. Failing the
		// start names the flag instead of leaving every session to fail later.
		return fmt.Errorf("-network-mode tenant needs -join-containers (SANDBOX_JOIN_CONTAINERS): name the control plane and this runner, e.g. piforge,piforge-sandbox-runner")
	}

	provider, err := sandboxrunner.NewDockerProvider(sandboxrunner.DockerOptions{
		DataRoot:            *dataRoot,
		Image:               *image,
		Network:             *network,
		NetworkMode:         mode,
		TenantNetworkPrefix: *tenantNetworkPrefix,
		JoinContainers:      splitList(*joinContainers),
		CPUs:                *cpus,
		Memory:              *memory,
		Pids:                *pids,
		APIPort:             *apiPort,
		APIKey:              *apiKey,
		ContainerEndpoint:   *containerEndpoint,
		WorkspaceVolume:     *workspaceVolume,
		WorkspaceVolumeRoot: *workspaceVolumeRoot,
	})
	if err != nil {
		return err
	}

	server, err := sandboxrunner.New(sandboxrunner.Config{
		Token:    *token,
		Provider: provider,
		Logger:   logger,
	})
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:    *addr,
		Handler: server.Handler(),
		// Bodies are small and local; the long part of a request is `docker run`
		// plus the checkout, which is bounded by the handler's own timeout.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	failures := make(chan error, 1)
	go func() {
		failures <- httpServer.ListenAndServe()
	}()
	logger.Info("sandbox runner listening",
		"addr", *addr,
		"data_root", *dataRoot,
		"image", *image,
		"network", *network,
		"network_mode", string(mode),
		"join_containers", strings.Join(splitList(*joinContainers), ","),
		"container_endpoint", *containerEndpoint)

	select {
	case err := <-failures:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	// Let in-flight lifecycle calls finish: a half-created container is exactly the
	// state this service exists to avoid.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// splitList reads a comma-separated flag, dropping empty entries so a trailing
// comma or an unset variable is "no containers" rather than one named "".
func splitList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// envInt reads an integer environment variable, keeping the fallback when it is
// unset or unparsable rather than failing startup over a default.
func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envBool(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
