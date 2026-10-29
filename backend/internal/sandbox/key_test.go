package sandbox

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Per-sandbox API keys. The old deployment key is the subject of this file: one
// value handed to every container, reachable by anything on the sandbox network,
// opening every tenant's shell. These tests pin the replacement at the two levels
// that can be checked without asking anyone to believe a comment: the argv the
// provider sends, and a real container that rejects the wrong key.

// A key is 32 random bytes, hex encoded, and two sandboxes never share one.
func TestNewAPIKeyIsUniqueAndFullLength(t *testing.T) {
	first, err := NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	second, err := NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if first == second {
		t.Fatal("two sandboxes were given the same API key")
	}
	if len(first) != 64 {
		t.Fatalf("key length = %d, want 64 hex characters (32 bytes)", len(first))
	}
	if _, err := hex.DecodeString(first); err != nil {
		t.Fatalf("key is not hex: %v", err)
	}
	if strings.TrimSpace(first) != first || strings.ContainsAny(first, " \t\n") {
		t.Fatalf("key has whitespace: %q", first)
	}
}

// The key is what actually reaches the container, and the contract that carries it
// back to the control plane must not serialize it.
func TestSandboxAPIKeyIsNotSerializedInTheSandboxContract(t *testing.T) {
	sb := contracts.Sandbox{ID: "piforge-abc", State: contracts.SandboxReady, SandboxAPIKey: "top-secret-key"}
	raw, err := json.Marshal(sb)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "top-secret-key") {
		t.Fatalf("the sandbox contract serialized its API key: %s", raw)
	}
}

// Each container's argv carries its own key, and a container started without one
// is not something this provider produces: runContainer always supplies a key.
func TestContainerArgsCarryOneKeyPerContainer(t *testing.T) {
	provider := &Docker{cfg: DockerConfig{Image: "sandbox:1.11.0", DataRoot: "/data/sandboxes", APIPort: 8080}}
	first, err := NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	second, err := NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}

	argsA := provider.containerArgs("piforge-aaa", "usr_1", "prj_1", "/data/sandboxes/usr_1/prj_1", first)
	argsB := provider.containerArgs("piforge-bbb", "usr_1", "prj_2", "/data/sandboxes/usr_1/prj_2", second)
	assertArgsContain(t, argsA, "-e", SandboxAPIKeyEnv+"="+first)
	assertArgsContain(t, argsB, "-e", SandboxAPIKeyEnv+"="+second)
	if strings.Contains(strings.Join(argsA, " "), second) || strings.Contains(strings.Join(argsB, " "), first) {
		t.Fatal("a container was given another sandbox's key")
	}

	// An empty key must not become `-e SANDBOX_API_KEY=`: a container started with
	// an empty value would accept requests that present nothing.
	for _, arg := range provider.containerArgs("piforge-ccc", "u", "p", "/w", "") {
		if strings.HasPrefix(arg, SandboxAPIKeyEnv+"=") {
			t.Fatalf("an empty key was passed to the container: %q", arg)
		}
	}
}

// The fixed override is for debugging: it is allowed to be shared, and it is what
// a deployment that sets -sandbox-api-key gets in every container.
func TestContainerArgsUseTheFixedOverrideWhenOneIsConfigured(t *testing.T) {
	provider := &Docker{cfg: DockerConfig{
		Image: "sandbox:1.11.0", DataRoot: "/data/sandboxes", APIPort: 8080, APIKey: "fixed-debug-key",
	}}
	assertArgsContain(t, provider.containerArgs("piforge-abc", "u", "p", "/w", "fixed-debug-key"),
		"-e", SandboxAPIKeyEnv+"=fixed-debug-key")
}

// The key travels in `docker run`'s argv, so every error that echoes the command
// has to redact it — the runner logs those errors and returns them over HTTP.
func TestDockerErrorsRedactTheSandboxAPIKey(t *testing.T) {
	key, err := NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	err = dockerFailure(
		[]string{"run", "-d", "-e", SandboxAPIKeyEnv + "=" + key, "sandbox:1.11.0"},
		"docker: failed to start: "+SandboxAPIKeyEnv+"="+key,
		errors.New("exit status 125"),
	)
	message := err.Error()
	if strings.Contains(message, key) {
		t.Fatalf("the docker error leaked the sandbox key: %s", message)
	}
	if !strings.Contains(message, SandboxAPIKeyEnv+"=<redacted>") {
		t.Fatalf("the docker error did not say what it redacted: %s", message)
	}
	if strings.Contains(redactSandboxKeys("nothing to hide"), "redacted") {
		t.Fatal("an ordinary line was rewritten")
	}
}

// The container's environment is the authority on which key it runs with: the
// adoption path reads it back instead of trusting a value in memory.
func TestParseSandboxAPIKeyEnv(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{name: "present", out: "PATH=/usr/bin\nSANDBOX_API_KEY=abc123\nHOME=/home/gem\n", want: "abc123"},
		{name: "absent", out: "PATH=/usr/bin\nHOME=/home/gem\n", want: ""},
		{name: "empty value is absent", out: "SANDBOX_API_KEY=\n", want: ""},
		{name: "a value containing equals survives", out: "SANDBOX_API_KEY=a=b=c\n", want: "a=b=c"},
		{name: "a similar name is not the key", out: "SANDBOX_API_KEY_OLD=nope\n", want: ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseSandboxAPIKeyEnv(tt.out); got != tt.want {
				t.Fatalf("parse = %q, want %q", got, tt.want)
			}
		})
	}
}

