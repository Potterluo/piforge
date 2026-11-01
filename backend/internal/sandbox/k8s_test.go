package sandbox

// What a fake clientset proves about the Kubernetes provider, and what it does
// not.
//
// Every object and every lifecycle verb below is exercised against
// k8s.io/client-go/kubernetes/fake: Ensure, Get, Pause, Resume, Destroy, Endpoint,
// ContainerPath, AddWorktree and Preflight run the real code paths and the real
// request shapes, so the tests pin the object graph (labels, securityContext,
// runtime class, volumes, Secret reference), the state machine, and the refusal
// rules.
//
// Two seams are replaced, and the reason is stated where they are used:
//
//   - pods/exec is not a JSON request but a hijacked SPDY stream, and the fake
//     clientset has no transport for it (k8s_stream.go). A recorder stands in, so
//     Exec's argv/cwd/env and Attach's duplex wiring are asserted exactly; what it
//     cannot prove is the transport, which k8s_cluster_test.go does.
//   - the sandbox's HTTP API (the checkout) is the same seam the Docker provider
//     already exposes (openCheckout), for the same reason: those commands go to the
//     image's /v1/shell/exec, not to Kubernetes.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// recordedExec is one pods/exec call the provider made.
type recordedExec struct {
	Namespace string
	Pod       string
	Container string
	Argv      []string
	HasStdin  bool
	HasStdout bool
	HasStderr bool
	// stdin is what the provider wrote to the stream, captured by the fake when it
	// reads it.
	stdin string
}

// fakeStreamer stands in for pods/exec. It records argv and the stream wiring,
// answers with the configured bytes, and can echo stdin back (the duplex shape
// Attach needs).
type fakeStreamer struct {
	mu    sync.Mutex
	calls []recordedExec

	stdout string
	stderr string
	err    error
	echo   bool
}

func newFakeStreamer() *fakeStreamer { return &fakeStreamer{} }

func (f *fakeStreamer) Exec(ctx context.Context, namespace, pod string, opts k8sExecOptions) error {
	call := recordedExec{
		Namespace: namespace,
		Pod:       pod,
		Container: opts.Container,
		Argv:      append([]string{}, opts.Argv...),
		HasStdin:  opts.Stdin != nil,
		HasStdout: opts.Stdout != nil,
		HasStderr: opts.Stderr != nil,
	}
	if opts.Stdin != nil {
		// A stream that echoes is a stream that stays open until stdin closes:
		// that is the duplex shape Attach promises, and the fake has to reproduce
		// it or the test would only prove a batch job.
		buf := make([]byte, 0, 256)
		chunk := make([]byte, 256)
		for {
			n, err := opts.Stdin.Read(chunk)
			if n > 0 {
				buf = append(buf, chunk[:n]...)
				if f.echo && opts.Stdout != nil {
					_, _ = opts.Stdout.Write(chunk[:n])
				}
			}
			if err != nil {
				break
			}
		}
		call.stdin = string(buf)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if opts.Stdout != nil && f.stdout != "" {
		_, _ = opts.Stdout.Write([]byte(f.stdout))
	}
	if opts.Stderr != nil && f.stderr != "" {
		_, _ = opts.Stderr.Write([]byte(f.stderr))
	}
	return f.err
}

func (f *fakeStreamer) callsSnapshot() []recordedExec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedExec{}, f.calls...)
}

// testK8sEnv is everything a test needs to drive the provider.
type testK8sEnv struct {
	provider *K8s
	client   *fake.Clientset
	shell    *fakeCheckoutShell
	streamer *fakeStreamer
}

// newTestK8s builds the provider over a fake clientset.
//
// The configuration is the one a deployment ships: gVisor, a non-root sandbox
// account, a read-only root filesystem, a storage class and a per-tenant limit,
// so the assertions below are about the objects an operator would actually get.
func newTestK8s(t *testing.T, objects ...runtime.Object) *testK8sEnv {
	t.Helper()
	client := fake.NewClientset(objects...)
	// Every GET of a pod reports it Running and Ready: the readiness wait is a
	// poll, and a test that had to advance it by hand would be testing the poller
	// rather than the provider.
	client.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		get := action.(k8stesting.GetAction)
		object, err := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("pods"), get.GetNamespace(), get.GetName())
		if err != nil {
			return false, nil, nil
		}
		pod, ok := object.(*corev1.Pod)
		if !ok {
			return false, nil, nil
		}
		updated := pod.DeepCopy()
		if updated.Status.Phase == "" {
			updated.Status.Phase = corev1.PodRunning
			updated.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
			_ = client.Tracker().Update(corev1.SchemeGroupVersion.WithResource("pods"), updated, get.GetNamespace())
		}
		return false, nil, nil
	})
	shell := &fakeCheckoutShell{}
	streamer := newFakeStreamer()
	provider := &K8s{
		cfg: K8sConfig{
			Namespace:      "piforge",
			Image:          "piforge/sandbox-agent:test",
			APIPort:        8080,
			RuntimeClass:   "gvisor",
			RunAsUser:      1000,
			StorageClass:   "standard",
			DiskSize:       "5Gi",
			ClusterDomain:  "cluster.local",
			GraceStop:      time.Second,
			EnsureTimeout:  5 * time.Second,
			RequestTimeout: 5 * time.Second,
			CPUs:           "2",
			Memory:         "2g",
			openCheckout: func(ctx context.Context, sandboxID string) (checkoutShell, error) {
				return shell, nil
			},
		},
		client:   client,
		streamer: streamer,
		keyLocks: sync.Map{},
	}
	return &testK8sEnv{provider: provider, client: client, shell: shell, streamer: streamer}
}

// podOf reads one pod out of the fake store.
func (e *testK8sEnv) podOf(t *testing.T, name string) *corev1.Pod {
	t.Helper()
	pod, err := e.client.CoreV1().Pods("piforge").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read pod %s: %v", name, err)
	}
	return pod
}

