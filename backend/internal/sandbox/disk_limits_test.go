package sandbox

import (
	"context"
	"testing"
)

// TestContainerArgsDiskCap pins the kernel-level half of the disk quota: every
// process in the container inherits RLIMIT_FSIZE at the budget, soft and hard
// the same, so `ulimit -f unlimited` in the tenant's own shell cannot lift it
// and one `fallocate` cannot outgrow the quota.
func TestContainerArgsDiskCap(t *testing.T) {
	const budget = int64(2 << 30)
	provider := &Docker{cfg: DockerConfig{Image: "sandbox:1.11.0", DataRoot: "/data/sandboxes", APIPort: 8080, DiskBytes: budget}}
	args := provider.containerArgs(context.Background(), "piforge-abc", "usr_1", "prj_1", "/w", "k")
	assertArgsContain(t, args, "--ulimit", "fsize=2147483648:2147483648")

	// A per-tenant override wins over the deployment default, the same way CPUs
	// and memory do.
	withPolicy := &Docker{cfg: DockerConfig{
		Image: "sandbox:1.11.0", DataRoot: "/data/sandboxes", APIPort: 8080, DiskBytes: budget,
		LimitsFor: func(context.Context, string) Limits { return Limits{DiskBytes: 64 << 20} },
	}}
	args = withPolicy.containerArgs(context.Background(), "piforge-abc", "usr_1", "prj_1", "/w", "k")
	assertArgsContain(t, args, "--ulimit", "fsize=67108864:67108864")

	// No budget, no limit: a deployment that disables the quota must not get a
	// cap of zero, which would refuse every write.
	off := &Docker{cfg: DockerConfig{Image: "sandbox:1.11.0", DataRoot: "/data/sandboxes", APIPort: 8080}}
	args = off.containerArgs(context.Background(), "piforge-abc", "usr_1", "prj_1", "/w", "k")
	assertArgsAbsent(t, args, "--ulimit")

	// A tenant policy that says nothing about disk must not switch the cap off:
	// the policy has no disk field, so the deployment's quota fills the gap.
	silent := &Docker{cfg: DockerConfig{
		Image: "sandbox:1.11.0", DataRoot: "/data/sandboxes", APIPort: 8080, DiskBytes: budget,
		LimitsFor: func(context.Context, string) Limits { return Limits{CPUs: "1", Pids: 64} },
	}}
	args = silent.containerArgs(context.Background(), "piforge-abc", "usr_1", "prj_1", "/w", "k")
	assertArgsContain(t, args, "--ulimit", "fsize=2147483648:2147483648")
	assertArgsContain(t, args, "--pids-limit", "64")
}


