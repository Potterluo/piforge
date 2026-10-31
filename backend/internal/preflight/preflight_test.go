package preflight

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeInspector is the whole Docker surface the preflight uses. Every check's
// decision is tested through it: which image is absent, what the probe prints,
// which Engine answers.
type fakeInspector struct {
	engine    string
	engineErr error
	images    map[string]bool
	probeOut  string
	probeErr  error
	probed    []string
}

func (f *fakeInspector) EngineVersion(context.Context) (string, error) {
	return f.engine, f.engineErr
}

func (f *fakeInspector) ImageExists(_ context.Context, ref string) (bool, error) {
	return f.images[ref], nil
}

func (f *fakeInspector) ImageProbe(_ context.Context, ref, _ string) (string, error) {
	f.probed = append(f.probed, ref)
	return f.probeOut, f.probeErr
}

func healthy() *fakeInspector {
	return &fakeInspector{
		engine: "29.9.2",
		images: map[string]bool{
			"piforge:isolated":                true,
			"piforge/sandbox-runner:isolated": true,
			"piforge/sandbox-agent:isolated":  true,
		},
		probeOut: "PF_LAUNCHER_OK\nPF_PI_OK\n",
	}
}

func testConfig() Config {
	return Config{
		ControlPlaneImage: "piforge:isolated",
		RunnerImage:       "piforge/sandbox-runner:isolated",
		SandboxImage:      "piforge/sandbox-agent:isolated",
	}
}

// The happy path is the one command's promise: three images, an Engine new
// enough for volume subpaths, and an image that can start the agent.
func TestPreflightPassesAWorkingHost(t *testing.T) {
	report := Run(context.Background(), testConfig(), healthy())
	if !report.OK() {
		t.Fatalf("a healthy host failed:\n%s", strings.Join(report.Lines(), "\n"))
	}
	if len(report.Failures()) != 0 {
		t.Fatalf("failures = %v", report.Failures())
	}
}

// The acceptance's actual trap: the tag exists, the image cannot run the agent.
// The failure has to name /opt/piforge/piforged, not "preflight failed".
func TestPreflightNamesAnImageThatCannotRunTheAgent(t *testing.T) {
	insp := healthy()
	// This is the measured answer of the shipped base image.
	insp.images["piforge/sandbox-agent:isolated"] = true
	insp.probeOut = ""

	report := Run(context.Background(), testConfig(), insp)
	if report.OK() {
		t.Fatal("an image with no agent runtime passed the preflight")
	}
	lines := strings.Join(report.Lines(), "\n")
	if !strings.Contains(lines, LauncherPath) {
		t.Fatalf("the failure does not name the missing launcher:\n%s", lines)
	}
	if !strings.Contains(lines, "deploy/Dockerfile.sandbox-agent") {
		t.Fatalf("the failure does not name the fix:\n%s", lines)
	}
	// Both halves are reported in one run, so the operator does not rebuild
	// twice to discover the second missing binary.
	if !strings.Contains(lines, PiBinary) {
		t.Fatalf("the failure does not name the missing pi:\n%s", lines)
	}
}

func TestPreflightNamesAMissingImage(t *testing.T) {
	insp := healthy()
	delete(insp.images, "piforge/sandbox-runner:isolated")
	report := Run(context.Background(), testConfig(), insp)
	if report.OK() {
		t.Fatal("a missing runner image passed")
	}
	lines := strings.Join(report.Lines(), "\n")
	if !strings.Contains(lines, "sandbox-runner image") {
		t.Fatalf("the failure does not name the image:\n%s", lines)
	}
	if !strings.Contains(lines, "Dockerfile.runner") {
		t.Fatalf("the failure does not say how to build it:\n%s", lines)
	}
}

// Engine 24 has no volume-subpath mount; letting it through means the first
// sandbox dies twenty minutes later with a daemon error nobody can place.
func TestPreflightRejectsAnOldEngine(t *testing.T) {
	insp := healthy()
	insp.engine = "24.0.7"
	report := Run(context.Background(), testConfig(), insp)
	if report.OK() {
		t.Fatal("Engine 24 passed")
	}
	lines := strings.Join(report.Lines(), "\n")
	if !strings.Contains(lines, "24.0.7") || !strings.Contains(lines, "volume-subpath") {
		t.Fatalf("the failure does not explain the engine requirement:\n%s", lines)
	}
}

func TestPreflightReportsAStoppedDaemon(t *testing.T) {
	insp := healthy()
	insp.engineErr = errors.New("docker version: Cannot connect to the Docker daemon")
	report := Run(context.Background(), testConfig(), insp)
	if report.OK() {
		t.Fatal("a stopped daemon passed")
	}
	if !strings.Contains(strings.Join(report.Lines(), "\n"), "Cannot connect") {
		t.Fatalf("the daemon error is missing:\n%s", strings.Join(report.Lines(), "\n"))
	}
}

// One run reports every problem: a host missing all three images must not make
// the operator rebuild three times.
func TestPreflightReportsEverythingAtOnce(t *testing.T) {
	insp := &fakeInspector{engine: "29.9.2", images: map[string]bool{}, probeOut: ""}
	report := Run(context.Background(), testConfig(), insp)
	if len(report.Failures()) != 5 {
		t.Fatalf("failures = %d, want 5:\n%s", len(report.Failures()), strings.Join(report.Lines(), "\n"))
	}
}

// The probe asks about the sandbox image and does not start a container for the
// other two: the sandbox image is the 13 GB one.
func TestPreflightProbesOnlyTheSandboxImage(t *testing.T) {
	insp := healthy()
	Run(context.Background(), testConfig(), insp)
	if len(insp.probed) != 1 || insp.probed[0] != "piforge/sandbox-agent:isolated" {
		t.Fatalf("probed = %v", insp.probed)
	}
}

func TestEngineVersionParsing(t *testing.T) {
	for _, tc := range []struct {
		version string
		ok      bool
	}{
		{"29.9.2", true},
		{"25.0.0", true},
		{"v26.1.4", true},
		{"24.0.7", false},
		{"", false},
		{"not-a-version", false},
	} {
		if got := engineAtLeast(tc.version, MinEngine); got != tc.ok {
			t.Errorf("engineAtLeast(%q) = %v, want %v", tc.version, got, tc.ok)
		}
	}
}