func (e *testK8sEnv) pvcOf(t *testing.T, name string) *corev1.PersistentVolumeClaim {
	t.Helper()
	pvc, err := e.client.CoreV1().PersistentVolumeClaims("piforge").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read pvc %s: %v", name, err)
	}
	return pvc
}

func (e *testK8sEnv) secretOf(t *testing.T, name string) *corev1.Secret {
	t.Helper()
	secret, err := e.client.CoreV1().Secrets("piforge").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read secret %s: %v", name, err)
	}
	return secret
}

func (e *testK8sEnv) serviceOf(t *testing.T, name string) *corev1.Service {
	t.Helper()
	service, err := e.client.CoreV1().Services("piforge").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read service %s: %v", name, err)
	}
	return service
}

func (e *testK8sEnv) absent(t *testing.T, resource, name string) {
	t.Helper()
	var err error
	switch resource {
	case "pods":
		_, err = e.client.CoreV1().Pods("piforge").Get(context.Background(), name, metav1.GetOptions{})
	case "persistentvolumeclaims":
		_, err = e.client.CoreV1().PersistentVolumeClaims("piforge").Get(context.Background(), name, metav1.GetOptions{})
	case "services":
		_, err = e.client.CoreV1().Services("piforge").Get(context.Background(), name, metav1.GetOptions{})
	case "secrets":
		_, err = e.client.CoreV1().Secrets("piforge").Get(context.Background(), name, metav1.GetOptions{})
	default:
		t.Fatalf("unknown resource %q", resource)
	}
	if err == nil {
		t.Fatalf("%s %s still exists", resource, name)
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("%s %s: %v", resource, name, err)
	}
}

func (e *testK8sEnv) count(t *testing.T, resource string) int {
	t.Helper()
	var list runtime.Object
	var err error
	switch resource {
	case "pods":
		pods, lerr := e.client.CoreV1().Pods("piforge").List(context.Background(), metav1.ListOptions{})
		list, err = pods, lerr
	case "persistentvolumeclaims":
		pvcs, lerr := e.client.CoreV1().PersistentVolumeClaims("piforge").List(context.Background(), metav1.ListOptions{})
		list, err = pvcs, lerr
	case "services":
		services, lerr := e.client.CoreV1().Services("piforge").List(context.Background(), metav1.ListOptions{})
		list, err = services, lerr
	case "secrets":
		secrets, lerr := e.client.CoreV1().Secrets("piforge").List(context.Background(), metav1.ListOptions{})
		list, err = secrets, lerr
	}
	if err != nil {
		t.Fatalf("list %s: %v", resource, err)
	}
	switch typed := list.(type) {
	case *corev1.PodList:
		return len(typed.Items)
	case *corev1.PersistentVolumeClaimList:
		return len(typed.Items)
	case *corev1.ServiceList:
		return len(typed.Items)
	case *corev1.SecretList:
		return len(typed.Items)
	}
	t.Fatalf("unknown list for %q", resource)
	return 0
}

// allowAccess installs the RBAC answer a correctly configured cluster gives.
func allowAccess(client *fake.Clientset) {
	client.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, &authorizationv1.SelfSubjectAccessReview{
			Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true},
		}, nil
	})
}

// gvisorCluster is the cluster a deployment is supposed to run against: the
// namespace, the sandbox runtime class, and the RBAC.
func gvisorCluster() []runtime.Object {
	return []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "piforge"}},
		&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "gvisor"}, Handler: "runsc"},
	}
}

// ---------------------------------------------------------------------------
// Ensure: the object graph
// ---------------------------------------------------------------------------