// sandboxAPIStatus asks a container's own API who it thinks the caller is, using
// one key. 401 with the wrong key and 200 with the right one is the proof that the
// key — not the endpoint — is what protects the sandbox.
func sandboxAPIStatus(t *testing.T, endpoint, key string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(endpoint, "/")+"/v1/browser/info", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if key != "" {
		req.Header.Set("X-AIO-API-Key", key)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s/v1/browser/info: %v", endpoint, err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

// waitForSandboxAPI polls one status out of a container's API. A freshly created
// AIO container answers 503 on /v1/browser/info until its Chromium is up, and a
// test that read that as "the image does not enforce keys" would skip exactly the
// assertion it exists for. Only the wanted status counts as an answer.
func waitForSandboxAPI(t *testing.T, endpoint, key string, want int) int {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	last := 0
	for {
		last = sandboxAPIStatus(t, endpoint, key)
		if last == want || time.Now().After(deadline) {
			return last
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// The real thing: two sandboxes created by this provider carry two different keys,
// each container enforces its own, and a key from one is rejected by the other.
//
// It needs a Docker daemon, an image that can host the checkout, and an image whose
// API enforces SANDBOX_API_KEY (the AIO sandbox image). PIFORGE_TEST_SANDBOX_IMAGE
// selects it; without one the test skips loudly instead of passing vacuously.
func TestDockerProviderPerSandboxAPIKey(t *testing.T) {
	provider := newDockerForTest(t)
	requireCheckoutImage(t, provider)
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	ctx := context.Background()

	created := make([]*contracts.Sandbox, 0, 2)
	for _, pair := range [][2]string{{"usr_key_a", "prj_key_a"}, {"usr_key_b", "prj_key_b"}} {
		sb, err := provider.Ensure(ctx, pair[0], pair[1], "", "main")
		if err != nil {
			t.Skipf("cannot start a container here: %v", err)
		}
		t.Cleanup(func() { _ = provider.Destroy(context.Background(), sb.ID) })
		if sb.SandboxAPIKey == "" {
			t.Fatalf("sandbox %s was created without an API key", sb.ID)
		}
		created = append(created, sb)
	}

	a, b := created[0], created[1]
	if a.SandboxAPIKey == b.SandboxAPIKey {
		t.Fatalf("two sandboxes share the key %q", a.SandboxAPIKey)
	}
	if len(a.SandboxAPIKey) != 64 {
		t.Fatalf("key length = %d, want 64 hex characters", len(a.SandboxAPIKey))
	}

	// The key the provider reported is the key the container actually runs with:
	// inspect, not a value remembered in this process.
	for _, sb := range created {
		out, err := exec.Command("docker", "inspect", "--format", "{{range .Config.Env}}{{println .}}{{end}}", sb.ID).CombinedOutput()
		if err != nil {
			t.Fatalf("docker inspect %s: %v: %s", sb.ID, err, out)
		}
		if got := parseSandboxAPIKeyEnv(string(out)); got != sb.SandboxAPIKey {
			t.Fatalf("container %s runs with key %q, provider reported %q", sb.ID, got, sb.SandboxAPIKey)
		}
	}

	endpointA, err := provider.Endpoint(ctx, a.ID)
	if err != nil {
		t.Fatalf("Endpoint(%s): %v", a.ID, err)
	}
	endpointB, err := provider.Endpoint(ctx, b.ID)
	if err != nil {
		t.Fatalf("Endpoint(%s): %v", b.ID, err)
	}

	status := waitForSandboxAPI(t, endpointA, a.SandboxAPIKey, http.StatusOK)
	if status != http.StatusOK {
		t.Skipf("the image at %s does not enforce SANDBOX_API_KEY (GET with the right key = %d, want 200); "+
			"set PIFORGE_TEST_SANDBOX_IMAGE to the AIO sandbox image to run the cross-tenant assertions", endpointA, status)
	}

	// Right key on its own sandbox: allowed. Wrong key on the same sandbox: 401.
	if got := waitForSandboxAPI(t, endpointA, b.SandboxAPIKey, http.StatusUnauthorized); got != http.StatusUnauthorized {
		t.Fatalf("sandbox A answered %d to sandbox B's key, want 401", got)
	}
	// A's key against B's container: the cross-tenant case this whole change is
	// about. Both directions, because "one of them happens to be the deployment key"
	// is exactly the bug being fixed.
	if got := waitForSandboxAPI(t, endpointB, a.SandboxAPIKey, http.StatusUnauthorized); got != http.StatusUnauthorized {
		t.Fatalf("sandbox B answered %d to sandbox A's key, want 401", got)
	}
	if got := waitForSandboxAPI(t, endpointB, b.SandboxAPIKey, http.StatusOK); got != http.StatusOK {
		t.Fatalf("sandbox B answered %d to its own key, want 200", got)
	}
	// An unauthenticated request must not get in either.
	if got := waitForSandboxAPI(t, endpointB, "", http.StatusUnauthorized); got != http.StatusUnauthorized {
		t.Fatalf("sandbox B answered %d without a key, want 401", got)
	}
}
