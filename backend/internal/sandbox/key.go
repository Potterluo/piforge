package sandbox

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// Per-sandbox API keys.
//
// A deployment used to start every container with one SANDBOX_API_KEY, passed
// everywhere as -sandbox-api-key. That was survivable only while each sandbox
// published a random port on the loopback interface: the endpoint was the
// secret. The runner changed the endpoint to a container name on a shared
// network (http://piforge-<hash8>:8080), so anything on that network can reach
// every sandbox — and one leaked key then opened every tenant's browser, shell
// and files.
//
// So a key belongs to one container. It is generated where the container is
// created, passed to it as SANDBOX_API_KEY, and reported back to the control
// plane once over the (bearer-protected, internal) runner API so the row can
// keep it for every later call. The alternative — the control plane generates
// and pushes the key down on create — puts the secret on the wire on every
// create, and still leaves the runner unable to answer for a container it
// adopted (the runner is stateless; it would have to inspect the container to
// learn the key either way). Generating at the point of creation means the key
// travels exactly once, upward, and never has to be re-sent.

// SandboxAPIKeyEnv is the environment variable the sandbox image reads its API
// key from. The runner and the provider both use this name; an operator reading
// `docker inspect` sees the same thing.
const SandboxAPIKeyEnv = "SANDBOX_API_KEY"

// NewAPIKey returns a fresh sandbox API key: 32 bytes from crypto/rand, hex
// encoded (64 characters, no padding). crypto/rand, not math/rand: this value is
// the only thing between one tenant's shell and another tenant that can reach
// the container.
func NewAPIKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("sandbox: generate an API key: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