// TestK8sEnsureCreatesTheSandbox is the central test: it asserts the whole object
// graph an operator would see, because the graph *is* the security boundary —
// labels for the tenant, a runtime class for the syscall boundary, a
// securityContext for the account, a PVC that only this pod mounts, and a Service
// for the name the control plane dials.
func TestK8sEnsureCreatesTheSandbox(t *testing.T) {
	env := newTestK8s(t)
	const userID, projectID = "6f1e6a3c-8f7d-4b1e-9a2c-1d2e3f4a5b6c", "0b7c2d11-1111-4222-8333-944455556666"
	names := k8sNamesFor(userID, projectID)

	sb, err := env.provider.Ensure(context.Background(), userID, projectID, "", "main")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// --- the contract the rest of the product reads -------------------------
	if sb.ID != names.Pod {
		t.Fatalf("sandbox id = %q, want the pod name %q", sb.ID, names.Pod)
	}
	if sb.Provider != ProviderNameK8s {
		t.Fatalf("provider = %q, want %q", sb.Provider, ProviderNameK8s)
	}
	if sb.State != contracts.SandboxReady {
		t.Fatalf("state = %q, want READY", sb.State)
	}
	if sb.RootDir != "/workspace" {
		t.Fatalf("RootDir = %q, want the container path /workspace", sb.RootDir)
	}
	// The load-bearing one: this process never mounts the PVC, so every caller
	// must go through the sandbox API. A record that said "host" would make the
	// file panel open a path that does not exist here.
	if sb.Workspace != contracts.WorkspaceAPI {
		t.Fatalf("Workspace = %q, want api", sb.Workspace)
	}
	if sb.SandboxAPIKey == "" || len(sb.SandboxAPIKey) != 64 {
		t.Fatalf("SandboxAPIKey = %q, want a generated 64-hex key", sb.SandboxAPIKey)
	}
	if sb.UserID != userID || sb.ProjectID != projectID {
		t.Fatalf("sandbox ids = %q/%q, want %q/%q", sb.UserID, sb.ProjectID, userID, projectID)
	}
	if sb.CPUMilli != 2000 || sb.MemMB != 2048 {
		t.Fatalf("recorded limits = %d milli / %d MiB, want 2000 / 2048", sb.CPUMilli, sb.MemMB)
	}

	// --- the volume ---------------------------------------------------------
	pvc := env.pvcOf(t, names.PVC)
	if got := pvc.Labels[K8sManagedByLabel]; got != K8sManagedBy {
		t.Fatalf("pvc managed-by label = %q", got)
	}
	if got := pvc.Labels[K8sSandboxLabel]; got != names.Hash {
		t.Fatalf("pvc sandbox label = %q, want %q", got, names.Hash)
	}
	if got := pvc.Labels[K8sUserLabel]; got != K8sLabelValue(userID) {
		t.Fatalf("pvc user label = %q", got)
	}
	if got := pvc.Annotations[K8sUserAnnotation]; got != userID {
		t.Fatalf("pvc user annotation = %q, want the verbatim id", got)
	}
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Fatalf("pvc access modes = %v, want ReadWriteOnce", pvc.Spec.AccessModes)
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "standard" {
		t.Fatalf("pvc storage class = %v", pvc.Spec.StorageClassName)
	}
	if got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; got.String() != "5Gi" {
		t.Fatalf("pvc size = %s, want 5Gi", got.String())
	}
	// No owner reference: Destroy is the only path that removes a workspace, and a
	// garbage-collection edge from the pod would make Pause delete it.
	if len(pvc.OwnerReferences) != 0 {
		t.Fatalf("pvc has owner references %v; a cascade must not be able to delete user data", pvc.OwnerReferences)
	}

	// --- the service --------------------------------------------------------
	service := env.serviceOf(t, names.Service)
	if service.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Fatalf("service clusterIP = %q, want a headless None", service.Spec.ClusterIP)
	}
	if got := service.Spec.Selector[K8sSandboxLabel]; got != names.Hash {
		t.Fatalf("service selector = %v, want the sandbox hash", service.Spec.Selector)
	}
	if len(service.Spec.Ports) != 1 || service.Spec.Ports[0].Port != 8080 || service.Spec.Ports[0].Name != K8sAPIPortName {
		t.Fatalf("service ports = %+v", service.Spec.Ports)
	}

	// --- the secret ---------------------------------------------------------
	secret := env.secretOf(t, names.Secret)
	if got := string(secret.Data[K8sAPIKeySecretKey]); got != sb.SandboxAPIKey {
		t.Fatalf("secret key = %q, want the key the sandbox reported", got)
	}
	if secret.Type != corev1.SecretTypeOpaque {
		t.Fatalf("secret type = %q", secret.Type)
	}

	// --- the pod ------------------------------------------------------------
	pod := env.podOf(t, names.Pod)
	if pod.Labels[K8sSandboxLabel] != names.Hash || pod.Labels[K8sUserLabel] != K8sLabelValue(userID) ||
		pod.Labels[K8sProjectLabel] != K8sLabelValue(projectID) || pod.Labels[K8sManagedByLabel] != K8sManagedBy {
		t.Fatalf("pod labels = %v", pod.Labels)
	}
	if pod.Annotations[K8sUserAnnotation] != userID || pod.Annotations[K8sProjectAnnotation] != projectID {
		t.Fatalf("pod annotations = %v", pod.Annotations)
	}
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != "gvisor" {
		t.Fatalf("runtimeClassName = %v, want gvisor", pod.Spec.RuntimeClassName)
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyAlways {
		t.Fatalf("restartPolicy = %q", pod.Spec.RestartPolicy)
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatal("the sandbox pod may talk to the Kubernetes API; automountServiceAccountToken must be false")
	}
	if pod.Spec.EnableServiceLinks == nil || *pod.Spec.EnableServiceLinks {
		t.Fatal("service links must be disabled: they leak the namespace's shape into the tenant's environment")
	}

	// Pod-level posture.
	psc := pod.Spec.SecurityContext
	if psc == nil {
		t.Fatal("no pod securityContext")
	}
	if psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
		t.Fatal("runAsNonRoot is not set: the sandbox would be able to run as root")
	}
	if psc.RunAsUser == nil || *psc.RunAsUser != 1000 {
		t.Fatalf("runAsUser = %v, want 1000", psc.RunAsUser)
	}
	if psc.FSGroup == nil || *psc.FSGroup != 1000 {
		t.Fatalf("fsGroup = %v, want 1000", psc.FSGroup)
	}
	if psc.SeccompProfile == nil || psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("seccomp profile = %+v, want RuntimeDefault", psc.SeccompProfile)
	}

	// Container-level posture: the container provider's `--security-opt
	// no-new-privileges --cap-drop ALL --cap-add <the measured five>`.
	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("containers = %d, want one", len(pod.Spec.Containers))
	}
	container := pod.Spec.Containers[0]
	if container.Name != K8sContainerName {
		t.Fatalf("container name = %q", container.Name)
	}
	if container.Image != "piforge/sandbox-agent:test" {
		t.Fatalf("image = %q", container.Image)
	}
	// The container provider appends `sleep infinity` to every `docker run`; the pod
	// says the same thing with Args (CMD override, entrypoint untouched), so an
	// image whose default command exits is still a sandbox that stays up.
	if strings.Join(container.Args, " ") != "sleep infinity" {
		t.Fatalf("args = %v, want the keep-alive argv the container provider passes", container.Args)
	}
	csc := container.SecurityContext
	if csc == nil {
		t.Fatal("no container securityContext")
	}
	if csc.AllowPrivilegeEscalation == nil || *csc.AllowPrivilegeEscalation {
		t.Fatal("allowPrivilegeEscalation must be false (no-new-privileges)")
	}
	if csc.ReadOnlyRootFilesystem == nil || !*csc.ReadOnlyRootFilesystem {
		t.Fatal("readOnlyRootFilesystem must be true")
	}
	if csc.Capabilities == nil {
		t.Fatal("no capabilities block")
	}
	if len(csc.Capabilities.Drop) != 1 || csc.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("capabilities.drop = %v, want [ALL]", csc.Capabilities.Drop)
	}
	var added []string
	for _, capability := range csc.Capabilities.Add {
		added = append(added, string(capability))
	}
	if strings.Join(added, ",") != strings.Join(sandboxCapAdd, ",") {
		t.Fatalf("capabilities.add = %v, want the measured set %v", added, sandboxCapAdd)
	}

	// Volumes: the PVC at /workspace and nothing else writable on the root fs
	// except the two universal paths.
	mounts := map[string]string{}
	for _, mount := range container.VolumeMounts {
		mounts[mount.MountPath] = mount.Name
	}
	if _, ok := mounts["/workspace"]; !ok {
		t.Fatalf("no /workspace mount: %v", mounts)
	}
	if _, ok := mounts["/tmp"]; !ok {
		t.Fatalf("a read-only root filesystem without /tmp writable breaks every image: %v", mounts)
	}
	var sawPVC bool
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == names.PVC {
			sawPVC = true
		}
	}
	if !sawPVC {
		t.Fatalf("the pod does not mount its PVC %s: %v", names.PVC, pod.Spec.Volumes)
	}

	// Environment and the key reference.
	envVars := map[string]corev1.EnvVar{}
	for _, entry := range container.Env {
		envVars[entry.Name] = entry
	}
	if got := envVars["WORKSPACE"].Value; got != "/workspace" {
		t.Fatalf("WORKSPACE = %q", got)
	}
	if _, ok := envVars["BROWSER_NO_SANDBOX"]; !ok {
		t.Fatal("BROWSER_NO_SANDBOX is missing: Chromium cannot create its own sandbox under gVisor")
	}
	key, ok := envVars[SandboxAPIKeyEnv]
	if !ok {
		t.Fatal("the pod carries no SANDBOX_API_KEY reference")
	}
	if key.Value != "" {
		t.Fatal("the key must not be a literal in the pod spec; it belongs in the sandbox's Secret")
	}
	if key.ValueFrom == nil || key.ValueFrom.SecretKeyRef == nil ||
		key.ValueFrom.SecretKeyRef.Name != names.Secret || key.ValueFrom.SecretKeyRef.Key != K8sAPIKeySecretKey {
		t.Fatalf("SANDBOX_API_KEY does not reference %s/%s: %+v", names.Secret, K8sAPIKeySecretKey, key.ValueFrom)
	}

	// Limits: requests equal limits, in Kubernetes units converted from the
	// tenant policy's Docker units.
	limits := container.Resources.Limits
	if got := limits[corev1.ResourceCPU]; got.Cmp(resource.MustParse("2")) != 0 {
		t.Fatalf("cpu limit = %s, want 2", got.String())
	}
	if got := limits[corev1.ResourceMemory]; got.Cmp(resource.MustParse("2Gi")) != 0 {
		t.Fatalf("memory limit = %s, want 2Gi", got.String())
	}
	if got := container.Resources.Requests[corev1.ResourceCPU]; got.Cmp(resource.MustParse("2")) != 0 {
		t.Fatalf("cpu request = %s, want the limit (a promised sandbox is scheduled for it)", got.String())
	}

	// The checkout was provisioned through the sandbox API, not through pods/exec.
	if len(env.streamer.callsSnapshot()) != 0 {
		t.Fatalf("the checkout used pods/exec: %+v", env.streamer.callsSnapshot())
	}
	if len(env.shell.calls) < 3 {
		t.Fatalf("the checkout ran %d commands, want the probe, the prepare and the plan: %+v", len(env.shell.calls), env.shell.calls)
	}
	if !strings.Contains(env.shell.calls[0].command, "rev-parse --verify --quiet HEAD") {
		t.Fatalf("first command = %q, want the checkout readiness probe", env.shell.calls[0].command)
	}
}

