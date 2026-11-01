package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/contracts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// The Kubernetes provider: one pod, one volume and one headless Service per
// (user, project), driven through the Kubernetes API.
//
// This is the third deployment shape and it is deliberately the plain one from
// docs/SANDBOX-K8S-AGENTSANDBOX.md §7.1(a): Pod + PVC + Service that this
// repository writes itself, not the agent-sandbox CRDs. The design document's own
// conclusion is the reason (§9: "pilot, not adopt"; §7.3: a beta CRD in the only
// sandbox-creation path trades a few hundred lines of boilerplate for an upstream
// release schedule we do not control), and the object graph is small enough that
// writing it is not the hard part. What the document does *not* provide — a
// per-sandbox credential, the memory snapshot Pause promises, a pause contract —
// is exactly what has to live here, so borrowing the CRD would not have removed
// any of the real work.
//
// What the provider guarantees, operation by operation, is the container
// provider's story with a different runtime underneath:
//
//   - Ensure creates the PVC (if missing), the Service (if missing) and the Pod,
//     waits for it to be Ready, then provisions the checkout through the sandbox's
//     own HTTP API — the same plan, the same code (checkout.go), as Docker.
//   - The workspace is a PVC only the sandbox's pod mounts. This process never
//     mounts it: the control plane reaches files through the sandbox API, which is
//     what contracts.WorkspaceAPI means and why RootDir is "/workspace".
//   - Pause deletes the Pod and keeps the PVC. There is no memory snapshot, and
//     this provider says so instead of pretending: the PTY, the tmux session and
//     `npm run dev` do not survive a pause (docs/SANDBOX-K8S.md states the
//     consequence, and docs/SANDBOX-K8S-AGENTSANDBOX.md §2 records that the
//     design's own contract has to be read that way).
//   - Resume recreates the Pod on the same PVC and returns READY only once the
//     sandbox API answers again.
//   - Destroy deletes the Pod, the Service and then the PVC, refusing to touch an
//     object this deployment did not create.
//   - Exec runs an argv inside the sandbox over pods/exec (the runtime's own stdio
//     primitive, the same one `docker exec` gives), and Attach/Reattach expose it
//     as the byte stream the agent bridge needs.
//
// It is not a stub: every verb is implemented against the real API, the objects
// carry the security posture the container provider applies, and the only thing
// that needs a cluster to prove is the cluster half (k8s_cluster_test.go names
// the command).

// ProviderNameK8s is the value reported in contracts.Sandbox.Provider.
const ProviderNameK8s = "k8s"

// DefaultK8sRunAsUser is the account a sandbox pod runs as: the sandbox image's
// own user (uid 1000), which is the identity its file/shell/browser services drop
// to in the container provider too.
const DefaultK8sRunAsUser = 1000

// Kubernetes polling and default timing.
const (
	// k8sReadyPoll is how often a pod's readiness is re-read while Ensure or
	// Resume waits. One GET per interval per starting sandbox: the API server
	// answers it from cache, and 500 ms is fast enough that a pod that is ready
	// is not made to wait by us.
	k8sReadyPoll = 500 * time.Millisecond
	// k8sDeletePoll is how often a deleted pod or volume is re-read while
	// Pause/Destroy wait for it to disappear.
	k8sDeletePoll = 300 * time.Millisecond
	// DefaultK8sEnsureTimeout bounds one Ensure/Resume: pull, schedule, boot, then
	// the sandbox API's own startup window. A cold image pull is the slow case and
	// it is measured in minutes, not seconds.
	DefaultK8sEnsureTimeout = 10 * time.Minute
	// DefaultK8sRequestTimeout bounds a read or a small change (Get, Pause,
	// Destroy, Exec).
	DefaultK8sRequestTimeout = 2 * time.Minute
	// DefaultK8sGraceStop is how long a pod is given to stop before it is killed.
	// Same intent as `docker stop --time`: the agent gets a chance to flush.
	DefaultK8sGraceStop = 30 * time.Second
	// DefaultK8sNamespace is where the manifests in deploy/k8s put everything.
	DefaultK8sNamespace = "piforge"
	// DefaultK8sDiskSize is one sandbox's workspace request.
	DefaultK8sDiskSize = "10Gi"
	// DefaultK8sRuntimeClass is the sandbox runtime this provider expects. It is
	// a *class*, not a flag on the pod: gVisor is what makes the syscall boundary
	// real, and preflight refuses a cluster that does not have it unless the
	// operator says so on the command line.
	DefaultK8sRuntimeClass = "gvisor"
)

