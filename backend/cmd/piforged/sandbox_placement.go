package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// placement is where one panel feature runs: on the host or inside the sandbox
// container. "auto" is a request, not a placement, and never survives resolution.
type placement string

const (
	// placeAuto defers to the provider: a container sandbox puts the feature
	// inside the container, a local sandbox leaves it on the host. It is the
	// default so that "which provider" and "where the panels look" stay one
	// decision.
	placeAuto placement = "auto"
	// placeHost runs the feature on the control plane's machine.
	placeHost placement = "host"
	// placeRemote runs the feature inside the sandbox container.
	placeRemote placement = "remote"
)

// sandboxPlacement is the resolved answer: exactly one placement per feature.
type sandboxPlacement struct {
	Browser  placement
	Terminal placement
	Files    placement

	// container records which provider the answer was resolved against, because
	// it changes what "host" means: a choice under a container provider, the only
	// possibility under local. Both docker and runner put the sandbox in a
	// container; they differ only in which process holds the Docker CLI.
	container bool
}

// features pairs a feature's flag name with its resolved placement, in the order
// the flags are documented. Keeping one list means the warning and the log line
// can never disagree about which features exist.
func (p sandboxPlacement) features() []struct {
	name      string
	placement placement
} {
	return []struct {
		name      string
		placement placement
	}{
		{"browser", p.Browser},
		{"terminal", p.Terminal},
		{"files", p.Files},
	}
}

// HostFeatures names every feature that ended up on the host, in flag order. An
// empty result means the deployment is entirely one side, which is the only
// shape that does not surprise whoever reads the log.
func (p sandboxPlacement) HostFeatures() []string {
	var out []string
	for _, f := range p.features() {
		if f.placement == placeHost {
			out = append(out, f.name)
		}
	}
	return out
}

// RemoteFeatures names every feature that ended up inside the sandbox.
func (p sandboxPlacement) RemoteFeatures() []string {
	var out []string
	for _, f := range p.features() {
		if f.placement == placeRemote {
			out = append(out, f.name)
		}
	}
	return out
}

// Mixed reports whether one deployment is split across the host and the container.
//
// Only a container provider can be mixed: under local there is no container, so
// every feature on the host is the whole deployment, not half of one. Under
// docker or runner a single host feature is the arrangement nobody picks on
// purpose, which is why the caller warns about it.
func (p sandboxPlacement) Mixed() bool {
	return p.container && len(p.HostFeatures()) > 0
}

// resolveSandboxPlacement turns the three requested placements into the ones the
// process will actually use.
//
// The rule is that the provider decides by default: a container sandbox means the
// agent, the browser, the terminal and the file panel all look at one container,
// because that is the only arrangement where they all see the same bytes. A
// per-feature "host" is still honoured — someone may genuinely want the local
// Chrome against a containerised workspace — but it has to be asked for, and the
// caller reports it, so a half-inside deployment cannot happen by omission.
//
// Remote without a container is not a degraded mode, it is a contradiction, so it
// is rejected at startup instead of failing on the first request.
func resolveSandboxPlacement(kind, browser, terminal, files string) (sandboxPlacement, error) {
	container := isContainerSandbox(kind)
	var p sandboxPlacement
	p.container = container
	for _, req := range []struct {
		feature   string
		requested string
		dst       *placement
	}{
		{"browser", browser, &p.Browser},
		{"terminal", terminal, &p.Terminal},
		{"files", files, &p.Files},
	} {
		resolved, err := resolvePlacement(req.feature, req.requested, container)
		if err != nil {
			return sandboxPlacement{}, err
		}
		*req.dst = resolved
	}
	return p, nil
}

// isContainerSandbox reports whether the provider puts a sandbox in a container.
//
// docker and runner are the same boundary reached through different processes:
// docker runs the CLI here, runner asks a service that has the CLI. Everything
// placement-related treats them alike.
func isContainerSandbox(kind string) bool {
	normalized := normalizedSandboxKind(kind)
	return normalized == "docker" || normalized == "runner"
}

// normalizedSandboxKind trims the flag value and lowercases the provider name, so
// "-sandbox Docker " is the provider it looks like and "-sandbox Runner" is not
// silently treated as local. An unknown value is returned normalised and left for
// validateSandboxFlags to reject.
func normalizedSandboxKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		return "local"
	}
	return kind
}

// validateSandboxFlags rejects combinations that cannot mean anything.
//
// The runner credentials are not a hint: if -sandbox is not runner they name a
// service nothing will talk to, and if they are missing under -sandbox runner the
// process would start and then fail on the first session. Both are startup
// errors, named by the flag that has to change.
func validateSandboxFlags(kind, runnerURL, runnerToken string) error {
	switch kind {
	case "local", "docker", "runner":
	default:
		return fmt.Errorf("-sandbox must be local, docker or runner, got %q", kind)
	}
	runnerURL = strings.TrimSpace(runnerURL)
	runnerToken = strings.TrimSpace(runnerToken)
	if kind == "runner" {
		if runnerURL == "" {
			return fmt.Errorf("-sandbox runner needs -sandbox-runner-url (PIFORGE_SANDBOX_RUNNER_URL), e.g. http://sandbox-runner:7801")
		}
		if runnerToken == "" {
			return fmt.Errorf("-sandbox runner needs -sandbox-runner-token (PIFORGE_SANDBOX_RUNNER_TOKEN); the runner rejects unauthenticated requests")
		}
		return nil
	}
	if runnerURL != "" || runnerToken != "" {
		// Name the flag that was set, so the operator sees which line to delete
		// instead of being told "invalid configuration".
		flagName := "-sandbox-runner-url"
		if runnerURL == "" {
			flagName = "-sandbox-runner-token"
		}
		return fmt.Errorf("%s was given, but -sandbox is %q: the runner credentials only apply to -sandbox runner", flagName, kind)
	}
	return nil
}

