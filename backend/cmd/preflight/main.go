// Command preflight checks a host for the isolated deployment: Docker Engine
// version, the three images, and — the part a tag cannot tell you — whether the
// sandbox image can actually start the agent.
//
// It is what `scripts/up-isolated.ps1` runs before it builds or starts anything,
// and it can be run on its own:
//
//	go run ./backend/cmd/preflight \
//	  -control-plane-image piforge:isolated \
//	  -runner-image piforge/sandbox-runner:isolated \
//	  -sandbox-image piforge/sandbox-agent:isolated
//
// Exit status 0 means every check passed; 1 means at least one failed and the
// output names it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/piforge/piforge/backend/internal/preflight"
)

func main() {
	controlPlane := flag.String("control-plane-image", envOr("PIFORGE_IMAGE", "piforge:isolated"),
		"the piforge image (piforged + the built web app)")
	runner := flag.String("runner-image", envOr("PIFORGE_RUNNER_IMAGE", "piforge/sandbox-runner:isolated"),
		"the sandbox-runner image")
	sandbox := flag.String("sandbox-image", envOr("PIFORGE_SANDBOX_IMAGE", "piforge/sandbox-agent:isolated"),
		"the image the agent, the shell and the browser run in")
	minEngine := flag.Int("min-engine", preflight.MinEngine, "minimum Docker Engine major version")
	quiet := flag.Bool("quiet", false, "print only failures")
	flag.Parse()

	insp := &preflight.DockerInspector{}
	report := preflight.Run(context.Background(), preflight.Config{
		ControlPlaneImage: *controlPlane,
		RunnerImage:       *runner,
		SandboxImage:      *sandbox,
		MinEngine:         *minEngine,
	}, insp)

	for _, line := range report.Lines() {
		if *quiet && len(line) > 0 && line[0] == '[' && line[1:5] == "PASS" {
			continue
		}
		fmt.Println(line)
	}
	if !report.OK() {
		fmt.Fprintln(os.Stderr, "\npreflight failed: the isolated stack would not run the agent; nothing was started.")
		os.Exit(1)
	}
	fmt.Println("\npreflight ok: the three images exist and the sandbox image can start the agent.")
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
