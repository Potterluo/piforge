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
			"Docker network sandboxes join (empty: bridge)")
		cpus = flag.String("cpus", envOr("SANDBOX_CPUS", ""),
			"default CPU limit per sandbox")
		memory = flag.String("memory", envOr("SANDBOX_MEMORY", ""),
			"default memory limit per sandbox, e.g. 2g")
		apiPort = flag.Int("api-port", envInt("SANDBOX_API_PORT", 8080),
			"container port that serves the sandbox HTTP API")
		apiKey = flag.String("api-key", envOr("SANDBOX_API_KEY", ""),
			"default SANDBOX_API_KEY for containers and for reaching their API")
		containerEndpoint = flag.Bool("container-endpoint", envBool("SANDBOX_CONTAINER_ENDPOINT", false),
			"report http://<container>:<api-port> on the sandbox network instead of a published loopback port")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if strings.TrimSpace(*token) == "" {
		return fmt.Errorf("-token (RUNNER_TOKEN) is required: a runner without one is a container-creating HTTP service open to its network")
	}

	provider, err := sandboxrunner.NewDockerProvider(sandboxrunner.DockerOptions{
		DataRoot:          *dataRoot,
		Image:             *image,
		Network:           *network,
		CPUs:              *cpus,
		Memory:            *memory,
		APIPort:           *apiPort,
		APIKey:            *apiKey,
		ContainerEndpoint: *containerEndpoint,
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