// TestK8sEnsureIsIdempotent pins the contract's idempotency rule: a second Ensure
// for the same pair adopts what the first one made, key and all.
func TestK8sEnsureIsIdempotent(t *testing.T) {
	env := newTestK8s(t)
	ctx := context.Background()
	first, err := env.provider.Ensure(ctx, "user-1", "project-1", "", "main")
	if err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	second, err := env.provider.Ensure(ctx, "user-1", "project-1", "", "main")
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("ids differ: %q vs %q", first.ID, second.ID)
	}
	if first.SandboxAPIKey != second.SandboxAPIKey {
		t.Fatal("a second Ensure rotated the sandbox's API key; the stored row would be stale")
	}
	for _, resource := range []string{"pods", "persistentvolumeclaims", "services", "secrets"} {
		if got := env.count(t, resource); got != 1 {
			t.Fatalf("%s = %d after two Ensures, want 1", resource, got)
		}
	}
}

// TestK8sEnsureReplacesAPodWithoutAKey: a pod from an older shape (or one an
// operator made by hand) has an API that accepts anything that can reach it.
// Adopting it would call that a sandbox.
func TestK8sEnsureReplacesAPodWithoutAKey(t *testing.T) {
	userID, projectID := "user-2", "project-2"
	names := k8sNamesFor(userID, projectID)
	legacy := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.Pod,
			Namespace: "piforge",
			Labels:    map[string]string{K8sManagedByLabel: K8sManagedBy},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: K8sContainerName, Image: "old"}}},
	}
	env := newTestK8s(t, legacy)
	if _, err := env.provider.Ensure(context.Background(), userID, projectID, "", "main"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	pod := env.podOf(t, names.Pod)
	if pod.Spec.Containers[0].Image != "piforge/sandbox-agent:test" {
		t.Fatalf("the pod was adopted instead of replaced: image = %q", pod.Spec.Containers[0].Image)
	}
	if podAPIKey(pod) == nil {
		t.Fatal("the replacement pod has no key reference")
	}
}

// ---------------------------------------------------------------------------
// Get: the state machine
// ---------------------------------------------------------------------------

