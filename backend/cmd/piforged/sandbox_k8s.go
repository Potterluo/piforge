package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/sandbox"
)

// The Kubernetes provider's flags, and the startup gate that makes a broken
// cluster configuration a refusal instead of a first-session failure.
//
// Three decisions are made here rather than inside the provider:
//
//   - the *defaults* are empty strings; the provider fills "piforge", "gvisor",
//     "10Gi" and "cluster.local" itself (sandbox.K8sConfig.normalise). That is what
//     lets validateK8sFlags tell "the operator wrote -sandbox-k8s-namespace" from
//     "nobody said anything" without a second copy of every default;
//   - a Kubernetes flag given to another provider is refused (like the runner
//     credentials), so a command line copied from a Kubernetes deployment does not
//     look like it configured something it did not;
//   - preflight runs before anything is served. It asks the cluster: is there a
//     namespace, is the runtime class installed, may this identity create a pod,
//     stream pods/exec, manage a claim and a service, read a secret. Every failure
//     names the manifest or the flag to change.

// k8sFlagNames is every flag this provider owns. One list, so the refusal under a
// different provider cannot drift from what was registered.
var k8sFlagNames = []string{
	"sandbox-k8s-kubeconfig",
	"sandbox-k8s-context",
	"sandbox-k8s-namespace",
	"sandbox-k8s-runtime-class",
	"sandbox-k8s-allow-runc",
	"sandbox-k8s-storage-class",
	"sandbox-k8s-disk",
	"sandbox-k8s-service-account",
	"sandbox-k8s-run-as-user",
	"sandbox-k8s-allow-root-user",
	"sandbox-k8s-read-only-rootfs",
	"sandbox-k8s-writable-dirs",
	"sandbox-k8s-cluster-domain",
	"sandbox-k8s-endpoint",
	"sandbox-k8s-grace-stop",
}

// defineK8sFlags registers the provider's flags. It is a function so a test can
// register the very same set on a fresh FlagSet, which is what validateK8sFlags
// inspects.
func defineK8sFlags(fs *flag.FlagSet, opt *options) {
	fs.StringVar(&opt.k8sKubeconfig, "sandbox-k8s-kubeconfig", envOr("PIFORGE_SANDBOX_K8S_KUBECONFIG", ""),
		"path of the kubeconfig the Kubernetes sandbox provider uses; empty uses the in-cluster service account, then $KUBECONFIG, then ~/.kube/config (env PIFORGE_SANDBOX_K8S_KUBECONFIG)")
	fs.StringVar(&opt.k8sContext, "sandbox-k8s-context", envOr("PIFORGE_SANDBOX_K8S_CONTEXT", ""),
		"kubeconfig context to use; empty uses its current context. Naming it is how a deployment with two clusters is sure which one it creates pods in (env PIFORGE_SANDBOX_K8S_CONTEXT)")
	fs.StringVar(&opt.k8sNamespace, "sandbox-k8s-namespace", envOr("PIFORGE_SANDBOX_K8S_NAMESPACE", ""),
		"namespace the sandbox objects live in (default piforge). It must already exist; the provider refuses to create one (env PIFORGE_SANDBOX_K8S_NAMESPACE)")
	fs.StringVar(&opt.k8sRuntimeClass, "sandbox-k8s-runtime-class", envOr("PIFORGE_SANDBOX_K8S_RUNTIME_CLASS", ""),
		"RuntimeClass every sandbox pod requests (default gvisor). Empty with -sandbox-k8s-allow-runc means the node's default runtime (env PIFORGE_SANDBOX_K8S_RUNTIME_CLASS)")
	fs.BoolVar(&opt.k8sAllowRunc, "sandbox-k8s-allow-runc", envOr("PIFORGE_SANDBOX_K8S_ALLOW_RUNC", "0") == "1",
		"accept a cluster without the sandbox runtime class instead of refusing to start: the sandbox is then an ordinary container on the node's kernel (no syscall boundary)")
	fs.StringVar(&opt.k8sStorageClass, "sandbox-k8s-storage-class", envOr("PIFORGE_SANDBOX_K8S_STORAGE_CLASS", ""),
		"StorageClass of a sandbox's workspace volume; empty uses the cluster's default class (env PIFORGE_SANDBOX_K8S_STORAGE_CLASS)")
	fs.StringVar(&opt.k8sDiskSize, "sandbox-k8s-disk", envOr("PIFORGE_SANDBOX_K8S_DISK", ""),
		"size of a sandbox's workspace volume, in Kubernetes quantity form (default 10Gi) (env PIFORGE_SANDBOX_K8S_DISK)")
	fs.StringVar(&opt.k8sServiceAccount, "sandbox-k8s-service-account", envOr("PIFORGE_SANDBOX_K8S_SERVICE_ACCOUNT", ""),
		"service account the sandbox pod runs as; empty uses the namespace default. The token is never mounted either way (env PIFORGE_SANDBOX_K8S_SERVICE_ACCOUNT)")
	fs.Int64Var(&opt.k8sRunAsUser, "sandbox-k8s-run-as-user", envInt64("PIFORGE_SANDBOX_K8S_RUN_AS_USER", sandbox.DefaultK8sRunAsUser),
		"uid the sandbox pod runs as (default 1000, the sandbox image's own account); it is also the volume's fsGroup (env PIFORGE_SANDBOX_K8S_RUN_AS_USER)")
	fs.BoolVar(&opt.k8sAllowRootUser, "sandbox-k8s-allow-root-user", envOr("PIFORGE_SANDBOX_K8S_ALLOW_ROOT_USER", "0") == "1",
		"omit runAsNonRoot from the pod so an image whose entrypoint needs root for its own bootstrap can start; the capability set and the runtime class are then the boundary")
	fs.BoolVar(&opt.k8sReadOnlyRootFS, "sandbox-k8s-read-only-rootfs", envOr("PIFORGE_SANDBOX_K8S_READ_ONLY_ROOTFS", "1") == "1",
		"mount the sandbox container's own root filesystem read-only (default true); the workspace is the volume either way")
	fs.StringVar(&opt.k8sWritableDirs, "sandbox-k8s-writable-dirs", envOr("PIFORGE_SANDBOX_K8S_WRITABLE_DIRS", ""),
		"comma-separated container paths that get an emptyDir when the root filesystem is read-only (default /tmp,/run) (env PIFORGE_SANDBOX_K8S_WRITABLE_DIRS)")
	fs.StringVar(&opt.k8sClusterDomain, "sandbox-k8s-cluster-domain", envOr("PIFORGE_SANDBOX_K8S_CLUSTER_DOMAIN", ""),
		"the cluster's DNS suffix (default cluster.local), used to build the address of a sandbox's API service (env PIFORGE_SANDBOX_K8S_CLUSTER_DOMAIN)")
	fs.StringVar(&opt.k8sEndpoint, "sandbox-k8s-endpoint", envOr("PIFORGE_SANDBOX_K8S_ENDPOINT", ""),
		"base URL that reaches a sandbox's API when this process is not in the cluster (a port-forward or an ingress); {sandbox} is replaced with the sandbox's name. Empty uses the headless service's cluster DNS name (env PIFORGE_SANDBOX_K8S_ENDPOINT)")
	fs.DurationVar(&opt.k8sGraceStop, "sandbox-k8s-grace-stop", envDuration("PIFORGE_SANDBOX_K8S_GRACE_STOP", 30*time.Second),
		"how long a sandbox pod is given to stop before the API server kills it (env PIFORGE_SANDBOX_K8S_GRACE_STOP)")
}

