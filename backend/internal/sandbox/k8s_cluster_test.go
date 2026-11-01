package sandbox

// The real-cluster half.
//
// This test is deliberately *not* behind a build tag: it compiles on every
// `go vet ./...` and `go test ./...`, and it skips unless the environment names a
// cluster. A build tag would let it rot (nothing would compile it until somebody
// remembered the tag), and the point of the file is that the boundary between
// "proven with a fake client" and "proven only by a cluster" is written down and
// executable.
//
// What it proves that the fake cannot:
//
//   - the objects are *accepted* by a real API server: field names, label and
//     annotation syntax, the Secret reference, the PodSpec, the headless Service
//     (a fake clientset stores anything);
//   - the scheduler runs the pod under the configured runtime class, and the
//     kubelet starts the image (a fake client has no kubelet);
//   - pods/exec is a working SPDY stream: bytes in, bytes out, exit status, and
//     the `sh -c` cwd wrapper (a fake client cannot stream);
//   - pause/resume/destroy against the real garbage collector: the pod really
//     goes away, the PVC really survives it, and a recreated pod really gets its
//     volume back;
//   - the securityContext is not rejected by the cluster's pod security admission
//     (a policy violation is an API-server answer, not a client-side one).
//
// What it still does not prove, and nothing but a full deployment can: that the
// *sandbox image* boots as uid 1000 with a read-only root filesystem and serves
// /v1/health, and that this process can reach the Service's DNS name. Both need
// the control plane inside the cluster network; docs/SANDBOX-K8S.md lists them.
//
// Run it:
//
//	PIFORGE_K8S_TEST=1 \
//	PIFORGE_K8S_TEST_KUBECONFIG=$HOME/.kube/config \
//	PIFORGE_K8S_TEST_IMAGE=piforge/sandbox-agent:dev \
//	PIFORGE_K8S_TEST_NAMESPACE=piforge \
//	go test ./internal/sandbox -run TestK8sClusterLifecycle -count=1 -v
//
// On a kind cluster without gVisor, add PIFORGE_K8S_TEST_RUNTIME_CLASS="" and
// PIFORGE_K8S_TEST_ALLOW_RUNC=1 — the test then says, in its output, that what it
// proved is the lifecycle and not the syscall boundary.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/contracts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/exec"
)

// k8sClusterProvider builds a provider against the cluster the environment names,
// or skips with the command that would run it.
func k8sClusterProvider(t *testing.T) *K8s {
	t.Helper()
	const run = "PIFORGE_K8S_TEST=1 PIFORGE_K8S_TEST_KUBECONFIG=$HOME/.kube/config " +
		"PIFORGE_K8S_TEST_IMAGE=<sandbox image> PIFORGE_K8S_TEST_NAMESPACE=piforge " +
		"go test ./internal/sandbox -run TestK8sClusterLifecycle -count=1 -v"
	if strings.TrimSpace(os.Getenv("PIFORGE_K8S_TEST")) != "1" {
		t.Skipf("real-cluster test: needs a Kubernetes cluster.\n"+
			"It is skipped unless PIFORGE_K8S_TEST=1 is set. To run it:\n  %s\n"+
			"Everything else in this package is proven against client-go's fake clientset (k8s_test.go).", run)
	}
	image := strings.TrimSpace(os.Getenv("PIFORGE_K8S_TEST_IMAGE"))
	if image == "" {
		t.Skipf("real-cluster test: PIFORGE_K8S_TEST_IMAGE names the sandbox image to run.\n  %s", run)
	}
	cfg := K8sConfig{
		Kubeconfig:   strings.TrimSpace(os.Getenv("PIFORGE_K8S_TEST_KUBECONFIG")),
		Context:      strings.TrimSpace(os.Getenv("PIFORGE_K8S_TEST_CONTEXT")),
		Namespace:    strings.TrimSpace(os.Getenv("PIFORGE_K8S_TEST_NAMESPACE")),
		Image:        image,
		RuntimeClass: os.Getenv("PIFORGE_K8S_TEST_RUNTIME_CLASS"),
		AllowRunc:    strings.TrimSpace(os.Getenv("PIFORGE_K8S_TEST_ALLOW_RUNC")) == "1",
		APIPort:      8080,
	}
	if _, set := os.LookupEnv("PIFORGE_K8S_TEST_RUNTIME_CLASS"); !set {
		cfg.RuntimeClass = DefaultK8sRuntimeClass
	}
	if cfg.AllowRunc {
		t.Log("PIFORGE_K8S_TEST_ALLOW_RUNC=1: this run proves the lifecycle, not a syscall boundary")
	}
	provider, err := NewK8s(cfg)
	if err != nil {
		t.Fatalf("NewK8s: %v", err)
	}
	// The checkout normally goes through the sandbox's HTTP API, which needs this
	// process to resolve the Service's cluster DNS name — true inside the cluster
	// and not true from a laptop with only a kubeconfig. pods/exec reaches the pod
	// through the API server, so the lifecycle is testable from either place. The
	// API path itself is exercised by TestK8sEnsureCreatesTheSandbox (the plan and
	// its execution) and by the e2e drill against a real deployment.
	provider.cfg.openCheckout = func(ctx context.Context, sandboxID string) (checkoutShell, error) {
		names, err := provider.namesFromID(sandboxID)
		if err != nil {
			return nil, err
		}
		return podsExecShell{provider: provider, pod: names.Pod}, nil
	}
	provider.cfg.EnsureTimeout = 5 * time.Minute
	provider.cfg.RequestTimeout = time.Minute
	provider.cfg.GraceStop = 10 * time.Second
	return provider
}