func resolvePlacement(feature, requested string, container bool) (placement, error) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	switch requested {
	case "", string(placeAuto):
		if container {
			return placeRemote, nil
		}
		return placeHost, nil
	case string(placeHost), string(placeRemote):
		// Explicit, and validated below.
	default:
		return "", fmt.Errorf("-sandbox-%s must be host, remote or auto, got %q", feature, requested)
	}
	if placement(requested) == placeRemote && !container {
		// Naming the reason, not just the rule: the flag is not wrong in itself, it
		// is wrong for this provider, and the operator needs to know which knob to
		// turn instead of guessing.
		return "", fmt.Errorf("-sandbox-%s remote needs -sandbox docker or -sandbox runner: with -sandbox local there is no container to talk to, so this feature can only run on the host", feature)
	}
	return placement(requested), nil
}

// validateContainerWorkspace rejects a container placement whose file panel
// still reads this process's own disk.
//
// Stage 2 of docs/EXECUTION-BOUNDARY.md makes the sandbox's workspace a volume of
// its own: nothing outside the sandbox mounts it. The file panel is how the user
// sees that workspace, and the skill projection writes into it; both go through
// the sandbox API. A deployment that pins the panel to the host would start
// healthy and then show an empty tree beside an agent that is editing files, which
// is worse than refusing to start.
//
// Stage 4 removed the last case that made this conditional: every container
// placement now has a detached workspace, so the check no longer depends on a
// flag. -sandbox local is unaffected — there the host directory *is* the
// workspace, which is why the rule is about the provider and not about panels in
// general.
func validateContainerWorkspace(kind, files string) error {
	if !isContainerSandbox(kind) {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(files), string(placeRemote)) {
		return nil
	}
	return fmt.Errorf("a %s sandbox keeps the workspace inside the container, so the file panel must be remote too (got -sandbox-files %s); the agent runs in the sandbox and no host directory holds its files", kind, files)
}

// rejectDisabledIsolation refuses a deployment that asks for the pre-stage-4
// shape: a container sandbox with the agent running beside the control plane.
//
// It is a refusal rather than a warning because the failure mode is exactly what
// stages 1–3 removed — an agent that can read /data: the database, master.key,
// every tenant's workspace. An operator who writes `-agent-in-sandbox=false` (or
// inherits it from an older compose file) gets a startup error that names the
// flag, the reason and the way to run unsandboxed on purpose (-sandbox local, the
// development path).
//
// Both directions are decided here:
//
//   - `true` is accepted and does nothing: it is what an existing compose file
//     says, and it agrees with the only placement that exists (PIFORGE_AGENT_IN_SANDBOX
//     is still read for the same reason, and a nonsense value is still an error
//     rather than a silent default).
//   - `false` on docker/runner is a startup error.
//   - on -sandbox local the switch cannot mean anything — there is no container to
//     put the agent in — so a stale `true` is reported once and ignored. Failing a
//     development start because a retired variable is still exported would add a
//     failure mode with no security value; the returned warning is what keeps that
//     from being silent.
//
// Stage 4 of docs/EXECUTION-BOUNDARY.md §10 is why this is all that is left of a
// flag that used to choose between two executions.
func rejectDisabledIsolation(fs *flag.FlagSet, value bool, kind string) (warning string, err error) {
	envRaw := strings.ToLower(strings.TrimSpace(os.Getenv("PIFORGE_AGENT_IN_SANDBOX")))
	envValue, envSet := parseIsolationSwitch(envRaw)
	if envRaw != "" && !envSet {
		// Same rule as stage 3: an operator who wrote "yes" expects it to mean
		// something, so a value this process cannot read is an error.
		return "", fmt.Errorf("PIFORGE_AGENT_IN_SANDBOX must be 0 or 1, got %q", os.Getenv("PIFORGE_AGENT_IN_SANDBOX"))
	}
	// What the deployment asked for, in the order it can express it: the flag
	// when it was passed, then the environment, and "nothing" when neither said
	// anything (which is the honest answer for a compose file that never had the
	// switch).
	asked, requested := false, false
	source := ""
	if fs != nil && setBy(fs, "agent-in-sandbox") {
		asked, requested, source = true, value, "-agent-in-sandbox"
	} else if envSet {
		asked, requested, source = true, envValue, "PIFORGE_AGENT_IN_SANDBOX"
	}
	if !isContainerSandbox(kind) {
		if asked && requested {
			return fmt.Sprintf("%s is retired and has no effect for -sandbox %s: the agent there is this process's child, and there is no sandbox to run it in. Remove it from the deployment's configuration",
				source, kind), nil
		}
		return "", nil
	}
	if asked && !requested {
		return "", fmt.Errorf("-agent-in-sandbox=false is not available for -sandbox %s: the agent always runs inside the sandbox, because running it beside the control plane lets it read /data (docs/EXECUTION-BOUNDARY.md §10.1). "+
			"Remove the flag to keep the isolated placement, or run -sandbox local for an unsandboxed development deployment", kind)
	}
	return "", nil
}

// setBy reports whether fs saw that flag on the command line.
func setBy(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// parseIsolationSwitch reads the retired switch's environment value. The second
// result is false for a value that is neither an affirmation nor a negation, so
// the caller can reject a typo instead of treating it as "unset".
func parseIsolationSwitch(raw string) (bool, bool) {
	switch raw {
	case "":
		return false, false
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}
