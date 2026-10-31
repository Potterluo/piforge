// Package preflight checks that a machine can host the isolated deployment
// before it spends twenty minutes building three images and then fails at the
// first session.
//
// It exists because the acceptance pass had to override every image variable in
// the shipped `.env`: the file named a base image with no `/opt/piforge` and no
// `pi`, so a team that trusted it got a sandbox that could not start the agent.
// The check is therefore about *what is inside the images*, not only whether a
// tag exists — a tag that exists and cannot run the agent is the trap.
//
// The inspection itself is Docker, which is why everything here goes through
// Inspector: the decision logic is testable with a fake, and the shell-out lives
// in docker.go.
package preflight

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// MinEngine is the Docker Engine version the isolated deployment needs: the
// runner creates sandboxes with `--mount type=volume,...,volume-subpath=...`,
// which is 25.0.
const MinEngine = 25

// Inspector is everything the preflight needs from the host.
type Inspector interface {
	// EngineVersion returns the daemon's version, e.g. "29.9.2".
	EngineVersion(ctx context.Context) (string, error)
	// ImageExists reports whether a local image ref exists.
	ImageExists(ctx context.Context, ref string) (bool, error)
	// ImageProbe runs one shell script inside an image and returns its stdout.
	ImageProbe(ctx context.Context, ref, script string) (string, error)
}

// Config names the images this deployment will run.
type Config struct {
	// ControlPlaneImage is the piforge image (piforged + the built web app).
	ControlPlaneImage string
	// RunnerImage is the sandbox-runner image (the only one with docker.sock).
	RunnerImage string
	// SandboxImage is the image the agent, the shell and the browser run in. It
	// must contain the agent runtime; that is what most of this file checks.
	SandboxImage string
	// MinEngine overrides MinEngine (zero means MinEngine).
	MinEngine int
}

// LauncherPath and PiBinary are the two things the sandbox image must provide.
const (
	LauncherPath = "/opt/piforge/piforged"
	PiBinary     = "pi"
)

// probeScript prints one marker per requirement it finds. Markers rather than
// exit codes: the point is to name *which* piece is missing, and a combined
// `&&` chain can only say "something".
const probeScript = `test -x ` + LauncherPath + ` && echo PF_LAUNCHER_OK; ` +
	`command -v ` + PiBinary + ` >/dev/null 2>&1 && echo PF_PI_OK; true`

// Check is one requirement and what to do about it.
type Check struct {
	Name   string
	OK     bool
	Detail string
	// Remedy is printed only when OK is false, and it must name the fix.
	Remedy string
}

// Report is the result of a preflight run.
type Report struct {
	Checks []Check
}