// podsExecShell is a checkoutShell over pods/exec: the sandbox's own command
// channel, with the `cd` prefix aio.Client.Exec adds (the image ignores the cwd
// field, so the directory has to be in the command line either way).
type podsExecShell struct {
	provider *K8s
	pod      string
}

func (s podsExecShell) Exec(ctx context.Context, command, cwd string) (aio.ShellResult, error) {
	if dir := strings.TrimSpace(cwd); dir != "" {
		command = "cd -- " + aio.Quote(dir) + " && " + command
	}
	var out lockedBuffer
	err := s.provider.streamer.Exec(ctx, s.provider.cfg.Namespace, s.pod, k8sExecOptions{
		Container: K8sContainerName,
		Argv:      []string{"/bin/sh", "-c", command},
		Stdout:    &out,
		Stderr:    &out,
	})
	if err != nil {
		var code exec.CodeExitError
		if errors.As(err, &code) {
			return aio.ShellResult{Output: out.String(), ExitCode: code.Code}, nil
		}
		return aio.ShellResult{}, err
	}
	return aio.ShellResult{Output: out.String()}, nil
}

// TestK8sClusterLifecycle is the whole story on a real cluster: create → get →
// exec → pause (pod gone, PVC present) → resume (pod back) → destroy (all gone).
func TestK8sClusterLifecycle(t *testing.T) {
	provider := k8sClusterProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	if err := provider.Preflight(ctx); err != nil {
		t.Fatalf("Preflight: %v", err)
	}

	userID := "cluster-test-user"
	projectID := "cluster-test-project"
	// A previous failed run may have left the pair behind; start clean so the
	// "create" half is a real create.
	_ = provider.Destroy(ctx, k8sNamesFor(userID, projectID).Pod)
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 3*time.Minute)
		defer done()
		_ = provider.Destroy(cleanup, k8sNamesFor(userID, projectID).Pod)
	})

	sb, err := provider.Ensure(ctx, userID, projectID, "", "main")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	names := k8sNamesFor(userID, projectID)
	if sb.State != contracts.SandboxReady {
		t.Fatalf("state = %q, want READY", sb.State)
	}
	if sb.RootDir != "/workspace" || sb.Workspace != contracts.WorkspaceAPI {
		t.Fatalf("workspace = %q/%q, want /workspace/api", sb.RootDir, sb.Workspace)
	}
	key := sb.SandboxAPIKey
	if len(key) != 64 {
		t.Fatalf("api key length = %d", len(key))
	}

	// --- the objects a real API server accepted -----------------------------
	pod, err := provider.pod(ctx, names.Pod)
	if err != nil {
		t.Fatalf("read pod: %v", err)
	}
	if pod.Labels[K8sSandboxLabel] != names.Hash || pod.Labels[K8sManagedByLabel] != K8sManagedBy {
		t.Fatalf("pod labels = %v", pod.Labels)
	}
	if pod.Labels[K8sUserLabel] != K8sLabelValue(userID) {
		t.Fatalf("pod user label = %q", pod.Labels[K8sUserLabel])
	}
	if ref := provider.cfg.RuntimeClass; ref != "" {
		if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != ref {
			t.Fatalf("runtimeClassName = %v, want %q (was the class installed?)", pod.Spec.RuntimeClassName, ref)
		}
	}
	if pod.Spec.SecurityContext == nil || pod.Spec.SecurityContext.RunAsNonRoot == nil || !*pod.Spec.SecurityContext.RunAsNonRoot {
		t.Fatalf("pod securityContext = %+v, want runAsNonRoot", pod.Spec.SecurityContext)
	}
	container := pod.Spec.Containers[0]
	if container.SecurityContext == nil || container.SecurityContext.Capabilities == nil ||
		len(container.SecurityContext.Capabilities.Drop) != 1 {
		t.Fatalf("container securityContext = %+v, want a capability drop", container.SecurityContext)
	}
	if pod.Status.Phase != corev1.PodRunning {
		t.Fatalf("pod phase = %q, want Running", pod.Status.Phase)
	}

	// --- exec: the SPDY stream and the exit status --------------------------
	out, err := provider.Exec(ctx, sb.ID, []string{"sh", "-c", "echo k8s-exec-ok; id -u"}, "/workspace")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(out, "k8s-exec-ok") {
		t.Fatalf("Exec output = %q", out)
	}
	if !strings.Contains(out, "1000") {
		t.Fatalf("Exec ran as something other than 1000: %q", out)
	}
	if _, err := provider.Exec(ctx, sb.ID, []string{"sh", "-c", "exit 7"}, "/workspace"); err == nil {
		t.Fatal("Exec of a failing command returned no error")
	} else if !strings.Contains(err.Error(), "exit") {
		t.Fatalf("error = %v, want the exit status named", err)
	}

	// --- get -----------------------------------------------------------------
	current, err := provider.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if current.State != contracts.SandboxReady {
		t.Fatalf("Get state = %q", current.State)
	}

	// --- pause: the pod goes, the volume stays ------------------------------
	if err := provider.Pause(ctx, sb.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	gone, err := provider.pod(ctx, names.Pod)
	if err == nil {
		t.Fatalf("the pod still exists after Pause: %+v", gone.Status.Phase)
	}
	claim, err := provider.claim(ctx, names.PVC)
	if err != nil {
		t.Fatalf("the volume is gone after Pause: %v", err)
	}
	if claim.Annotations[K8sPausedAnnotation] != "true" {
		t.Fatalf("the volume has no paused marker: %v", claim.Annotations)
	}
	paused, err := provider.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after Pause: %v", err)
	}
	if paused.State != contracts.SandboxPaused {
		t.Fatalf("state after Pause = %q", paused.State)
	}

	// --- resume: the pod comes back on the same volume, with the same key ----
	resumed, err := provider.Resume(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.State != contracts.SandboxReady {
		t.Fatalf("state after Resume = %q", resumed.State)
	}
	if resumed.SandboxAPIKey != key {
		t.Fatal("Resume changed the sandbox's API key; the row the control plane stored is stale")
	}
	if _, err := provider.pod(ctx, names.Pod); err != nil {
		t.Fatalf("the pod did not come back: %v", err)
	}

	// --- destroy: everything goes ------------------------------------------
	if err := provider.Destroy(ctx, sb.ID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	for _, resource := range []string{"pods", "persistentvolumeclaims", "services", "secrets"} {
		name := map[string]string{
			"pods":                   names.Pod,
			"persistentvolumeclaims": names.PVC,
			"services":               names.Service,
			"secrets":                names.Secret,
		}[resource]
		var exists bool
		switch resource {
		case "pods":
			_, err = provider.pod(ctx, name)
			exists = err == nil
		case "persistentvolumeclaims":
			_, err = provider.claim(ctx, name)
			exists = err == nil
		case "services":
			_, err = provider.service(ctx, name)
			exists = err == nil
		case "secrets":
			_, err = provider.secret(ctx, name)
			exists = err == nil
		}
		if exists {
			t.Fatalf("%s %s survived Destroy", resource, name)
		}
	}
	if _, err := provider.Get(ctx, sb.ID); err == nil {
		t.Fatal("Get after Destroy returned a sandbox")
	}
}

// TestK8sClusterLabelsAreSelectable proves the labels are useful to the thing that
// reads them for an operator: one selector finds exactly one sandbox's objects.
func TestK8sClusterLabelsAreSelectable(t *testing.T) {
	provider := k8sClusterProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := provider.Preflight(ctx); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	userID, projectID := "cluster-label-user", "cluster-label-project"
	names := k8sNamesFor(userID, projectID)
	_ = provider.Destroy(ctx, names.Pod)
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 3*time.Minute)
		defer done()
		_ = provider.Destroy(cleanup, names.Pod)
	})
	if _, err := provider.Ensure(ctx, userID, projectID, "", "main"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	selector := K8sSandboxLabel + "=" + names.Hash
	pods, err := provider.client.CoreV1().Pods(provider.cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		t.Fatalf("list pods by label: %v", err)
	}
	if len(pods.Items) != 1 || pods.Items[0].Name != names.Pod {
		t.Fatalf("selector %q returned %d pods", selector, len(pods.Items))
	}
	claims, err := provider.client.CoreV1().PersistentVolumeClaims(provider.cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		t.Fatalf("list pvcs by label: %v", err)
	}
	if len(claims.Items) != 1 || claims.Items[0].Name != names.PVC {
		t.Fatalf("selector %q returned %d volumes", selector, len(claims.Items))
	}
}