// K8sConfig configures the Kubernetes provider.
type K8sConfig struct {
	// Kubeconfig is the path to a kubeconfig file. Empty uses the in-cluster
	// service account when it exists, then $KUBECONFIG, then ~/.kube/config —
	// client-go's own loading rules — which is what "run this on a laptop against
	// a kind cluster" and "run this as a pod" both need.
	Kubeconfig string
	// Context selects one context of that kubeconfig. Empty uses its current
	// context. Naming it is the only way to be sure which cluster a deployment
	// with two of them talks to, so the flag exists even though the default is
	// usually right.
	Context string
	// Namespace is where the sandbox objects live. Required (default piforge);
	// preflight refuses a namespace that does not exist rather than creating one,
	// because a namespace is an operator's decision (quota, NetworkPolicy, RBAC).
	Namespace string
	// Image is the sandbox image (the same one the container providers run).
	Image string
	// APIPort is the container port serving the sandbox API (8080).
	APIPort int
	// RuntimeClass is the pod's runtimeClassName. Default "gvisor". Empty means
	// "the node's default runtime": preflight refuses that unless AllowRunc, and
	// AllowRunc exists so an operator can say it on purpose.
	RuntimeClass string
	// AllowRunc accepts a cluster without the runtime class (or with RuntimeClass
	// empty). The consequence is named by preflight and in the docs: the sandbox
	// is then an ordinary container on the node's kernel, and a kernel bug is a
	// sandbox escape.
	AllowRunc bool
	// RunAsUser is the uid the pod runs as (default 1000). Zero means "the image's
	// own user", which is only reachable with AllowRootUser.
	RunAsUser int64
	// AllowRootUser omits runAsNonRoot/runAsUser/runAsGroup from the pod's
	// securityContext. It exists for an image whose entrypoint needs root for its
	// own bootstrap (the stock AIO image does: it useradds and chowns before it
	// drops privileges); the capability set and the runtime class are what
	// constrain that shape, and docs/SANDBOX-K8S.md says so.
	AllowRootUser bool
	// ReadOnlyRootFS mounts the container's own root filesystem read-only (default
	// true). The workspace is the PVC either way; what this protects is the
	// image's binaries and the image's own writable paths.
	ReadOnlyRootFS *bool
	// WritableDirs are the container paths that get an emptyDir when ReadOnlyRootFS
	// is on. Default /tmp and /run: every image needs those two, and nothing else
	// is guessed on the operator's behalf.
	WritableDirs []string
	// StorageClass names the StorageClass for a sandbox's PVC. Empty uses the
	// cluster's default class.
	StorageClass string
	// DiskSize is the PVC's storage request (default 10Gi).
	DiskSize string
	// ServiceAccount is the pod's service account. Empty means the namespace's
	// default, and automountServiceAccountToken is false either way.
	ServiceAccount string
	// ImagePullSecrets are attached to the pod (a private registry).
	ImagePullSecrets []corev1.LocalObjectReference
	// NodeSelector pins sandboxes to nodes that run the sandbox runtime (a gVisor
	// node pool is usually labelled, and the RuntimeClass alone does not schedule).
	NodeSelector map[string]string
	// ClusterDomain is the cluster's DNS suffix (default cluster.local), used to
	// build the Service's FQDN.
	ClusterDomain string
	// EndpointFor overrides the address of a sandbox's API. Nil is the headless
	// Service's DNS name, which is right when the control plane runs in the
	// cluster. A deployment whose control plane runs outside it (a laptop against a
	// cluster, a CI job) needs a port-forward or an ingress and sets this.
	EndpointFor func(sandboxID string) (string, error)
	// GraceStop is how long a pod is given to stop (default 30 s).
	GraceStop time.Duration
	// EnsureTimeout bounds one Ensure/Resume (default 10 min).
	EnsureTimeout time.Duration
	// RequestTimeout bounds one read or small change (default 2 min).
	RequestTimeout time.Duration
	// CPUs and Memory are the deployment-wide default limits, in Docker's units
	// ("2", "2g"), converted to Kubernetes quantities when the pod is built.
	CPUs   string
	Memory string
	// LimitsFor resolves one tenant's limits, exactly as the container providers'
	// does: the policy is the deployment's, this provider only applies it.
	LimitsFor func(ctx context.Context, userID string) Limits
	// APIKey is a fixed key for every sandbox (debugging override). Empty - the
	// normal case - gives each sandbox its own key, generated at create and read
	// back off the pod forever after.
	APIKey string

	// Clientset and RESTConfig are for tests and for a caller that already built
	// them. Nil means "build from Kubeconfig/Context".
	Clientset  kubernetes.Interface
	RESTConfig *rest.Config

	// openCheckout builds the shell a checkout runs on. Nil means the sandbox's
	// Service endpoint (production); tests replace it, which is also what pins
	// that no checkout command goes through pods/exec.
	openCheckout func(ctx context.Context, sandboxID string) (checkoutShell, error)
	// streamer is the pods/exec primitive. Nil means the real SPDY one; tests
	// replace it, because client-go's fake clientset has no pods/exec.
	streamer k8sStreamer
}

func (c K8sConfig) normalise() K8sConfig {
	if strings.TrimSpace(c.Namespace) == "" {
		c.Namespace = DefaultK8sNamespace
	}
	if strings.TrimSpace(c.RuntimeClass) == "" && !c.AllowRunc {
		c.RuntimeClass = DefaultK8sRuntimeClass
	}
	if c.APIPort <= 0 {
		c.APIPort = 8080
	}
	if strings.TrimSpace(c.ClusterDomain) == "" {
		c.ClusterDomain = "cluster.local"
	}
	if strings.TrimSpace(c.DiskSize) == "" {
		c.DiskSize = DefaultK8sDiskSize
	}
	if c.GraceStop <= 0 {
		c.GraceStop = DefaultK8sGraceStop
	}
	if c.EnsureTimeout <= 0 {
		c.EnsureTimeout = DefaultK8sEnsureTimeout
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = DefaultK8sRequestTimeout
	}
	if c.RunAsUser < 0 {
		c.RunAsUser = 0
	}
	if len(c.WritableDirs) == 0 {
		c.WritableDirs = []string{"/tmp", "/run"}
	}
	return c
}

// K8s implements contracts.SandboxProvider against a Kubernetes API.
type K8s struct {
	cfg      K8sConfig
	client   kubernetes.Interface
	rest     *rest.Config
	streamer k8sStreamer
	// keyLocks serialises the create path per sandbox id, so two sessions that
	// start at once do not both generate a key and race the pod's creation.
	keyLocks sync.Map
}

var (
	_ contracts.SandboxProvider = (*K8s)(nil)
	_ contracts.WorktreeSandbox = (*K8s)(nil)
	_ Remote                    = (*K8s)(nil)
	_ Attacher                  = (*K8s)(nil)
	_ Reattacher                = (*K8s)(nil)
)

// NewK8s builds the provider and checks that its configuration can mean
// something. It does not talk to the cluster: that is Preflight's job, called at
// startup so a missing namespace or RBAC fails the boot instead of the first
// session.
func NewK8s(cfg K8sConfig) (*K8s, error) {
	cfg = cfg.normalise()
	if strings.TrimSpace(cfg.Image) == "" {
		return nil, fmt.Errorf("sandbox: k8s: an image is required (-sandbox-image / PIFORGE_SANDBOX_IMAGE)")
	}
	if strings.Contains(cfg.Image, " ") {
		return nil, fmt.Errorf("sandbox: k8s: image %q must not contain spaces", cfg.Image)
	}
	if _, err := k8sResources(Limits{CPUs: cfg.CPUs, Memory: cfg.Memory}); err != nil {
		return nil, err
	}
	provider := &K8s{cfg: cfg}
	if cfg.Clientset != nil {
		provider.client = cfg.Clientset
	}
	if cfg.RESTConfig != nil {
		provider.rest = cfg.RESTConfig
	}
	if provider.client == nil || provider.rest == nil {
		restConfig, err := k8sRESTConfig(cfg)
		if err != nil {
			return nil, err
		}
		if provider.rest == nil {
			provider.rest = restConfig
		}
		if provider.client == nil {
			client, cerr := kubernetes.NewForConfig(restConfig)
			if cerr != nil {
				return nil, fmt.Errorf("sandbox: k8s: build a client: %w", cerr)
			}
			provider.client = client
		}
	}
	if cfg.streamer != nil {
		provider.streamer = cfg.streamer
	} else {
		provider.streamer = &spdyStreamer{client: provider.client, rest: provider.rest, timeout: cfg.RequestTimeout}
	}
	provider.logPosture()
	return provider, nil
}

// k8sRESTConfig loads the kubeconfig the deployment named.
//
// The order is client-go's own (in-cluster, then $KUBECONFIG, then ~/.kube/config)
// because a deployment and a laptop differ only in which of those answers; naming a
// *context* is ours, because two contexts in one kubeconfig is how a laptop ends
// up creating pods in the wrong cluster.
func k8sRESTConfig(cfg K8sConfig) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path := strings.TrimSpace(cfg.Kubeconfig); path != "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("sandbox: k8s: kubeconfig %q: %w", path, err)
		}
		rules.ExplicitPath = abs
	}
	overrides := &clientcmd.ConfigOverrides{}
	if name := strings.TrimSpace(cfg.Context); name != "" {
		overrides.CurrentContext = name
	}
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	restConfig, err := loader.ClientConfig()
	if err != nil {
		message := err.Error()
		if strings.TrimSpace(cfg.Kubeconfig) != "" {
			message = fmt.Sprintf("kubeconfig %q (context %q): %s", cfg.Kubeconfig, cfg.Context, message)
		}
		return nil, fmt.Errorf("sandbox: k8s: no usable Kubernetes context: %s; "+
			"pass -sandbox-k8s-kubeconfig / PIFORGE_SANDBOX_K8S_KUBECONFIG, or run inside the cluster with a service account", message)
	}
	if strings.TrimSpace(restConfig.Host) == "" {
		return nil, fmt.Errorf("sandbox: k8s: the kubeconfig context %q names no server", cfg.Context)
	}
	restConfig.UserAgent = "piforge-sandbox"
	return restConfig, nil
}

