package main

import (
	"flag"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/sandbox"
)

// The Kubernetes provider's startup wiring: which flags are its own, what happens
// when one is given to another provider, and which combinations are refused
// because the process cannot make them true.
//
// The provider itself is tested in internal/sandbox (against client-go's fake
// clientset). What is pinned here is the part only cmd/piforged owns: the flag set,
// the refusal that names the wrong provider, and the boot gates that a Kubernetes
// deployment must not pass while claiming a Docker boundary.

// k8sTestFlags registers the provider's flags on a fresh FlagSet the way run() does.
func k8sTestFlags(t *testing.T) (*flag.FlagSet, *options) {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	opt := &options{}
	defineK8sFlags(fs, opt)
	return fs, opt
}

// TestValidateK8sFlags: the flags belong to -sandbox k8s, and a command line that
// names one under another provider is refused instead of ignored.
func TestValidateK8sFlags(t *testing.T) {
	t.Run("k8s accepts its own flags", func(t *testing.T) {
		fs, opt := k8sTestFlags(t)
		args := []string{
			"-sandbox-k8s-kubeconfig", "/tmp/kubeconfig",
			"-sandbox-k8s-context", "kind-kind",
			"-sandbox-k8s-namespace", "sandboxes",
			"-sandbox-k8s-runtime-class", "kata",
			"-sandbox-k8s-storage-class", "fast",
			"-sandbox-k8s-disk", "20Gi",
			"-sandbox-k8s-service-account", "sandbox",
			"-sandbox-k8s-endpoint", "http://127.0.0.1:18080",
			"-sandbox-k8s-cluster-domain", "cluster.local",
			"-sandbox-k8s-read-only-rootfs=false",
			"-sandbox-k8s-writable-dirs", "/tmp,/var/tmp",
			"-sandbox-k8s-allow-runc",
			"-sandbox-k8s-allow-root-user",
			"-sandbox-k8s-run-as-user", "1001",
			"-sandbox-k8s-grace-stop", "5s",
		}
		if err := fs.Parse(args); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if err := validateK8sFlags(fs, "k8s", *opt); err != nil {
			t.Fatalf("validateK8sFlags(k8s) = %v", err)
		}
		if opt.k8sNamespace != "sandboxes" || opt.k8sRuntimeClass != "kata" || opt.k8sDiskSize != "20Gi" {
			t.Fatalf("flags did not land: %+v", *opt)
		}
		if opt.k8sReadOnlyRootFS {
			t.Fatal("-sandbox-k8s-read-only-rootfs=false was ignored")
		}
		if opt.k8sRunAsUser != 1001 || opt.k8sGraceStop != 5*time.Second {
			t.Fatalf("run-as-user/grace-stop = %d/%s", opt.k8sRunAsUser, opt.k8sGraceStop)
		}
	})

	t.Run("a Kubernetes flag under another provider is refused", func(t *testing.T) {
		for _, kind := range []string{"local", "docker", "runner"} {
			fs, opt := k8sTestFlags(t)
			if err := fs.Parse([]string{"-sandbox-k8s-namespace", "piforge"}); err != nil {
				t.Fatalf("parse: %v", err)
			}
			err := validateK8sFlags(fs, kind, *opt)
			if err == nil {
				t.Fatalf("-sandbox-k8s-namespace was accepted under -sandbox %s", kind)
			}
			for _, want := range []string{"-sandbox-k8s-namespace", kind, "only apply to -sandbox k8s"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal %q does not name %q", err.Error(), want)
				}
			}
		}
	})

	// The defaults are real values (a bool default of `true`, a uid of 1000, a
	// grace period): refusing them would make `-sandbox docker` impossible.
	t.Run("defaults are not a refusal", func(t *testing.T) {
		fs, opt := k8sTestFlags(t)
		if err := fs.Parse(nil); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !opt.k8sReadOnlyRootFS || opt.k8sRunAsUser != sandbox.DefaultK8sRunAsUser || opt.k8sGraceStop <= 0 {
			t.Fatalf("defaults = %+v", *opt)
		}
		if err := validateK8sFlags(fs, "docker", *opt); err != nil {
			t.Fatalf("the defaults were read as a Kubernetes configuration: %v", err)
		}
	})

	// An environment value is as much an instruction as a flag, so it is refused
	// the same way (the string flags default to empty, which is what makes this
	// decidable).
	t.Run("an environment value under another provider is refused", func(t *testing.T) {
		fs, opt := k8sTestFlags(t)
		opt.k8sNamespace = "piforge"
		if err := validateK8sFlags(fs, "docker", *opt); err == nil ||
			!strings.Contains(err.Error(), "PIFORGE_SANDBOX_K8S_NAMESPACE") {
			t.Fatalf("validateK8sFlags = %v, want a refusal naming the variable", err)
		}
	})
}

// TestValidateTenantWiringK8s: a per-tenant Docker bridge has no Kubernetes
// meaning, and the process does not create NetworkPolicies, so the flag is a
// promise nobody keeps.
func TestValidateTenantWiringK8s(t *testing.T) {
	if err := validateTenantWiring("k8s", "tenant", nil, 0); err == nil {
		t.Fatal("-sandbox-network-mode tenant was accepted under -sandbox k8s")
	} else if !strings.Contains(err.Error(), "NetworkPolicy") {
		t.Fatalf("refusal = %v, want it to name the NetworkPolicy that does apply", err)
	}
	if err := validateTenantWiring("k8s", "shared", nil, 3); err != nil {
		t.Fatalf("the default network mode under k8s: %v", err)
	}
	// The count guard is not a network: it still applies.
	if err := validateTenantWiring("k8s", "shared", nil, -1); err == nil {
		t.Fatal("a negative -tenant-max-sandboxes was accepted")
	}
}

// TestK8sFlagNamesAreRegistered keeps the refusal list and the flag set from
// drifting: a rename would otherwise leave a flag that is never refused.
func TestK8sFlagNamesAreRegistered(t *testing.T) {
	fs, _ := k8sTestFlags(t)
	registered := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) { registered[f.Name] = true })
	for _, name := range k8sFlagNames {
		if !registered[name] {
			t.Fatalf("k8sFlagNames lists %q, which defineK8sFlags does not register", name)
		}
	}
	if len(registered) != len(k8sFlagNames) {
		t.Fatalf("defineK8sFlags registers %d flags, k8sFlagNames lists %d", len(registered), len(k8sFlagNames))
	}
}

// TestNewK8sSandboxRefusesWithoutAContext is the one construction failure that
// needs no cluster: a kubeconfig that does not exist must fail the start with the
// flag named, not hang.
func TestNewK8sSandboxRefusesWithoutAContext(t *testing.T) {
	opt := options{
		sandboxImage:      "piforge/sandbox-agent:test",
		k8sKubeconfig:     "/nonexistent/kubeconfig",
		k8sNamespace:      "piforge",
		k8sReadOnlyRootFS: true,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := newK8sSandbox(t.Context(), opt, nil, logger); err == nil {
		t.Fatal("newK8sSandbox accepted a kubeconfig that does not exist")
	} else if !strings.Contains(err.Error(), "kubeconfig") && !strings.Contains(err.Error(), "Kubernetes context") {
		t.Fatalf("error = %v, want it to name the kubeconfig or the context", err)
	}
}