// envInt64 reads an integer environment value, falling back to def.
func envInt64(key string, def int64) int64 {
	raw := strings.TrimSpace(envOr(key, ""))
	if raw == "" {
		return def
	}
	var value int64
	if _, err := fmt.Sscanf(raw, "%d", &value); err != nil || value < 0 {
		return def
	}
	return value
}

// validateK8sFlags refuses the Kubernetes flags under another provider, in the
// same spirit as the runner credentials: a command line that names a namespace
// while running Docker looks configured and is not.
//
// Two ways to be "given", because the flags do not share a default shape: a
// string flag defaults to empty, so a value can only come from the flag or the
// environment; a bool or a number has a real default (true, 1000, 30s), so only an
// explicit command-line mention counts. Refusing on the *default* would make
// `-sandbox docker` impossible to start.
func validateK8sFlags(fs *flag.FlagSet, kind string, opt options) error {
	if kind == "k8s" {
		return nil
	}
	given := func(name, value string) bool {
		if fs != nil && setBy(fs, name) {
			return true
		}
		return strings.TrimSpace(value) != ""
	}
	checks := []struct {
		flag  string
		env   string
		value string
	}{
		{"sandbox-k8s-kubeconfig", "PIFORGE_SANDBOX_K8S_KUBECONFIG", opt.k8sKubeconfig},
		{"sandbox-k8s-context", "PIFORGE_SANDBOX_K8S_CONTEXT", opt.k8sContext},
		{"sandbox-k8s-namespace", "PIFORGE_SANDBOX_K8S_NAMESPACE", opt.k8sNamespace},
		{"sandbox-k8s-runtime-class", "PIFORGE_SANDBOX_K8S_RUNTIME_CLASS", opt.k8sRuntimeClass},
		{"sandbox-k8s-storage-class", "PIFORGE_SANDBOX_K8S_STORAGE_CLASS", opt.k8sStorageClass},
		{"sandbox-k8s-disk", "PIFORGE_SANDBOX_K8S_DISK", opt.k8sDiskSize},
		{"sandbox-k8s-service-account", "PIFORGE_SANDBOX_K8S_SERVICE_ACCOUNT", opt.k8sServiceAccount},
		{"sandbox-k8s-writable-dirs", "PIFORGE_SANDBOX_K8S_WRITABLE_DIRS", opt.k8sWritableDirs},
		{"sandbox-k8s-cluster-domain", "PIFORGE_SANDBOX_K8S_CLUSTER_DOMAIN", opt.k8sClusterDomain},
		{"sandbox-k8s-endpoint", "PIFORGE_SANDBOX_K8S_ENDPOINT", opt.k8sEndpoint},
		// Defaults that are meaningful values, so only the command line can be a
		// refusal.
		{"sandbox-k8s-allow-runc", "PIFORGE_SANDBOX_K8S_ALLOW_RUNC", ""},
		{"sandbox-k8s-run-as-user", "PIFORGE_SANDBOX_K8S_RUN_AS_USER", ""},
		{"sandbox-k8s-allow-root-user", "PIFORGE_SANDBOX_K8S_ALLOW_ROOT_USER", ""},
		{"sandbox-k8s-read-only-rootfs", "PIFORGE_SANDBOX_K8S_READ_ONLY_ROOTFS", ""},
		{"sandbox-k8s-grace-stop", "PIFORGE_SANDBOX_K8S_GRACE_STOP", ""},
	}
	for _, check := range checks {
		if given(check.flag, check.value) {
			return k8sFlagRefusal(kind, "-"+check.flag, check.env)
		}
	}
	return nil
}