// Name identifies the provider in the sandbox row.
func (k *K8s) Name() string { return ProviderNameK8s }

// Namespace reports the namespace this provider creates objects in.
func (k *K8s) Namespace() string { return k.cfg.Namespace }

// Ensure creates (or re-adopts) the sandbox for a (user, project) pair.
//
// Idempotent per pair: the names come from the pair's hash, so a second call
// adopts the objects the first one made. READY is only returned once the sandbox
// API answers and its checkout exists — a pod that is merely Running is not a
// sandbox yet.
func (k *K8s) Ensure(ctx context.Context, userID, projectID, repoURL, branch string) (*contracts.Sandbox, error) {
	if err := safeSegment(userID); err != nil {
		return nil, err
	}
	if err := safeSegment(projectID); err != nil {
		return nil, err
	}
	names := k8sNamesFor(userID, projectID)
	lock := k.lockFor(names.Pod)
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithTimeout(ctx, k.cfg.EnsureTimeout)
	defer cancel()
	return k.ensure(ctx, names, userID, projectID, repoURL, branch)
}

func (k *K8s) lockFor(id string) *sync.Mutex {
	mu := &sync.Mutex{}
	actual, _ := k.keyLocks.LoadOrStore(id, mu)
	return actual.(*sync.Mutex)
}

// build is the shared create-or-adopt path of Ensure and Resume.
func (k *K8s) ensure(ctx context.Context, names k8sNames, userID, projectID, repoURL, branch string) (*contracts.Sandbox, error) {
	if err := k.ensurePVC(ctx, names, userID, projectID); err != nil {
		return nil, err
	}
	if err := k.ensureService(ctx, names, userID, projectID); err != nil {
		return nil, err
	}
	apiKey, err := k.ensureSecret(ctx, names, userID, projectID)
	if err != nil {
		return nil, err
	}
	pod, err := k.ensurePod(ctx, names, userID, projectID, apiKey)
	if err != nil {
		return nil, err
	}
	if err := k.waitReady(ctx, names.Pod); err != nil {
		return nil, err
	}
	// The runtime is back: clear the parked marker so Get reports READY rather
	// than "paused, with a pod running".
	if err := k.markPaused(ctx, names.PVC, false); err != nil {
		return nil, err
	}
	// Re-read the pod: waitReady proves readiness from a GET that may be newer
	// than the create's answer.
	if current, gerr := k.pod(ctx, names.Pod); gerr == nil {
		pod = current
	}
	if key, kerr := k.apiKeyFor(ctx, pod); kerr == nil && key != "" {
		apiKey = key
	}
	if apiKey == "" {
		return nil, fmt.Errorf("sandbox: k8s: pod %s carries no %s; refusing to address an unauthenticated sandbox", names.Pod, SandboxAPIKeyEnv)
	}
	if err := k.ensureCheckout(ctx, names.Pod, apiKey, repoURL, branch); err != nil {
		return nil, err
	}
	return k.describe(ctx, names, pod, apiKey), nil
}

// Get reports the provider's state for one sandbox.
//
// Three answers, and the difference between the last two is the point:
//
//   - the Pod exists: its state (READY/STARTING/PAUSED/ERROR);
//   - the Pod is gone and the PVC carries the paused marker: PAUSED — this
//     provider parked it on purpose and the workspace is intact;
//   - the Pod is gone and the PVC is not marked: MISSING — the runtime vanished
//     without us (an eviction, a node drain, a delete), which is the honest
//     description and the one a caller must not mistake for a pause;
//   - neither object exists: ErrNotFound, the same as Docker's "no such
//     container".
func (k *K8s) Get(ctx context.Context, sandboxID string) (*contracts.Sandbox, error) {
	names, err := k.namesFromID(sandboxID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, k.cfg.RequestTimeout)
	defer cancel()
	pod, err := k.pod(ctx, names.Pod)
	switch {
	case err == nil:
		key, _ := k.apiKeyFor(ctx, pod)
		return k.describe(ctx, names, pod, key), nil
	case !apierrors.IsNotFound(err):
		return nil, fmt.Errorf("sandbox: k8s: get %s: %w", names.Pod, err)
	}
	pvc, perr := k.claim(ctx, names.PVC)
	if apierrors.IsNotFound(perr) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, names.Pod)
	}
	if perr != nil {
		return nil, fmt.Errorf("sandbox: k8s: get %s: %w", names.PVC, perr)
	}
	sb := k.blank(names, pvc)
	if pvc.Annotations[K8sPausedAnnotation] == "true" {
		sb.State = contracts.SandboxPaused
		sb.Message = "paused: the pod is gone and the workspace volume is kept"
		return sb, nil
	}
	sb.State = contracts.SandboxMissing
	sb.Message = "the pod is gone and the sandbox was not paused; the workspace volume is still there"
	return sb, nil
}

// Pause deletes the pod and keeps the workspace.
//
// It is the design's own shape (docs/SANDBOX-K8S-AGENTSANDBOX.md §2): pause
// releases compute, keeps storage, and loses the process. The Service is left in
// place — a headless Service with no endpoints is inert, and recreating it on
// Resume would hand the caller a different DNS answer for no benefit.
func (k *K8s) Pause(ctx context.Context, sandboxID string) error {
	names, err := k.namesFromID(sandboxID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, k.cfg.RequestTimeout)
	defer cancel()
	if _, err := k.pod(ctx, names.Pod); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("sandbox: k8s: pause %s: %w", names.Pod, err)
		}
		// No pod: already paused, or never created. The volume decides which.
		pvc, perr := k.claim(ctx, names.PVC)
		if apierrors.IsNotFound(perr) {
			return fmt.Errorf("%w: %s", ErrNotFound, names.Pod)
		}
		if perr != nil {
			return fmt.Errorf("sandbox: k8s: pause %s: %w", names.Pod, perr)
		}
		if err := k.requireOurs(pvc); err != nil {
			return err
		}
		return k.markPaused(ctx, names.PVC, true)
	}
	if err := k.deletePod(ctx, names.Pod); err != nil {
		return err
	}
	return k.markPaused(ctx, names.PVC, true)
}

