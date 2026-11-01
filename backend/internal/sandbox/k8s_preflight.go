package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Preflight: the four things that have to be true before the first session, read
// from the cluster instead of assumed.
//
// It exists for the reason internal/preflight exists for Docker: the failure it
// catches is discovered twenty minutes later, at the first prompt, and it is
// always configuration — a context that points at the wrong cluster, a namespace
// nobody created, a Role that omits `pods/exec`, a cluster without the sandbox
// runtime. Each of those produces a *different* downstream error (a 404 at
// create, a 403 at the checkout, a pod that runs under runc), and none of them
// names the missing piece.
//
// It refuses the boot. The alternative — starting and failing per session — is
// what an operator would experience as "the product is broken" rather than "the
// namespace is missing".
//
// Every check is answerable by the API server, which is why this is testable
// against the fake clientset (k8s_test.go) and why it never needs a real cluster
// to be *wrong* for the right reason.

// k8sAccess is one RBAC answer the provider needs.
//
// Group is empty for the core API group. Resource may carry a subresource
// ("pods/exec"), which is what the API's ResourceAttributes expects.
type k8sAccess struct {
	Verb     string
	Group    string
	Resource string
	// Cluster says the object is cluster-scoped (a runtime class), so the review
	// is asked without a namespace.
	Cluster bool
	// Why names the operation that needs it, for the error message.
	Why string
}

// k8sRequiredAccess is the complete permission set of the provider.
//
// Read it as the RBAC contract: deploy/k8s/rbac.yaml grants exactly this and
// nothing else, and a deployment that tightens it gets a preflight failure naming
// the verb rather than a session that dies later.
func k8sRequiredAccess() []k8sAccess {
	return []k8sAccess{
		{Verb: "create", Resource: "pods", Why: "Ensure creates a sandbox pod"},
		{Verb: "get", Resource: "pods", Why: "Get, Pause and the readiness wait read the pod"},
		{Verb: "delete", Resource: "pods", Why: "Pause and Destroy stop the pod"},
		{Verb: "create", Resource: "pods/exec", Why: "Exec, Attach and Reattach stream a process inside the pod"},
		{Verb: "create", Resource: "persistentvolumeclaims", Why: "Ensure creates the workspace volume"},
		{Verb: "get", Resource: "persistentvolumeclaims", Why: "Get distinguishes paused from missing by the volume"},
		{Verb: "update", Resource: "persistentvolumeclaims", Why: "Pause and Resume write or clear the parked marker"},
		{Verb: "delete", Resource: "persistentvolumeclaims", Why: "Destroy removes the workspace volume"},
		{Verb: "create", Resource: "services", Why: "Ensure creates the sandbox's API service"},
		{Verb: "get", Resource: "services", Why: "Endpoint resolves the service before dialling it"},
		{Verb: "delete", Resource: "services", Why: "Destroy removes the service"},
		{Verb: "create", Resource: "secrets", Why: "Ensure creates the per-sandbox API key"},
		{Verb: "get", Resource: "secrets", Why: "the key is read back from the Secret on every create, resume and API call"},
		{Verb: "delete", Resource: "secrets", Why: "Destroy removes the key with the sandbox"},
		{Verb: "get", Group: "node.k8s.io", Resource: "runtimeclasses", Cluster: true, Why: "Preflight checks that the sandbox runtime class exists"},
	}
}

// Preflight verifies that this process can do its job against this cluster, and
// returns one error naming every problem.
//
// All four checks run before anything is reported: a cluster missing both the
// namespace and the runtime class should say so once.
func (k *K8s) Preflight(ctx context.Context) error {
	var problems []string

	version, err := k.client.Discovery().ServerVersion()
	if err != nil {
		problems = append(problems, fmt.Sprintf("the Kubernetes API is not reachable from here (%v): check -sandbox-k8s-kubeconfig / PIFORGE_SANDBOX_K8S_KUBECONFIG and its context, or the service account of the pod this runs in", err))
		return k8sPreflightError(problems)
	}

	if _, err := k.client.CoreV1().Namespaces().Get(ctx, k.cfg.Namespace, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			problems = append(problems, fmt.Sprintf("namespace %q does not exist: apply deploy/k8s/00-namespace.yaml (or `kubectl create namespace %s`) before starting, or point -sandbox-k8s-namespace at the namespace the deployment was granted",
				k.cfg.Namespace, k.cfg.Namespace))
		} else {
			problems = append(problems, fmt.Sprintf("namespace %q cannot be read (%v): the sandbox provider needs `get namespaces` for its own namespace so it can refuse a start in a namespace that is not there",
				k.cfg.Namespace, err))
		}
	}

	problems = append(problems, k.preflightRuntimeClass(ctx)...)
	problems = append(problems, k.preflightAccess(ctx)...)

	if len(problems) > 0 {
		return k8sPreflightError(problems)
	}
	slog.Info("sandbox: k8s: preflight passed",
		"server", version.GitVersion,
		"namespace", k.cfg.Namespace,
		"runtime_class", k.cfg.RuntimeClass,
		"verified", "namespace, runtime class, RBAC (pods, pods/exec, pvc, services)")
	return nil
}