func TestK8sGetReportsEveryState(t *testing.T) {
	userID, projectID := "user-3", "project-3"
	names := k8sNamesFor(userID, projectID)

	pod := func(name string, phase corev1.PodPhase, ready bool) *corev1.Pod {
		out := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "piforge",
				Labels: map[string]string{
					K8sManagedByLabel: K8sManagedBy,
					K8sSandboxLabel:   names.Hash,
				},
				Annotations: map[string]string{K8sUserAnnotation: userID, K8sProjectAnnotation: projectID},
			},
			Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: K8sContainerName}}},
			Status: corev1.PodStatus{Phase: phase},
		}
		if ready {
			out.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		}
		return out
	}
	pvc := func(paused bool) *corev1.PersistentVolumeClaim {
		annotations := map[string]string{K8sUserAnnotation: userID, K8sProjectAnnotation: projectID}
		if paused {
			annotations[K8sPausedAnnotation] = "true"
		}
		return &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:        names.PVC,
				Namespace:   "piforge",
				Labels:      map[string]string{K8sManagedByLabel: K8sManagedBy},
				Annotations: annotations,
			},
		}
	}

	cases := []struct {
		name     string
		objects  []runtime.Object
		want     contracts.SandboxState
		notFound bool
		message  string
	}{
		{name: "a ready pod", objects: []runtime.Object{pod(names.Pod, corev1.PodRunning, true)}, want: contracts.SandboxReady},
		{name: "a pending pod", objects: []runtime.Object{pod(names.Pod, corev1.PodPending, false)}, want: contracts.SandboxStarting},
		{name: "a running but unready pod", objects: []runtime.Object{pod(names.Pod, corev1.PodRunning, false)}, want: contracts.SandboxStarting},
		{name: "a failed pod", objects: []runtime.Object{pod(names.Pod, corev1.PodFailed, false)}, want: contracts.SandboxError, message: "the pod is Failed"},
		{name: "a paused sandbox", objects: []runtime.Object{pvc(true)}, want: contracts.SandboxPaused, message: "workspace volume is kept"},
		{name: "a vanished pod", objects: []runtime.Object{pvc(false)}, want: contracts.SandboxMissing, message: "was not paused"},
		{name: "nothing at all", objects: nil, notFound: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestK8s(t, tc.objects...)
			sb, err := env.provider.Get(context.Background(), "sbx_"+names.Hash)
			if tc.notFound {
				if err == nil {
					t.Fatalf("Get = %+v, want ErrNotFound", sb)
				}
				if !strings.Contains(err.Error(), "not found") {
					t.Fatalf("error = %v, want a not-found error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if sb.State != tc.want {
				t.Fatalf("state = %q, want %q (message %q)", sb.State, tc.want, sb.Message)
			}
			if tc.message != "" && !strings.Contains(sb.Message, tc.message) {
				t.Fatalf("message = %q, want it to mention %q", sb.Message, tc.message)
			}
			// The row id and the object name both have to resolve (the store keeps
			// one, a human types the other).
			if sb.ID != names.Pod {
				t.Fatalf("id = %q, want %q", sb.ID, names.Pod)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Pause / Resume
// ---------------------------------------------------------------------------

// TestK8sPauseKeepsTheVolume is the design's pause: compute goes, storage stays.
func TestK8sPauseKeepsTheVolume(t *testing.T) {
	env := newTestK8s(t)
	ctx := context.Background()
	sb, err := env.provider.Ensure(ctx, "user-4", "project-4", "", "main")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	names := k8sNamesForID(sb.ID)

	if err := env.provider.Pause(ctx, sb.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	// Pod: gone. Everything that is the sandbox's identity and data: still here.
	env.absent(t, "pods", names.Pod)
	if got := env.pvcOf(t, names.PVC); got.Annotations[K8sPausedAnnotation] != "true" {
		t.Fatalf("pvc has no paused marker: %v", got.Annotations)
	}
	if got := string(env.secretOf(t, names.Secret).Data[K8sAPIKeySecretKey]); got != sb.SandboxAPIKey {
		t.Fatal("Pause deleted or rotated the sandbox's key")
	}
	env.serviceOf(t, names.Service)

	paused, err := env.provider.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after Pause: %v", err)
	}
	if paused.State != contracts.SandboxPaused {
		t.Fatalf("state after Pause = %q, want PAUSED", paused.State)
	}

	// Idempotent: pausing a paused sandbox is not an error, and it does not touch
	// the volume's marker a second time in a way that could fail.
	if err := env.provider.Pause(ctx, sb.ID); err != nil {
		t.Fatalf("second Pause: %v", err)
	}
}

// TestK8sResumeRebringsThePodWithTheSameKey is the other half: the pod comes back
// on the same volume, with the same identity, and the key the row holds still
// works — which is only true because the key lives in a Secret, not in the pod.
func TestK8sResumeRebringsThePodWithTheSameKey(t *testing.T) {
	env := newTestK8s(t)
	ctx := context.Background()
	sb, err := env.provider.Ensure(ctx, "user-5", "project-5", "", "main")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	names := k8sNamesForID(sb.ID)
	if err := env.provider.Pause(ctx, sb.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	resumed, err := env.provider.Resume(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.State != contracts.SandboxReady {
		t.Fatalf("state after Resume = %q, want READY", resumed.State)
	}
	if resumed.SandboxAPIKey != sb.SandboxAPIKey {
		t.Fatal("Resume handed back a different key; the stored row is now stale")
	}
	pod := env.podOf(t, names.Pod)
	if pod.DeletionTimestamp != nil {
		t.Fatal("the resumed pod is being deleted")
	}
	if got := env.pvcOf(t, names.PVC).Annotations[K8sPausedAnnotation]; got != "" {
		t.Fatalf("the parked marker was not cleared on Resume: %q", got)
	}
	if resumed.RootDir != "/workspace" || resumed.Workspace != contracts.WorkspaceAPI {
		t.Fatalf("resumed workspace = %q/%q", resumed.RootDir, resumed.Workspace)
	}
	// Resuming a sandbox that does not exist is not a way to create one.
	if _, err := env.provider.Resume(ctx, "piforge-doesnotexist"); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("Resume of an unknown sandbox = %v, want not found", err)
	}
}

// ---------------------------------------------------------------------------
// Destroy
// ---------------------------------------------------------------------------

func TestK8sDestroyDeletesEverything(t *testing.T) {
	env := newTestK8s(t)
	ctx := context.Background()
	sb, err := env.provider.Ensure(ctx, "user-6", "project-6", "", "main")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	names := k8sNamesForID(sb.ID)
	if err := env.provider.Destroy(ctx, sb.ID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	for _, resource := range []string{"pods", "persistentvolumeclaims", "services", "secrets"} {
		env.absent(t, resource, map[string]string{
			"pods":                   names.Pod,
			"persistentvolumeclaims": names.PVC,
			"services":               names.Service,
			"secrets":                names.Secret,
		}[resource])
	}
	// Idempotent, as the contract requires: a teardown retried after a partial
	// failure must not fail on what is already gone.
	if err := env.provider.Destroy(ctx, sb.ID); err != nil {
		t.Fatalf("second Destroy: %v", err)
	}
	if _, err := env.provider.Get(ctx, sb.ID); err == nil {
		t.Fatal("Get after Destroy returned a sandbox")
	}
}

// TestK8sDestroyRefusesAForeignVolume: a name is a hash, and a hash can be
// resolved against the wrong cluster. Destroy must never delete storage this
// deployment did not create.
func TestK8sDestroyRefusesAForeignVolume(t *testing.T) {
	names := k8sNamesFor("user-7", "project-7")
	foreign := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: names.PVC, Namespace: "piforge", Labels: map[string]string{"owner": "someone-else"}},
	}
	env := newTestK8s(t, foreign)
	err := env.provider.Destroy(context.Background(), names.Pod)
	if err == nil {
		t.Fatal("Destroy deleted a volume it did not create")
	}
	if !strings.Contains(err.Error(), K8sManagedByLabel) {
		t.Fatalf("error = %v, want it to name the missing %s label", err, K8sManagedByLabel)
	}
	env.pvcOf(t, names.PVC)
}

// ---------------------------------------------------------------------------
// Exec and Attach: the pods/exec seam
// ---------------------------------------------------------------------------

func TestK8sExecRunsArgv(t *testing.T) {
	env := newTestK8s(t)
	ctx := context.Background()
	sb, err := env.provider.Ensure(ctx, "user-8", "project-8", "", "main")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	env.streamer.stdout = "hello\n"

	out, err := env.provider.Exec(ctx, sb.ID, []string{"git", "status", "--porcelain"}, "/workspace/repo")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if out != "hello\n" {
		t.Fatalf("output = %q", out)
	}
	calls := env.streamer.callsSnapshot()
	if len(calls) != 1 {
		t.Fatalf("pods/exec calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.Pod != k8sNamesForID(sb.ID).Pod || call.Namespace != "piforge" || call.Container != K8sContainerName {
		t.Fatalf("exec targeted %+v", call)
	}
	want := []string{"/bin/sh", "-c", `cd -- "$0" && exec "$@"`, "/workspace/repo", "git", "status", "--porcelain"}
	if strings.Join(call.Argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %#v\nwant %#v", call.Argv, want)
	}
	if call.HasStdin {
		t.Fatal("Exec opened stdin; nothing should be able to write into a command that was not asked for one")
	}

	// A non-zero exit is an error carrying the output — the same shape Docker's
	// `docker exec` failure has.
	env.streamer.err = apierrors.NewBadRequest("command terminated with exit code 2")
	env.streamer.stdout = "fatal: not a git repository\n"
	if _, err := env.provider.Exec(ctx, sb.ID, []string{"git", "log"}, ""); err == nil ||
		!strings.Contains(err.Error(), "exit code 2") {
		t.Fatalf("Exec of a failing command = %v, want the exit status", err)
	} else if !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("error = %v, want it to carry the command's output", err)
	}
	// An empty argv is refused before anything is streamed.
	if _, err := env.provider.Exec(ctx, sb.ID, nil, ""); err == nil {
		t.Fatal("Exec accepted an empty argv")
	}
}

func TestK8sAttachIsADuplexStream(t *testing.T) {
	env := newTestK8s(t)
	ctx := context.Background()
	sb, err := env.provider.Ensure(ctx, "user-9", "project-9", "", "main")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	env.streamer.echo = true

	stream, err := env.provider.Attach(ctx, sb.ID, AttachSpec{
		Argv: []string{"/opt/piforge/piforged", "agent-launcher"},
		Dir:  "/workspace/worktrees/feat",
		Env:  []string{"PI_CODING_AGENT_DIR=/workspace/.piforge/agents/s1"},
		User: "gem",
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, err := stream.Write([]byte("{\"jsonrpc\":\"2.0\"}\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 64)
	n, err := stream.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf[:n]) != "{\"jsonrpc\":\"2.0\"}\n" {
		t.Fatalf("the stream echoed %q", string(buf[:n]))
	}
	if cw, ok := stream.(interface{ CloseWrite() error }); !ok {
		t.Fatal("the stream has no CloseWrite: the agent bridge half-closes stdin to mean \"shut down cleanly\"")
	} else if err := cw.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	calls := env.streamer.callsSnapshot()
	if len(calls) != 1 {
		t.Fatalf("pods/exec calls = %d", len(calls))
	}
	want := []string{
		"/bin/sh", "-c", `cd -- "$0" && exec "$@"`, "/workspace/worktrees/feat",
		"/usr/bin/env", "PI_CODING_AGENT_DIR=/workspace/.piforge/agents/s1",
		"/opt/piforge/piforged", "agent-launcher",
	}
	if strings.Join(calls[0].Argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %#v\nwant %#v", calls[0].Argv, want)
	}
	if !calls[0].HasStdin || !calls[0].HasStdout {
		t.Fatalf("the stream is not duplex: %+v", calls[0])
	}
	if calls[0].stdin != "{\"jsonrpc\":\"2.0\"}\n" {
		t.Fatalf("stdin reached the sandbox as %q", calls[0].stdin)
	}

	// pods/exec cannot choose a user, so asking for another account is refused
	// rather than silently ignored.
	if _, err := env.provider.Attach(ctx, sb.ID, AttachSpec{Argv: []string{"id"}, User: "root"}); err == nil ||
		!strings.Contains(err.Error(), "no per-command user") {
		t.Fatalf("Attach as root = %v, want a refusal naming pods/exec", err)
	}
	// An environment entry that is not NAME=VALUE would be read by `env` as an
	// option; it never reaches it.
	if _, err := env.provider.Attach(ctx, sb.ID, AttachSpec{Argv: []string{"id"}, Env: []string{"-i"}}); err == nil ||
		!strings.Contains(err.Error(), "NAME=VALUE") {
		t.Fatalf("Attach with a bare flag env = %v, want a refusal", err)
	}
	// Reattach shares the transport and appends -socket.
	if _, err := env.provider.Reattach(ctx, sb.ID, ReattachSpec{
		Socket: "/workspace/.piforge/sessions/s1.sock",
		Helper: []string{"/opt/piforge/piforged", "agent-attach"},
	}); err != nil {
		t.Fatalf("Reattach: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Preflight
// ---------------------------------------------------------------------------

func TestK8sPreflight(t *testing.T) {
	ctx := context.Background()

	t.Run("a configured cluster passes", func(t *testing.T) {
		env := newTestK8s(t, gvisorCluster()...)
		allowAccess(env.client)
		if err := env.provider.Preflight(ctx); err != nil {
			t.Fatalf("Preflight: %v", err)
		}
	})

	t.Run("a missing namespace names the manifest", func(t *testing.T) {
		// The runtime class is there, the namespace is not.
		env := newTestK8s(t, &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "gvisor"}, Handler: "runsc"})
		allowAccess(env.client)
		err := env.provider.Preflight(ctx)
		if err == nil {
			t.Fatal("Preflight passed without a namespace")
		}
		for _, want := range []string{`namespace "piforge" does not exist`, "deploy/k8s/00-namespace.yaml"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not say %q", err.Error(), want)
			}
		}
	})

	// The one whose absence is invisible at runtime: without the class, the pod
	// runs under runc and every panel looks exactly the same.
	t.Run("a missing gvisor names the consequence", func(t *testing.T) {
		env := newTestK8s(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "piforge"}})
		allowAccess(env.client)
		err := env.provider.Preflight(ctx)
		if err == nil {
			t.Fatal("Preflight passed without the runtime class")
		}
		for _, want := range []string{
			`runtime class "gvisor" does not exist`,
			"no syscall boundary between a tenant and the host",
			"deploy/k8s/30-runtimeclass.yaml",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not say %q", err.Error(), want)
			}
		}
	})

	t.Run("allow-runc accepts the weaker boundary on purpose", func(t *testing.T) {
		env := newTestK8s(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "piforge"}})
		allowAccess(env.client)
		env.provider.cfg.AllowRunc = true
		if err := env.provider.Preflight(ctx); err != nil {
			t.Fatalf("Preflight with -allow-runc: %v", err)
		}
	})

	t.Run("missing RBAC names the verb", func(t *testing.T) {
		env := newTestK8s(t, gvisorCluster()...)
		// No reactor: every SelfSubjectAccessReview comes back "not allowed", which
		// is what a namespace-scoped Role that forgot pods/exec produces.
		err := env.provider.Preflight(ctx)
		if err == nil {
			t.Fatal("Preflight passed with no RBAC answers")
		}
		for _, want := range []string{`create "pods/exec"`, `get "runtimeclasses"`} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not name %q", err.Error(), want)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Endpoint, ContainerPath, naming
// ---------------------------------------------------------------------------

func TestK8sEndpointIsTheServiceName(t *testing.T) {
	env := newTestK8s(t)
	ctx := context.Background()
	sb, err := env.provider.Ensure(ctx, "user-10", "project-10", "", "main")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	endpoint, err := env.provider.Endpoint(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	names := k8sNamesForID(sb.ID)
	want := "http://" + names.Service + ".piforge.svc.cluster.local:8080"
	if endpoint != want {
		t.Fatalf("endpoint = %q, want %q", endpoint, want)
	}
	// An override is how a control plane outside the cluster reaches it.
	env.provider.cfg.EndpointFor = func(sandboxID string) (string, error) {
		return "http://127.0.0.1:18080", nil
	}
	if got, _ := env.provider.Endpoint(ctx, sb.ID); got != "http://127.0.0.1:18080" {
		t.Fatalf("overridden endpoint = %q", got)
	}
	// And a sandbox with no service is not a name that fails later.
	env.provider.cfg.EndpointFor = nil
	if _, err := env.provider.Endpoint(ctx, "piforge-doesnotexist"); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("Endpoint of an unknown sandbox = %v", err)
	}
}

func TestK8sContainerPath(t *testing.T) {
	env := newTestK8s(t)
	for _, in := range []string{"/workspace", "/workspace/repo", "/workspace/worktrees/feat"} {
		got, err := env.provider.ContainerPath(in)
		if err != nil {
			t.Fatalf("ContainerPath(%q): %v", in, err)
		}
		if got != in {
			t.Fatalf("ContainerPath(%q) = %q, want the same path", in, got)
		}
	}
	for _, in := range []string{"/data/sandboxes/u/p", "/etc/passwd", "relative/path", "/workspacex"} {
		if _, err := env.provider.ContainerPath(in); err == nil {
			t.Fatalf("ContainerPath(%q) was accepted; a host path can only address the wrong tree", in)
		}
	}
}

func TestK8sNamingMatchesTheContainerProvider(t *testing.T) {
	userID, projectID := "user-11", "project-11"
	names := k8sNamesFor(userID, projectID)
	docker := &Docker{cfg: DockerConfig{}}
	if names.Pod != docker.containerName(userID, projectID) {
		t.Fatalf("pod name %q != container name %q", names.Pod, docker.containerName(userID, projectID))
	}
	if names.PVC != detachedVolumeName(names.Pod) {
		t.Fatalf("pvc name %q != the detached workspace volume name %q", names.PVC, detachedVolumeName(names.Pod))
	}
	if names.Secret == names.Pod || names.Secret == names.PVC {
		t.Fatalf("the secret shares a name with another object: %q", names.Secret)
	}
	// Every id form the deployment hands around resolves to the same objects.
	for _, id := range []string{names.Pod, "sbx_" + names.Hash, names.Hash} {
		if got := k8sNamesForID(id); got.Pod != names.Pod || got.PVC != names.PVC || got.Secret != names.Secret {
			t.Fatalf("k8sNamesForID(%q) = %+v", id, got)
		}
	}
	if ID(userID, projectID) != "sbx_"+names.Hash {
		t.Fatalf("ID = %q, want sbx_%s", ID(userID, projectID), names.Hash)
	}
}

// TestK8sLabelValue: a tenant id that Kubernetes cannot put in a label must not
// turn into an API-server rejection at create time.
func TestK8sLabelValue(t *testing.T) {
	cases := map[string]string{
		"6f1e6a3c-8f7d-4b1e-9a2c-1d2e3f4a5b6c": "6f1e6a3c-8f7d-4b1e-9a2c-1d2e3f4a5b6c",
		// '@' is not a legal label rune; '.' is, which is why an address keeps its
		// dots and loses its at-sign.
		"user@example.com":      "user-example.com",
		"":                      "unknown",
		"---":                   "unknown",
		strings.Repeat("a", 80): strings.Repeat("a", 63),
	}
	for in, want := range cases {
		if got := K8sLabelValue(in); got != want {
			t.Fatalf("K8sLabelValue(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestK8sResources pins the unit conversion: the policy is in Docker's units and
// the pod is in Kubernetes'.
func TestK8sResources(t *testing.T) {
	resources, err := k8sResources(Limits{CPUs: "0.5", Memory: "512m"})
	if err != nil {
		t.Fatalf("k8sResources: %v", err)
	}
	if got := resources.Limits[corev1.ResourceCPU]; got.MilliValue() != 500 {
		t.Fatalf("cpu = %s, want 500m", got.String())
	}
	if got := resources.Limits[corev1.ResourceMemory]; got.Value() != 512<<20 {
		t.Fatalf("memory = %s, want 512Mi (Docker's m is 2^20)", got.String())
	}
	if got := resources.Requests; got.Cpu().MilliValue() != 500 || got.Memory().Value() != 512<<20 {
		t.Fatalf("requests = %v, want them equal to the limits", got)
	}
	if _, err := k8sResources(Limits{Memory: "2 zettabytes"}); err == nil {
		t.Fatal("an unparsable memory limit was accepted")
	}
}

// ---------------------------------------------------------------------------
// Worktree
// ---------------------------------------------------------------------------

// TestK8sAddWorktreeUsesTheSandboxAPI: the branch worktree is created inside the
// sandbox through its own API, as the sandbox user — the same plan Docker runs,
// which is the point of checkout.go.
func TestK8sAddWorktreeUsesTheSandboxAPI(t *testing.T) {
	env := newTestK8s(t)
	ctx := context.Background()
	sb, err := env.provider.Ensure(ctx, "user-12", "project-12", "", "main")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	env.shell.calls = nil
	if err := env.provider.AddWorktree(ctx, sb.ID, "/workspace/repo", "/workspace/worktrees/feat", "feat"); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if len(env.shell.calls) < 3 {
		t.Fatalf("AddWorktree ran %d commands: %+v", len(env.shell.calls), env.shell.calls)
	}
	// The first command is the API liveness probe (the same one Docker's
	// waitForSandboxShell runs); the worktree probe follows it.
	if !strings.Contains(env.shell.calls[0].command, "piforge-api-ready") {
		t.Fatalf("first command = %+v, want the sandbox API probe", env.shell.calls[0])
	}
	first := env.shell.calls[1]
	if !strings.Contains(first.command, "/workspace/worktrees/feat") || first.cwd != "/workspace" {
		t.Fatalf("second command = %+v, want the worktree readiness probe", first)
	}
	joined := ""
	for _, call := range env.shell.calls {
		joined += call.command + "\n"
		if strings.Contains(strings.ToLower(call.command), "docker") {
			t.Fatalf("the worktree plan mentioned docker: %q", call.command)
		}
	}
	if !strings.Contains(joined, "worktree") {
		t.Fatalf("the plan never ran git worktree: %q", joined)
	}
	// A worktree path outside the sandbox is refused before any command runs.
	if err := env.provider.AddWorktree(ctx, sb.ID, "/data/sandboxes/u/p/repo", "/workspace/worktrees/x", "x"); err == nil {
		t.Fatal("AddWorktree accepted a host path")
	}
}

// TestK8sNewRefusesIncompleteConfiguration: the flags that cannot mean anything
// fail at construction, not at the first session.
func TestK8sNewRefusesIncompleteConfiguration(t *testing.T) {
	if _, err := NewK8s(K8sConfig{Namespace: "piforge", Clientset: fake.NewClientset(), RESTConfig: nil}); err == nil ||
		!strings.Contains(err.Error(), "image is required") {
		t.Fatalf("NewK8s without an image = %v", err)
	}
	if _, err := NewK8s(K8sConfig{
		Namespace: "piforge",
		Image:     "piforge/sandbox-agent:test",
		CPUs:      "not-a-number",
	}); err == nil {
		t.Fatal("NewK8s accepted an unparsable cpu limit")
	}
}