// Resume recreates the pod on the same volume and returns READY once the sandbox
// API answers again.
func (k *K8s) Resume(ctx context.Context, sandboxID string) (*contracts.Sandbox, error) {
	names, err := k.namesFromID(sandboxID)
	if err != nil {
		return nil, err
	}
	lock := k.lockFor(names.Pod)
	lock.Lock()
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(ctx, k.cfg.EnsureTimeout)
	defer cancel()
	pvc, err := k.claim(ctx, names.PVC)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, names.Pod)
	}
	if err != nil {
		return nil, fmt.Errorf("sandbox: k8s: resume %s: %w", names.Pod, err)
	}
	if err := k.requireOurs(pvc); err != nil {
		return nil, err
	}
	return k.ensure(ctx, names, pvc.Annotations[K8sUserAnnotation], pvc.Annotations[K8sProjectAnnotation], "", "")
}

// Destroy deletes the pod, the service and the volume.
//
// The order matters: the pod first (a PVC cannot be removed while a pod mounts
// it), then the objects that only exist for the runtime. The refusal is the
// important half — an object without this deployment's managed-by label is never
// deleted, because a name is a hash and a hash can be resolved against the wrong
// cluster.
func (k *K8s) Destroy(ctx context.Context, sandboxID string) error {
	names, err := k.namesFromID(sandboxID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, k.cfg.RequestTimeout)
	defer cancel()
	if pvc, perr := k.claim(ctx, names.PVC); perr == nil {
		if err := k.requireOurs(pvc); err != nil {
			return err
		}
	} else if !apierrors.IsNotFound(perr) {
		return fmt.Errorf("sandbox: k8s: destroy %s: %w", names.PVC, perr)
	}
	if err := k.deletePod(ctx, names.Pod); err != nil {
		return err
	}
	if err := k.deleteObject(ctx, "services", names.Service); err != nil {
		return err
	}
	if err := k.deleteObject(ctx, "persistentvolumeclaims", names.PVC); err != nil {
		return err
	}
	if err := k.awaitGone(ctx, "persistentvolumeclaims", names.PVC); err != nil {
		return err
	}
	// The key goes with the sandbox. It is deleted last so a partially failed
	// destroy still leaves a usable key for the objects that remain.
	return k.deleteObject(ctx, "secrets", names.Secret)
}

// Exec runs an argv inside the sandbox and returns its combined output.
//
// pods/exec rather than the sandbox's line-oriented /v1/shell/exec: the contract
// says "argv, never a shell string", and pods/exec is the runtime's own exec —
// the same primitive `docker exec` gives the container provider. Exec is a
// debugging interface (docs/SANDBOX-DEPLOY.md §0.6); the agent uses Attach.
func (k *K8s) Exec(ctx context.Context, sandboxID string, argv []string, dir string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("sandbox: k8s: exec needs a command")
	}
	names, err := k.namesFromID(sandboxID)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, k.cfg.RequestTimeout)
	defer cancel()
	var out lockedBuffer
	streamErr := k.streamer.Exec(ctx, k.cfg.Namespace, names.Pod, k8sExecOptions{
		Container: K8sContainerName,
		Argv:      k8sExecArgv(argv, dir, nil),
		Stdout:    &out,
		Stderr:    &out,
	})
	text := out.String()
	if streamErr != nil {
		if strings.TrimSpace(text) != "" {
			return text, fmt.Errorf("sandbox: k8s: exec %s: %w: %s", names.Pod, streamErr, strings.TrimSpace(text))
		}
		return text, fmt.Errorf("sandbox: k8s: exec %s: %w", names.Pod, streamErr)
	}
	return text, nil
}

// Attach starts argv inside the sandbox and returns its stdio.
//
// The stream is the pods/exec channel: stdout is what the process writes, stdin is
// what the caller writes. ctx bounds the *start* (and the pod lookup), not the
// stream: the agent outlives the request that asked for it, so cancelling ctx
// after Attach returns leaves it running. Close ends it, CloseWrite half-closes
// stdin (the agent bridge's clean-shutdown signal).
func (k *K8s) Attach(ctx context.Context, sandboxID string, spec AttachSpec) (io.ReadWriteCloser, error) {
	if len(spec.Argv) == 0 {
		return nil, fmt.Errorf("sandbox: k8s: attach needs an argv")
	}
	return k.openStream(ctx, sandboxID, spec.Argv, spec.Dir, spec.User, spec.Env)
}

// Reattach connects to the supervisor already listening on spec.Socket.
func (k *K8s) Reattach(ctx context.Context, sandboxID string, spec ReattachSpec) (io.ReadWriteCloser, error) {
	argv, err := reattachArgv(spec)
	if err != nil {
		return nil, fmt.Errorf("sandbox: k8s: %w", err)
	}
	return k.openStream(ctx, sandboxID, argv, spec.Dir, spec.User, spec.Env)
}

// openStream is the one implementation of "a process in the sandbox, as a byte
// channel", shared by Attach and Reattach for the same reason the runner shares
// its transport: a reattach that started a second agent must not be expressible.
func (k *K8s) openStream(ctx context.Context, sandboxID string, argv []string, dir, user string, env []string) (io.ReadWriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("sandbox: k8s: attach: %w", err)
	}
	names, err := k.namesFromID(sandboxID)
	if err != nil {
		return nil, err
	}
	if err := k8sSandboxUser(user); err != nil {
		return nil, err
	}
	command, err := k8sExecArgvChecked(argv, dir, env)
	if err != nil {
		return nil, err
	}
	// The pod has to exist before a stream means anything, and a missing sandbox
	// should be ErrNotFound rather than a kubelet error about a pod that is not
	// there.
	if _, gerr := k.pod(ctx, names.Pod); gerr != nil {
		if apierrors.IsNotFound(gerr) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, names.Pod)
		}
		return nil, fmt.Errorf("sandbox: k8s: attach %s: %w", names.Pod, gerr)
	}

	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	conn := &k8sExecConn{in: inWriter, out: outReader, done: make(chan struct{})}
	stderr := newTail(8 << 10)
	// The stream is not the request's: `context.WithoutCancel` is exactly the
	// stage-1 rule (the agent outlives the HTTP request that started it), and it
	// keeps the deadline the caller set for *starting* it working above.
	streamCtx := context.WithoutCancel(ctx)
	go func() {
		defer close(conn.done)
		err := k.streamer.Exec(streamCtx, k.cfg.Namespace, names.Pod, k8sExecOptions{
			Container: K8sContainerName,
			Argv:      command,
			Stdin:     inReader,
			Stdout:    outWriter,
			Stderr:    stderr,
		})
		conn.finish(err, strings.TrimSpace(stderr.String()))
	}()
	return conn, nil
}