// preflightRuntimeClass refuses the one configuration whose consequence is
// invisible at runtime: a pod without a sandbox runtime.
//
// The sentence matters more than the rule. A cluster without gVisor still *runs*
// the pod — it runs it under runc, on the node's kernel, behind no syscall filter
// beyond seccomp — and every panel and session then looks exactly like a sandbox.
// That is the shape this check exists to make impossible to reach by accident.
func (k *K8s) preflightRuntimeClass(ctx context.Context) []string {
	class := strings.TrimSpace(k.cfg.RuntimeClass)
	if class == "" {
		if k.cfg.AllowRunc {
			slog.Warn("sandbox: k8s: running without a RuntimeClass (-sandbox-k8s-allow-runc): the sandbox is an ordinary container on the node's kernel",
				"consequence", "a kernel bug or a container escape reaches the node, and every tenant's pod shares that kernel")
			return nil
		}
		return []string{"no runtime class is configured, so pods would run under the node's default runtime (runc): the sandbox would be an ordinary container on the node's kernel. Install gVisor and keep -sandbox-k8s-runtime-class gvisor (deploy/k8s/30-runtimeclass.yaml), or accept the weaker boundary explicitly with -sandbox-k8s-allow-runc"}
	}
	_, err := k.client.NodeV1().RuntimeClasses().Get(ctx, class, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if apierrors.IsNotFound(err) {
		if k.cfg.AllowRunc {
			slog.Warn("sandbox: k8s: the runtime class is missing and -sandbox-k8s-allow-runc was given; pods will run under the node's default runtime",
				"runtime_class", class)
			return nil
		}
		return []string{fmt.Sprintf("runtime class %q does not exist on this cluster: every sandbox pod would be scheduled with a runtimeClassName the cluster cannot satisfy (the pod stays Pending) or, if the class were dropped, run under runc on the node's kernel — no syscall boundary between a tenant and the host. Install gVisor and apply deploy/k8s/30-runtimeclass.yaml (which registers %q and a runtimeClassPath), or name the class this cluster does have",
			class, class)}
	}
	if apierrors.IsForbidden(err) {
		return []string{fmt.Sprintf("cannot read runtime classes (%v): preflight has to answer \"is %q installed\", and `get runtimeclasses` is the only way to ask; add it to the sandbox ClusterRole (deploy/k8s/rbac.yaml)", err, class)}
	}
	return []string{fmt.Sprintf("cannot check runtime class %q: %v", class, err)}
}

// preflightAccess asks the API server what this identity may do, one review per
// (verb, resource).
//
// SelfSubjectAccessReview rather than a dry-run: it is the question "would this be
// allowed", answered by the same authorizer that will answer the real request, and
// it needs no object to exist first. A deployment that grants everything the
// provider needs gets twelve "allowed" answers and starts; one that forgot
// `pods/exec` starts nothing and is told which line to add.
func (k *K8s) preflightAccess(ctx context.Context) []string {
	var problems []string
	for _, access := range k8sRequiredAccess() {
		review := &authorizationv1.SelfSubjectAccessReview{
			Spec: authorizationv1.SelfSubjectAccessReviewSpec{
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Verb:     access.Verb,
					Group:    access.Group,
					Resource: access.Resource,
				},
			},
		}
		if !access.Cluster {
			review.Spec.ResourceAttributes.Namespace = k.cfg.Namespace
		}
		answer, err := k.client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
		if err != nil {
			problems = append(problems, fmt.Sprintf("cannot ask whether %s %q is allowed (%v): the sandbox provider needs `create selfsubjectaccessreviews`", access.Verb, access.Resource, err))
			continue
		}
		if answer.Status.Allowed {
			continue
		}
		problems = append(problems, fmt.Sprintf("%s %q is not allowed in %s%s (%s): %s",
			access.Verb, access.Resource, k.cfg.Namespace, clusterSuffix(access), access.Why, accessReason(answer.Status.Reason)))
	}
	// Sorted so a report with several gaps is stable and diffable.
	sort.Strings(problems)
	return problems
}

func clusterSuffix(access k8sAccess) string {
	if access.Cluster {
		return " (cluster-scoped)"
	}
	return ""
}

func accessReason(reason string) string {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return "apply deploy/k8s/rbac.yaml, or add the rule it names to the role this deployment runs as"
	}
	return trimmed
}

// k8sPreflightError renders the report: one line per problem, in a fixed order,
// with the command that fixes it already named by each line.
func k8sPreflightError(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	var out strings.Builder
	fmt.Fprintf(&out, "sandbox: k8s: preflight failed for %s (%d problem(s)); nothing was created:\n", "the configured cluster", len(problems))
	for _, problem := range problems {
		fmt.Fprintf(&out, "  - %s\n", problem)
	}
	out.WriteString("docs/SANDBOX-K8S.md lists the manifests (namespace, service account, RBAC, runtime class) and the flags")
	return fmt.Errorf("%s", out.String())
}