// OK reports whether every check passed.
func (r Report) OK() bool {
	for _, c := range r.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

// Failures returns the checks that failed, in order.
func (r Report) Failures() []Check {
	out := make([]Check, 0, len(r.Checks))
	for _, c := range r.Checks {
		if !c.OK {
			out = append(out, c)
		}
	}
	return out
}

// Lines renders the report the way both the CLI and a script log want it: one
// line per check, with the fix indented under a failure.
func (r Report) Lines() []string {
	var out []string
	for _, c := range r.Checks {
		status := "PASS"
		if !c.OK {
			status = "FAIL"
		}
		line := "[" + status + "] " + c.Name
		if c.Detail != "" {
			line += " — " + c.Detail
		}
		out = append(out, line)
		if !c.OK && c.Remedy != "" {
			out = append(out, "       fix: "+c.Remedy)
		}
	}
	return out
}

// Run performs every check. It never stops at the first failure: a machine that
// is missing all three images should say so once, not one image per attempt.
func Run(ctx context.Context, cfg Config, insp Inspector) Report {
	minEngine := cfg.MinEngine
	if minEngine <= 0 {
		minEngine = MinEngine
	}
	var checks []Check

	version, err := insp.EngineVersion(ctx)
	switch {
	case err != nil:
		checks = append(checks, Check{
			Name:   "docker daemon",
			Detail: err.Error(),
			Remedy: "start Docker (or point DOCKER_HOST at it) and run this again",
		})
	case engineAtLeast(version, minEngine):
		checks = append(checks, Check{
			Name:   "docker engine",
			OK:     true,
			Detail: fmt.Sprintf("%s (>= %d)", version, minEngine),
		})
	default:
		checks = append(checks, Check{
			Name:   "docker engine",
			Detail: fmt.Sprintf("%s is older than %d", version, minEngine),
			Remedy: fmt.Sprintf("the runner creates sandboxes with volume-subpath mounts, which need Engine %d.0 or newer; upgrade Docker", minEngine),
		})
	}

	images := []struct {
		name  string
		image string
		why   string
	}{
		{"control plane image", cfg.ControlPlaneImage, "its Dockerfile builds piforged and the web app; build it with: docker build -t " + cfg.ControlPlaneImage + " ."},
		{"sandbox-runner image", cfg.RunnerImage, "build it with: docker build -f deploy/Dockerfile.runner -t " + cfg.RunnerImage + " ."},
		{"sandbox image", cfg.SandboxImage, "build it with: docker build -f deploy/Dockerfile.sandbox-agent --build-arg AGENT_IMAGE=" + cfg.ControlPlaneImage + " -t " + cfg.SandboxImage + " ."},
	}
	sandboxPresent := false
	for _, item := range images {
		check := Check{Name: item.name, Detail: item.image, Remedy: item.why}
		if strings.TrimSpace(item.image) == "" {
			check.Detail = "not configured"
			check.Remedy = "name the image (PIFORGE_IMAGE / PIFORGE_RUNNER_IMAGE / PIFORGE_SANDBOX_IMAGE) or run scripts/up-isolated.ps1"
			checks = append(checks, check)
			continue
		}
		exists, ierr := insp.ImageExists(ctx, item.image)
		if ierr != nil {
			check.Detail = item.image + ": " + ierr.Error()
		} else if exists {
			check.OK = true
			if item.name == "sandbox image" {
				sandboxPresent = true
			}
		} else {
			check.Detail = item.image + " is not in the local image store"
		}
		checks = append(checks, check)
	}

	// What is *inside* the sandbox image is the failure a tag check cannot see.
	launcher := Check{
		Name:   "sandbox image has " + LauncherPath,
		Detail: cfg.SandboxImage,
		Remedy: "this image cannot start the agent: build it with deploy/Dockerfile.sandbox-agent (scripts/up-isolated.ps1 does) and point PIFORGE_SANDBOX_IMAGE at it",
	}
	pi := Check{
		Name:   "sandbox image has " + PiBinary,
		Detail: cfg.SandboxImage,
		Remedy: "the agent binary is missing from the image; build it with deploy/Dockerfile.sandbox-agent (scripts/up-isolated.ps1 does)",
	}
	if strings.TrimSpace(cfg.SandboxImage) != "" && sandboxPresent {
		out, perr := insp.ImageProbe(ctx, cfg.SandboxImage, probeScript)
		if perr != nil {
			launcher.Detail = cfg.SandboxImage + ": " + perr.Error()
			pi.Detail = cfg.SandboxImage + ": " + perr.Error()
		} else {
			launcher.OK = hasMarker(out, "PF_LAUNCHER_OK")
			pi.OK = hasMarker(out, "PF_PI_OK")
		}
	} else if strings.TrimSpace(cfg.SandboxImage) == "" {
		launcher.Detail = "no sandbox image configured"
		pi.Detail = launcher.Detail
	} else {
		// Not present: say so instead of starting a container for an image that
		// does not exist, which would only add a confusing daemon error.
		launcher.Detail = cfg.SandboxImage + " is not here, so its contents cannot be checked"
		pi.Detail = launcher.Detail
	}
	checks = append(checks, launcher, pi)

	return Report{Checks: checks}
}

func hasMarker(out, marker string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == marker {
			return true
		}
	}
	return false
}

// engineAtLeast reports whether a dotted version is at least major.minor.
func engineAtLeast(version string, major int) bool {
	trimmed := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if trimmed == "" {
		return false
	}
	parts := strings.Split(trimmed, ".")
	got, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return false
	}
	return got >= major
}

// MissingRefs is a convenience for a script that wants to name what is absent
// before building. It keeps the ordering stable so output can be diffed.
func MissingRefs(checks []Check) []string {
	var out []string
	for _, c := range checks {
		if c.OK {
			continue
		}
		out = append(out, c.Name)
	}
	sort.SliceStable(out, func(i, j int) bool { return i < j })
	return out
}