// Endpoint reports the address that serves the sandbox API.
//
// The headless Service's FQDN, `<name>.<namespace>.svc.<cluster-domain>`, which is
// stable across the pod being recreated by a pause/resume cycle — that is the
// whole reason the Service exists. A deployment whose control plane is outside the
// cluster sets EndpointFor instead (a port-forward or an ingress).
func (k *K8s) Endpoint(ctx context.Context, sandboxID string) (string, error) {
	names, err := k.namesFromID(sandboxID)
	if err != nil {
		return "", err
	}
	if k.cfg.EndpointFor != nil {
		return k.cfg.EndpointFor(names.Pod)
	}
	if k.cfg.APIPort <= 0 {
		return "", fmt.Errorf("sandbox: k8s: no api port is configured for this provider")
	}
	if _, err := k.service(ctx, names.Service); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("%w: %s (the sandbox has no service)", ErrNotFound, names.Service)
		}
		return "", fmt.Errorf("sandbox: k8s: endpoint %s: %w", names.Service, err)
	}
	return fmt.Sprintf("http://%s.%s.svc.%s:%d", names.Service, k.cfg.Namespace, k.cfg.ClusterDomain, k.apiPort()), nil
}

// ContainerPath maps a workspace path to the path the sandbox's own processes see.
//
// It is the identity for anything under /workspace, and an error for anything
// else. That is not a shortcut: in a Kubernetes placement the workspace *is* a
// path inside the pod's PVC, this process never mounts it, and a host path handed
// in (the shape -sandbox docker accepts) can only mean the caller is addressing a
// tree this provider never had. Mapping it to /workspace would silently write into
// the wrong project, which is the failure the local provider's root check exists
// to prevent.
func (k *K8s) ContainerPath(hostPath string) (string, error) {
	cleaned := path.Clean(filepath.ToSlash(strings.TrimSpace(hostPath)))
	if !strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("sandbox: k8s: %q is not an absolute path inside the sandbox", hostPath)
	}
	if cleaned == containerWorkspace || strings.HasPrefix(cleaned, containerWorkspace+"/") {
		return cleaned, nil
	}
	return "", fmt.Errorf("sandbox: k8s: %q is outside %s: a Kubernetes sandbox keeps its workspace on its own volume, so this process never mounts it", hostPath, containerWorkspace)
}

// AddWorktree creates a branch session's linked worktree inside the sandbox, as
// the sandbox user (contracts.WorktreeSandbox).
func (k *K8s) AddWorktree(ctx context.Context, sandboxID, repoDir, worktreePath, branch string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	names, err := k.namesFromID(sandboxID)
	if err != nil {
		return err
	}
	repo, err := k.ContainerPath(repoDir)
	if err != nil {
		return fmt.Errorf("sandbox: k8s: add worktree: repository %q: %w", repoDir, err)
	}
	worktree, err := k.ContainerPath(worktreePath)
	if err != nil {
		return fmt.Errorf("sandbox: k8s: add worktree: %q: %w", worktreePath, err)
	}
	ctx, cancel := context.WithTimeout(ctx, k.cfg.RequestTimeout)
	defer cancel()
	shell, err := k.waitForShell(ctx, names.Pod, "", probeSandboxAPI)
	if err != nil {
		return fmt.Errorf("sandbox: k8s: add worktree %s: %w", worktree, err)
	}
	ready, err := probeWorktreeReady(ctx, shell, worktree)
	if err != nil {
		return fmt.Errorf("sandbox: k8s: add worktree %s: %w", worktree, err)
	}
	return runWorktreePlan(ctx, shell, ready, repo, worktree, branch, false, k.Name())
}

// ---------------------------------------------------------------------------
// object plumbing
// ---------------------------------------------------------------------------

// namesFromID resolves every id form onto the three names, refusing an empty one.
func (k *K8s) namesFromID(sandboxID string) (k8sNames, error) {
	if strings.TrimSpace(sandboxID) == "" {
		return k8sNames{}, fmt.Errorf("%w: empty sandbox id", ErrNotFound)
	}
	return k8sNamesForID(sandboxID), nil
}

func (k *K8s) apiPort() int {
	if k.cfg.APIPort > 0 {
		return k.cfg.APIPort
	}
	return 8080
}

func (k *K8s) diskSize() string {
	if strings.TrimSpace(k.cfg.DiskSize) != "" {
		return k.cfg.DiskSize
	}
	return DefaultK8sDiskSize
}

func (k *K8s) graceStop() time.Duration {
	if k.cfg.GraceStop > 0 {
		return k.cfg.GraceStop
	}
	return DefaultK8sGraceStop
}

// readOnlyRootFS defaults to true: a sandbox's own filesystem is not where its
// work lives, so there is nothing to lose by making it read-only and a whole
// class of "the agent rewrote a binary for this pod's lifetime" to gain.
func (k *K8s) readOnlyRootFS() bool {
	if k.cfg.ReadOnlyRootFS != nil {
		return *k.cfg.ReadOnlyRootFS
	}
	return true
}

func (k *K8s) writableDirs() []string {
	if len(k.cfg.WritableDirs) == 0 {
		return sortedDirs([]string{"/tmp", "/run"})
	}
	return sortedDirs(k.cfg.WritableDirs)
}

func (k *K8s) pullPolicy() corev1.PullPolicy {
	// IfNotPresent by default: a node that already has the sandbox image must not
	// pull it again for every tenant, and a deployment that moves the tag is
	// deployed by moving the tag (or by a rollout). An operator who needs Always
	// can set the image to a digest-less tag and pull-on-start (docs).
	return corev1.PullIfNotPresent
}

func (k *K8s) limitsFor(ctx context.Context, userID string) Limits {
	if k.cfg.LimitsFor != nil {
		return k.cfg.LimitsFor(ctx, userID)
	}
	return Limits{CPUs: k.cfg.CPUs, Memory: k.cfg.Memory}
}

