package main

import (
	"fmt"
	"strings"
)

// placement is where one panel feature runs: on the host or inside the sandbox
// container. "auto" is a request, not a placement, and never survives resolution.
type placement string

const (
	// placeAuto defers to the provider: a docker sandbox puts the feature inside
	// the container, a local sandbox leaves it on the host. It is the default so
	// that "which provider" and "where the panels look" stay one decision.
	placeAuto placement = "auto"
	// placeHost runs the feature on the control plane's machine.
	placeHost placement = "host"
	// placeRemote runs the feature inside the docker sandbox.
	placeRemote placement = "remote"
)

// sandboxPlacement is the resolved answer: exactly one placement per feature.
type sandboxPlacement struct {
	Browser  placement
	Terminal placement
	Files    placement

	// docker records which provider the answer was resolved against, because it
	// changes what "host" means: a choice under docker, the only possibility
	// under local.
	docker bool
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
// Only docker can be mixed: under local there is no container, so every feature on
// the host is the whole deployment, not half of one. Under docker a single host
// feature is the arrangement nobody picks on purpose, which is why the caller
// warns about it.
func (p sandboxPlacement) Mixed() bool {
	return p.docker && len(p.HostFeatures()) > 0
}

// resolveSandboxPlacement turns the three requested placements into the ones the
// process will actually use.
//
// The rule is that the provider decides by default: a docker sandbox means the
// agent, the browser, the terminal and the file panel all look at one container,
// because that is the only arrangement where they all see the same bytes. A
// per-feature "host" is still honoured — someone may genuinely want the local
// Chrome against a containerised workspace — but it has to be asked for, and the
// caller reports it, so a half-inside deployment cannot happen by omission.
//
// Remote without a container is not a degraded mode, it is a contradiction, so it
// is rejected at startup instead of failing on the first request.
func resolveSandboxPlacement(kind, browser, terminal, files string) (sandboxPlacement, error) {
	docker := normalizedSandboxKind(kind) == "docker"
	var p sandboxPlacement
	p.docker = docker
	for _, req := range []struct {
		feature   string
		requested string
		dst       *placement
	}{
		{"browser", browser, &p.Browser},
		{"terminal", terminal, &p.Terminal},
		{"files", files, &p.Files},
	} {
		resolved, err := resolvePlacement(req.feature, req.requested, docker)
		if err != nil {
			return sandboxPlacement{}, err
		}
		*req.dst = resolved
	}
	return p, nil
}

// normalizedSandboxKind trims the flag value and lowercases the provider name, so
// "-sandbox Docker " is the provider it looks like. Anything else is left alone:
// the wiring below treats every non-docker kind as the local provider.
func normalizedSandboxKind(kind string) string {
	if strings.EqualFold(strings.TrimSpace(kind), "docker") {
		return "docker"
	}
	return kind
}

func resolvePlacement(feature, requested string, docker bool) (placement, error) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	switch requested {
	case "", string(placeAuto):
		if docker {
			return placeRemote, nil
		}
		return placeHost, nil
	case string(placeHost), string(placeRemote):
		// Explicit, and validated below.
	default:
		return "", fmt.Errorf("-sandbox-%s must be host, remote or auto, got %q", feature, requested)
	}
	if placement(requested) == placeRemote && !docker {
		// Naming the reason, not just the rule: the flag is not wrong in itself, it
		// is wrong for this provider, and the operator needs to know which knob to
		// turn instead of guessing.
		return "", fmt.Errorf("-sandbox-%s remote needs -sandbox docker: with -sandbox local there is no container to talk to, so this feature can only run on the host", feature)
	}
	return placement(requested), nil
}