func k8sFlagRefusal(kind, flagName, envName string) error {
	return fmt.Errorf("%s (%s) was given, but -sandbox is %q: the Kubernetes flags only apply to -sandbox k8s", flagName, envName, kind)
}

// newK8sSandbox builds the provider and refuses to start against a cluster that
// cannot host it.
//
// The preflight is bounded: a control plane that cannot reach its API server must
// fail in seconds with "not reachable", not hang a boot for the client's default
// timeout.
func newK8sSandbox(ctx context.Context, opt options, tenants *tenantResolver, logger *slog.Logger) (*sandbox.K8s, error) {
	cfg := sandbox.K8sConfig{
		Kubeconfig:     opt.k8sKubeconfig,
		Context:        opt.k8sContext,
		Namespace:      opt.k8sNamespace,
		Image:          opt.sandboxImage,
		APIPort:        8080,
		RuntimeClass:   opt.k8sRuntimeClass,
		AllowRunc:      opt.k8sAllowRunc,
		RunAsUser:      opt.k8sRunAsUser,
		AllowRootUser:  opt.k8sAllowRootUser,
		ReadOnlyRootFS: &opt.k8sReadOnlyRootFS,
		WritableDirs:   splitList(opt.k8sWritableDirs),
		StorageClass:   opt.k8sStorageClass,
		DiskSize:       opt.k8sDiskSize,
		ServiceAccount: opt.k8sServiceAccount,
		ClusterDomain:  opt.k8sClusterDomain,
		GraceStop:      opt.k8sGraceStop,
		// The limits are the tenant policy's, in Docker's units; the provider
		// converts them to Kubernetes quantities when it builds the pod. Same
		// resolver as the container providers: one policy, one reader.
		CPUs:      opt.sandboxCPUs,
		Memory:    opt.sandboxMemory,
		LimitsFor: tenants.sandboxLimits,
		APIKey:    opt.sandboxAPIKey,
	}
	if endpoint := strings.TrimSpace(opt.k8sEndpoint); endpoint != "" {
		// A control plane outside the cluster (a laptop, a CI job) needs the exact
		// address a port-forward published. One placeholder, so a template cannot
		// silently point every sandbox at one pod.
		cfg.EndpointFor = func(sandboxID string) (string, error) {
			return strings.ReplaceAll(endpoint, "{sandbox}", sandboxID), nil
		}
	}
	provider, err := sandbox.NewK8s(cfg)
	if err != nil {
		return nil, err
	}
	preflightCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := provider.Preflight(preflightCtx); err != nil {
		return nil, err
	}
	logger.Info("the Kubernetes sandbox provider is ready",
		"namespace", provider.Namespace(),
		"image", opt.sandboxImage,
		"runtime_class", opt.k8sRuntimeClass,
		"storage_class", emptyAs(opt.k8sStorageClass, "the cluster default"),
		"disk", opt.k8sDiskSize,
		"detail", "one pod, one volume and one headless service per (user, project); the workspace is the volume, never mounted here")
	return provider, nil
}