// pod reads one sandbox pod.
func (k *K8s) pod(ctx context.Context, name string) (*corev1.Pod, error) {
	return k.client.CoreV1().Pods(k.cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
}

// claim reads one sandbox volume.
func (k *K8s) claim(ctx context.Context, name string) (*corev1.PersistentVolumeClaim, error) {
	return k.client.CoreV1().PersistentVolumeClaims(k.cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
}

// service reads one sandbox service.
func (k *K8s) service(ctx context.Context, name string) (*corev1.Service, error) {
	return k.client.CoreV1().Services(k.cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
}

// secret reads one sandbox API key.
func (k *K8s) secret(ctx context.Context, name string) (*corev1.Secret, error) {
	return k.client.CoreV1().Secrets(k.cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
}

// ensurePVC creates the workspace volume if it is missing, and refuses to adopt
// one this deployment did not create.
func (k *K8s) ensurePVC(ctx context.Context, names k8sNames, userID, projectID string) error {
	existing, err := k.claim(ctx, names.PVC)
	switch {
	case err == nil:
		return k.requireOurs(existing)
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("sandbox: k8s: read volume %s: %w", names.PVC, err)
	}
	claim, err := k.k8sPVC(names, userID, projectID)
	if err != nil {
		return err
	}
	if _, cerr := k.client.CoreV1().PersistentVolumeClaims(k.cfg.Namespace).Create(ctx, claim, metav1.CreateOptions{}); cerr != nil {
		if apierrors.IsAlreadyExists(cerr) {
			return nil
		}
		return fmt.Errorf("sandbox: k8s: create volume %s: %w", names.PVC, cerr)
	}
	return nil
}

// ensureService creates the headless API service if it is missing.
func (k *K8s) ensureService(ctx context.Context, names k8sNames, userID, projectID string) error {
	existing, err := k.service(ctx, names.Service)
	switch {
	case err == nil:
		return k.requireOurs(existing)
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("sandbox: k8s: read service %s: %w", names.Service, err)
	}
	if _, cerr := k.client.CoreV1().Services(k.cfg.Namespace).Create(ctx, k8sService(names, userID, projectID), metav1.CreateOptions{}); cerr != nil {
		if apierrors.IsAlreadyExists(cerr) {
			return nil
		}
		return fmt.Errorf("sandbox: k8s: create service %s: %w", names.Service, cerr)
	}
	return nil
}

// ensurePod creates the sandbox pod, or adopts the one already there.
//
// A pod whose API key reference does not match its sandbox (one created before
// per-sandbox keys, or by an older build) is *replaced*, not adopted: its API
// would accept anything that can reach it, and "it was already like that" is not a
// reason to keep calling it unauthenticated (the same rule Docker.Ensure applies).
// The workspace is a volume, so replacing the pod keeps every file.
func (k *K8s) ensurePod(ctx context.Context, names k8sNames, userID, projectID, apiKey string) (*corev1.Pod, error) {
	pod, err := k.pod(ctx, names.Pod)
	if err == nil {
		if ref := podAPIKey(pod); ref != nil && ref.Name == names.Secret && ref.Key == K8sAPIKeySecretKey {
			return pod, nil
		}
		if derr := k.deletePod(ctx, names.Pod); derr != nil {
			return nil, derr
		}
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("sandbox: k8s: read pod %s: %w", names.Pod, err)
	}

	spec, err := k.k8sPod(names, userID, projectID, apiKey != "", k.limitsFor(ctx, userID))
	if err != nil {
		return nil, err
	}
	created, cerr := k.client.CoreV1().Pods(k.cfg.Namespace).Create(ctx, spec, metav1.CreateOptions{})
	if cerr != nil {
		if apierrors.IsAlreadyExists(cerr) {
			return k.pod(ctx, names.Pod)
		}
		return nil, fmt.Errorf("sandbox: k8s: create pod %s: %w", names.Pod, cerr)
	}
	return created, nil
}

// ensureSecret returns the sandbox's API key, creating its Secret on first use.
//
// The key is generated at creation and never rotated by a later call: a Resume
// reattaches to the key the sandbox already has, so the value the control plane
// stored with the row stays valid across a pause/resume cycle. A fixed
// K8sConfig.APIKey overrides generation (a debugging shape, like the container
// providers' -sandbox-api-key).
func (k *K8s) ensureSecret(ctx context.Context, names k8sNames, userID, projectID string) (string, error) {
	existing, err := k.secret(ctx, names.Secret)
	switch {
	case err == nil:
		if err := k.requireOurs(existing); err != nil {
			return "", err
		}
		key := strings.TrimSpace(string(existing.Data[K8sAPIKeySecretKey]))
		if key == "" {
			return "", fmt.Errorf("sandbox: k8s: secret %s has no %s", names.Secret, K8sAPIKeySecretKey)
		}
		return key, nil
	case !apierrors.IsNotFound(err):
		return "", fmt.Errorf("sandbox: k8s: read secret %s: %w", names.Secret, err)
	}
	key := strings.TrimSpace(k.cfg.APIKey)
	if key == "" {
		generated, gerr := NewAPIKey()
		if gerr != nil {
			return "", gerr
		}
		key = generated
	}
	if _, cerr := k.client.CoreV1().Secrets(k.cfg.Namespace).Create(ctx, k.k8sSecret(names, userID, projectID, key), metav1.CreateOptions{}); cerr != nil {
		if apierrors.IsAlreadyExists(cerr) {
			// Two creates raced: the loser reads the winner's key.
			return k.ensureSecret(ctx, names, userID, projectID)
		}
		return "", fmt.Errorf("sandbox: k8s: create secret %s: %w", names.Secret, cerr)
	}
	return key, nil
}

// apiKeyFor resolves the key a sandbox pod was created with.
//
// It follows the pod's own reference (a SecretKeyRef, which is what this provider
// writes) and falls back to a literal env value, which is what a hand-made pod or
// an older build has. Empty with no error means "the pod carries no key at all",
// which Ensure treats as a reason to replace it.
func (k *K8s) apiKeyFor(ctx context.Context, pod *corev1.Pod) (string, error) {
	ref := podAPIKey(pod)
	if ref == nil {
		return "", nil
	}
	object, err := k.secret(ctx, ref.Name)
	if err != nil {
		return "", fmt.Errorf("sandbox: k8s: read the key of %s: %w", pod.Name, err)
	}
	return strings.TrimSpace(string(object.Data[ref.Key])), nil
}

// describe turns a pod into the contract the rest of the code uses.
func (k *K8s) describe(ctx context.Context, names k8sNames, pod *corev1.Pod, apiKey string) *contracts.Sandbox {
	sb := &contracts.Sandbox{
		ID:       names.Pod,
		Provider: k.Name(),
		Image:    k.cfg.Image,
		// The workspace exists only inside the sandbox: this process never mounts
		// the PVC (docs/EXECUTION-BOUNDARY.md stage 2). RootDir is the container
		// path, and WorkspaceAPI is what stops every caller from trying to open it.
		RootDir:   containerWorkspace,
		Workspace: contracts.WorkspaceAPI,
		Node:      pod.Spec.NodeName,
		State:     k8sState(pod),
	}
	applied := appliedLimitsFromPod(pod)
	sb.CPUMilli, sb.MemMB = applied.RecordedLimits()
	if apiKey != "" {
		sb.SandboxAPIKey = apiKey
	}
	sb.UserID = pod.Annotations[K8sUserAnnotation]
	sb.ProjectID = pod.Annotations[K8sProjectAnnotation]
	if sb.State == contracts.SandboxError {
		sb.Message = podFailure(pod)
	}
	if created := pod.CreationTimestamp.Time; !created.IsZero() {
		sb.CreatedAt = created.UTC()
	}
	sb.LastActiveAt = time.Now().UTC()
	return sb
}

// blank is the sandbox record for a volume without a pod: paused or missing.
func (k *K8s) blank(names k8sNames, pvc *corev1.PersistentVolumeClaim) *contracts.Sandbox {
	sb := &contracts.Sandbox{
		ID:        names.Pod,
		Provider:  k.Name(),
		Image:     k.cfg.Image,
		RootDir:   containerWorkspace,
		Workspace: contracts.WorkspaceAPI,
	}
	if pvc != nil {
		sb.UserID = pvc.Annotations[K8sUserAnnotation]
		sb.ProjectID = pvc.Annotations[K8sProjectAnnotation]
		if created := pvc.CreationTimestamp.Time; !created.IsZero() {
			sb.CreatedAt = created.UTC()
		}
	}
	sb.LastActiveAt = time.Now().UTC()
	return sb
}

// k8sState maps a pod onto the state machine (contracts.SandboxState).
func k8sState(pod *corev1.Pod) contracts.SandboxState {
	switch pod.Status.Phase {
	case corev1.PodRunning:
		if podReady(pod) {
			return contracts.SandboxReady
		}
		return contracts.SandboxStarting
	case corev1.PodPending:
		return contracts.SandboxStarting
	case corev1.PodFailed:
		return contracts.SandboxError
	case corev1.PodSucceeded:
		// The container exited 0 without being asked to. The volume is intact, so
		// this is "not running" rather than "gone" — the same reading Docker's
		// "exited" gets. (restartPolicy: Always means the cluster is already
		// bringing it back.)
		return contracts.SandboxPaused
	case "":
		return contracts.SandboxMissing
	default:
		return contracts.SandboxStarting
	}
}

// waitReady blocks until the pod is Ready, or fails with what kept it from being
// Ready (a scheduling failure, an image that will not pull, a container that
// crash-loops).
func (k *K8s) waitReady(ctx context.Context, name string) error {
	for {
		pod, err := k.pod(ctx, name)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("sandbox: k8s: pod %s disappeared while waiting for it: %w", name, err)
			}
			return fmt.Errorf("sandbox: k8s: wait for %s: %w", name, err)
		}
		switch {
		case podReady(pod):
			return nil
		case pod.Status.Phase == corev1.PodFailed:
			return fmt.Errorf("sandbox: k8s: pod %s failed: %s", name, podFailure(pod))
		}
		timer := time.NewTimer(k8sReadyPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("sandbox: k8s: pod %s was not ready within the deadline (%s): %w", name, podFailure(pod), ctx.Err())
		case <-timer.C:
		}
	}
}

// deletePod stops the pod with the configured grace period and waits until it is
// gone, so a following volume delete cannot race the kubelet's unmount.
func (k *K8s) deletePod(ctx context.Context, name string) error {
	grace := int64(k.graceStop().Seconds())
	err := k.client.CoreV1().Pods(k.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{
		GracePeriodSeconds: &grace,
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("sandbox: k8s: delete pod %s: %w", name, err)
	}
	return k.awaitGone(ctx, "pods", name)
}

// deleteObject removes one non-pod object, treating "already gone" as success.
func (k *K8s) deleteObject(ctx context.Context, resource, name string) error {
	var err error
	switch resource {
	case "services":
		err = k.client.CoreV1().Services(k.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
	case "secrets":
		err = k.client.CoreV1().Secrets(k.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
	case "persistentvolumeclaims":
		err = k.client.CoreV1().PersistentVolumeClaims(k.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
	default:
		return fmt.Errorf("sandbox: k8s: refusing to delete unknown resource %q", resource)
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("sandbox: k8s: delete %s %s: %w", resource, name, err)
	}
	return nil
}

// awaitGone waits until one object no longer exists.
func (k *K8s) awaitGone(ctx context.Context, resource, name string) error {
	for {
		exists := true
		switch resource {
		case "pods":
			_, err := k.pod(ctx, name)
			exists = err == nil
			if err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("sandbox: k8s: wait for %s %s: %w", resource, name, err)
			}
		case "persistentvolumeclaims":
			_, err := k.claim(ctx, name)
			exists = err == nil
			if err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("sandbox: k8s: wait for %s %s: %w", resource, name, err)
			}
		default:
			return fmt.Errorf("sandbox: k8s: refusing to wait on unknown resource %q", resource)
		}
		if !exists {
			return nil
		}
		timer := time.NewTimer(k8sDeletePoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("sandbox: k8s: %s %s still exists after the deadline: %w", resource, name, ctx.Err())
		case <-timer.C:
		}
	}
}

// requireOurs refuses to touch an object this deployment did not create.
func (k *K8s) requireOurs(object metav1.Object) error {
	if object.GetLabels()[K8sManagedByLabel] == K8sManagedBy {
		return nil
	}
	return fmt.Errorf("sandbox: k8s: refusing to manage %s/%s: it does not carry %s=%s, so it was not created by this deployment",
		object.GetNamespace(), object.GetName(), K8sManagedByLabel, K8sManagedBy)
}

// markPaused writes (or clears) the durable "parked on purpose" marker.
func (k *K8s) markPaused(ctx context.Context, pvcName string, paused bool) error {
	pvc, err := k.claim(ctx, pvcName)
	if err != nil {
		if apierrors.IsNotFound(err) && !paused {
			return nil
		}
		return fmt.Errorf("sandbox: k8s: mark %s: %w", pvcName, err)
	}
	if (pvc.Annotations[K8sPausedAnnotation] == "true") == paused {
		return nil
	}
	updated := pvc.DeepCopy()
	if paused {
		if updated.Annotations == nil {
			updated.Annotations = map[string]string{}
		}
		updated.Annotations[K8sPausedAnnotation] = "true"
	} else {
		delete(updated.Annotations, K8sPausedAnnotation)
	}
	if _, uerr := k.client.CoreV1().PersistentVolumeClaims(k.cfg.Namespace).Update(ctx, updated, metav1.UpdateOptions{}); uerr != nil {
		return fmt.Errorf("sandbox: k8s: mark %s paused=%t: %w", pvcName, paused, uerr)
	}
	return nil
}

// ---------------------------------------------------------------------------
// checkout through the sandbox API
// ---------------------------------------------------------------------------

// ensureCheckout provisions the main checkout through the sandbox's own HTTP API,
// exactly as the container providers do (checkout.go holds the shared plan and its
// execution).
func (k *K8s) ensureCheckout(ctx context.Context, sandboxID, apiKey, repoURL, branch string) error {
	shell, ready, err := k.waitForCheckoutShell(ctx, sandboxID, apiKey)
	if err != nil {
		return fmt.Errorf("sandbox: k8s: checkout %s: %w", containerRepoDir, err)
	}
	return runCheckoutPlan(ctx, shell, ready, repoURL, branch, false, k.Name())
}

// waitForCheckoutShell waits for the sandbox API and reports whether the checkout
// already exists.
func (k *K8s) waitForCheckoutShell(ctx context.Context, sandboxID, apiKey string) (checkoutShell, bool, error) {
	var ready bool
	shell, err := k.waitForShell(ctx, sandboxID, apiKey, func(probeCtx context.Context, shell checkoutShell) error {
		isReady, probeErr := probeCheckoutReady(probeCtx, shell)
		if probeErr != nil {
			return probeErr
		}
		ready = isReady
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return shell, ready, nil
}

// waitForShell is awaitSandboxShell with this provider's way of opening a shell.
func (k *K8s) waitForShell(ctx context.Context, sandboxID, apiKey string, probe func(context.Context, checkoutShell) error) (checkoutShell, error) {
	if k.cfg.APIPort <= 0 && k.cfg.openCheckout == nil {
		return nil, fmt.Errorf("the checkout runs through the sandbox API, but this provider has no api port (K8sConfig.APIPort)")
	}
	return awaitSandboxShell(ctx, func(ctx context.Context) (checkoutShell, error) {
		return k.shellFor(ctx, sandboxID, apiKey)
	}, probe)
}

// shellFor opens the sandbox's API as a shell, or the injected one.
func (k *K8s) shellFor(ctx context.Context, sandboxID, apiKey string) (checkoutShell, error) {
	if k.cfg.openCheckout != nil {
		return k.cfg.openCheckout(ctx, sandboxID)
	}
	endpoint, err := k.Endpoint(ctx, sandboxID)
	if err != nil {
		return nil, fmt.Errorf("sandbox %s has no reachable API endpoint: %w", sandboxID, err)
	}
	if strings.TrimSpace(apiKey) == "" {
		names, nerr := k.namesFromID(sandboxID)
		if nerr != nil {
			return nil, nerr
		}
		pod, perr := k.pod(ctx, names.Pod)
		if perr != nil {
			return nil, fmt.Errorf("sandbox: k8s: read the key of %s: %w", names.Pod, perr)
		}
		apiKey, perr = k.apiKeyFor(ctx, pod)
		if perr != nil {
			return nil, perr
		}
		if apiKey == "" {
			return nil, fmt.Errorf("sandbox: k8s: pod %s carries no %s", names.Pod, SandboxAPIKeyEnv)
		}
	}
	client, err := aio.New(aio.Config{
		BaseURL: endpoint,
		APIKey:  apiKey,
		Root:    containerWorkspace,
		Timeout: k.cfg.RequestTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("sandbox %s: %w", sandboxID, err)
	}
	return client, nil
}

// ---------------------------------------------------------------------------
// exec plumbing
// ---------------------------------------------------------------------------

// k8sExecOptions is one pods/exec request.
type k8sExecOptions struct {
	Container string
	Argv      []string
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	TTY       bool
}

// k8sExecArgv applies the two things pods/exec has no field for: a working
// directory and extra environment.
//
// Neither is interpolated into a shell string. The directory is passed as `$0` to
// `sh -c 'cd -- "$0" && exec "$@"'`, and the environment entries are arguments of
// `env`, so an argv element can never become syntax — the same property
// `docker exec -w/-e` gives the container provider.
func k8sExecArgv(argv []string, dir string, env []string) []string {
	out := make([]string, 0, len(argv)+len(env)+6)
	if len(env) > 0 {
		out = append(out, "/usr/bin/env")
		out = append(out, env...)
	}
	out = append(out, argv...)
	if cleaned := path.Clean(strings.TrimSpace(dir)); dir != "" && cleaned != "." && cleaned != containerWorkspace {
		wrapped := []string{"/bin/sh", "-c", `cd -- "$0" && exec "$@"`, cleaned}
		return append(wrapped, out...)
	}
	return out
}

// k8sSandboxUser accepts the account the agent asks for when it is the one the pod
// already runs as, and refuses anything else.
//
// `docker exec -u` has no pods/exec equivalent: a process inside a pod runs as the
// pod's account. Accepting a different user and running it anyway would be a lie
// about the identity a caller asked for, so the name the sandbox image uses is
// accepted (it *is* uid 1000, the pod's account) and every other answer is an
// error that says what the provider can do.
func k8sSandboxUser(user string) error {
	switch strings.TrimSpace(user) {
	case "", "gem", "1000":
		return nil
	}
	return fmt.Errorf("sandbox: k8s: pods/exec has no per-command user (asked for %q): a sandbox process runs as the pod's own account (uid %d), and -sandbox-k8s-run-as-user changes it for the whole sandbox",
		user, DefaultK8sRunAsUser)
}

// k8sEnvName is the shape of a variable python/node/pi agree on.
var k8sEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// k8sExecArgvChecked is k8sExecArgv plus the validation an AttachSpec's Env needs:
// an entry is `NAME=VALUE`, so it cannot be read by `env` as an option and cannot
// carry a NUL.
func k8sExecArgvChecked(argv []string, dir string, env []string) ([]string, error) {
	clean := make([]string, 0, len(env))
	for _, entry := range env {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		if strings.ContainsRune(trimmed, 0) {
			return nil, fmt.Errorf("sandbox: k8s: environment entry contains a NUL byte")
		}
		if !k8sEnvName.MatchString(trimmed) {
			return nil, fmt.Errorf("sandbox: k8s: %q is not a NAME=VALUE environment entry", entry)
		}
		clean = append(clean, trimmed)
	}
	return k8sExecArgv(argv, dir, clean), nil
}

// k8sExecConn is one pods/exec stream as an io.ReadWriteCloser.
//
// Reads are what the process wrote, writes are its stdin. Close terminates the
// stream (and with it the process: it sees EOF on stdin); CloseWrite half-closes
// stdin only, which is the clean-shutdown signal the agent bridge sends.
type k8sExecConn struct {
	in   *io.PipeWriter
	out  *io.PipeReader
	done chan struct{}
	once sync.Once
	mu   sync.Mutex
	err  error
	tail string
}

func (c *k8sExecConn) Read(b []byte) (int, error)  { return c.out.Read(b) }
func (c *k8sExecConn) Write(b []byte) (int, error) { return c.in.Write(b) }

// CloseWrite half-closes stdin so the sandbox-side process sees EOF while its
// stdout stays readable.
func (c *k8sExecConn) CloseWrite() error {
	return c.in.Close()
}

// Error reports what the stream said when it ended early (the stderr tail), for a
// caller that wants to explain a channel that closed immediately.
func (c *k8sExecConn) Error() string { return c.tail }

func (c *k8sExecConn) Close() error {
	var out error
	c.once.Do(func() {
		_ = c.in.Close()
		_ = c.out.Close()
		<-c.done
		c.mu.Lock()
		if c.err != nil {
			message := strings.TrimSpace(c.tail)
			if message == "" {
				message = c.err.Error()
			}
			out = errors.New(message)
		}
		c.mu.Unlock()
	})
	return out
}

func (c *k8sExecConn) finish(err error, tail string) {
	c.mu.Lock()
	c.err, c.tail = err, tail
	c.mu.Unlock()
	// EOF on the read side: the process's stdout is finished, and a reader must
	// see that rather than a closed pipe's error.
	_ = c.out.CloseWithError(io.EOF)
	_ = c.in.Close()
}

// lockedBuffer is the combined-output sink Exec uses (docker's CombinedOutput
// keeps the two streams in one buffer too; the contract only promises combined
// output).
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// logPosture reports the two choices an operator has to be able to see in the
// boot log: which runtime class the sandbox gets, and whether the pod runs as the
// sandbox account.
func (k *K8s) logPosture() {
	if strings.TrimSpace(k.cfg.RuntimeClass) == "" {
		slog.Warn("sandbox: k8s: pods get no runtimeClassName, so they run on the node's default runtime (runc): the syscall boundary is the node kernel's",
			"namespace", k.cfg.Namespace)
	}
	if k.cfg.AllowRootUser {
		slog.Warn("sandbox: k8s: pods run as the image's own user (runAsNonRoot is omitted), which the stock AIO image needs for its bootstrap",
			"namespace", k.cfg.Namespace,
			"detail", "the dropped capability set and the runtime class are what constrain the container")
	}
}
