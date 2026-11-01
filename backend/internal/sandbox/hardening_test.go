package sandbox

import (
	"context"
	"testing"
)

// TestContainerArgsHardening pins the two argv rules an adversarial acceptance
// run showed were missing: the container keeps no capabilities beyond the set
// the image measurably needs, and no_new_privs is set so the image's
// passwordless setuid sudo cannot produce a root shell for the tenant.
func TestContainerArgsHardening(t *testing.T) {
	provider := &Docker{cfg: DockerConfig{Image: "sandbox:1.11.0", DataRoot: "/data/sandboxes", APIPort: 8080}}
	args := provider.containerArgs(context.Background(), "piforge-abc", "usr_1", "prj_1", "/w", "k")

	assertArgsContain(t, args, "--security-opt", "no-new-privileges")
	assertArgsContain(t, args, "--cap-drop", "ALL")
	// The five the AIO image's own bootstrap needs, and nothing else. Each is
	// justified in docker.go; the test is what keeps the list from growing
	// quietly, because every added capability is a wider sandbox.
	for _, capability := range []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETUID", "SETGID"} {
		assertArgsContain(t, args, "--cap-add", capability)
	}
	for _, capability := range []string{"NET_RAW", "SYS_ADMIN", "SYS_CHROOT", "MKNOD", "SETFCAP", "KILL", "AUDIT_WRITE", "NET_BIND_SERVICE"} {
		assertArgsAbsent(t, args, capability)
	}
	// One cap-drop, one cap-add per capability — not a --privileged in disguise.
	adds := 0
	for i, arg := range args {
		if arg == "--cap-add" && i+1 < len(args) {
			adds++
		}
	}
	if adds != len(sandboxCapAdd) {
		t.Fatalf("argv adds %d capabilities, want %d", adds, len(sandboxCapAdd))
	}
	if args[len(args)-1] != "infinity" {
		t.Fatalf("the container does not idle: last arg %q", args[len(args)-1])
	}
}


