package sandbox

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// The Kubernetes objects one sandbox is made of, built as pure functions.
//
// Three objects, one per concept, and nothing else: a Pod (the runtime), a
// PersistentVolumeClaim (the workspace) and a headless Service (the name the
// control plane dials). There is deliberately no Deployment/StatefulSet: a
// sandbox is one instance with an identity, and "scale to zero" for it means
// "delete the pod, keep the volume", which is what Pause does — not a replica
// count that a controller would then be entitled to restore on its own.
//
// Every function here is pure so a test can assert the whole object graph — the
// labels, the securityContext, the runtime class, the volume — without a cluster
// (k8s_test.go), and so the one place that decides what a sandbox *is* stays
// readable.

// Label, annotation and name constants. They are the API between this provider,
// the manifests in deploy/k8s and the NetworkPolicy/ResourceQuota an operator
// applies, so they are exported as documentation rather than hidden in strings.
const (
	// K8sManagedByLabel marks an object this deployment created. Destroy refuses
	// to delete one that does not carry it: a mis-resolved name must not be able
	// to remove a volume somebody else's workload owns.
	K8sManagedByLabel = "app.kubernetes.io/managed-by"
	// K8sNameLabel is the conventional name label (piforge-sandbox).
	K8sNameLabel = "app.kubernetes.io/name"
	// K8sPartOfLabel groups the objects with the rest of the deployment.
	K8sPartOfLabel = "app.kubernetes.io/part-of"
	// K8sSandboxLabel carries the pair hash. Both the Pod and its PVC carry it,
	// which is how "which volume belongs to this sandbox" is answered by a label
	// selector rather than by string surgery on a name.
	K8sSandboxLabel = "piforge.io/sandbox"
	// K8sUserLabel and K8sProjectLabel carry the tenant and the pair. They are
	// label *values*, so they are sanitised (K8sLabelValue): an id with a rune
	// Kubernetes does not allow would make the create fail at the API server,
	// far from the id that caused it. The exact ids travel as annotations.
	K8sUserLabel    = "piforge.io/user"
	K8sProjectLabel = "piforge.io/project"

	// K8sUserAnnotation and K8sProjectAnnotation keep the ids verbatim, for the
	// operator (and the audit) that has to map an object back to an account.
	K8sUserAnnotation    = "piforge.io/user-id"
	K8sProjectAnnotation = "piforge.io/project-id"
	// K8sPausedAnnotation is the durable "this sandbox was parked on purpose"
	// marker. It is what separates PAUSED (pod deleted by us, PVC kept) from
	// MISSING (the runtime vanished on its own) when Get reads the cluster: a
	// pod that disappeared without this marker is not a pause.
	K8sPausedAnnotation = "piforge.io/paused"

	// K8sContainerName is the sandbox container inside the pod.
	K8sContainerName = "sandbox"
	// K8sAPIPortName names the API port so the Service targets it by name.
	K8sAPIPortName = "api"
	// K8sPVCVolumeName is the volume name the PVC is mounted under.
	K8sPVCVolumeName = "workspace"
	// K8sManagedBy is the value K8sManagedByLabel carries.
	K8sManagedBy = "piforge"

	// K8sAPIKeySecretKey is the key inside a sandbox's Secret.
	//
	// The per-sandbox API key is a Secret rather than a literal in the pod's env
	// for the same reason the container provider takes it out of `docker inspect`
	// rather than from a flag: it has to outlive the pod. A paused sandbox has no
	// pod, and a control plane that restarted has no memory, so the key has to
	// live somewhere durable that belongs to the sandbox — and the place that
	// belongs to the sandbox and outlives every recreation is its Volume plus this
	// Secret. A Secret (not an annotation) because `kubectl describe` prints
	// annotations and does not print Secret data.
	K8sAPIKeySecretKey = "api-key"
)

// k8sNames is the three names one (user, project) pair resolves to.
//
// The pod and the Service share a name — the design's addressing rule, so
// `http://<sandbox>.<ns>.svc` is one string an operator can type — and the PVC is
// the same name with the `piforge-ws-` prefix the container provider already uses
// for a detached workspace (detachedVolumeName). One pair therefore has one
// storage name in both providers, which is what makes "the workspace of this
// sandbox" answerable without knowing which shape created it.
type k8sNames struct {
	Pod     string
	Service string
	PVC     string
	Secret  string
	Hash    string
}

// k8sNamesFor derives the names from the same hash everything else uses.
func k8sNamesFor(userID, projectID string) k8sNames {
	hash := pairHash(userID, projectID)
	return k8sNames{
		Pod:     "piforge-" + hash,
		Service: "piforge-" + hash,
		PVC:     "piforge-ws-" + hash,
		Secret:  "piforge-key-" + hash,
		Hash:    hash,
	}
}

// k8sNamesForID maps every id form this deployment hands around onto the names.
// It is NormalizeSandboxID for the Kubernetes provider: a trust boundary, because
// the id arrives from the store and from URL segments.
func k8sNamesForID(sandboxID string) k8sNames {
	pod := NormalizeSandboxID(sandboxID)
	hash := strings.TrimPrefix(pod, "piforge-")
	return k8sNames{
		Pod:     pod,
		Service: pod,
		PVC:     "piforge-ws-" + hash,
		Secret:  "piforge-key-" + hash,
		Hash:    hash,
	}
}

// K8sLabelValue renders an identifier as a legal Kubernetes label value.
//
// Kubernetes allows [A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9]) up to 63 characters.
// A user id is usually a UUID and passes through unchanged; anything else is
// mapped character by character, so a pair always produces a valid object instead
// of an API-server rejection that names a field nobody wrote.
func K8sLabelValue(raw string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, strings.TrimSpace(raw))
	cleaned = strings.Trim(cleaned, "-_.")
	if cleaned == "" {
		return "unknown"
	}
	if len(cleaned) > 63 {
		cleaned = strings.Trim(cleaned[:63], "-_.")
	}
	return cleaned
}

// k8sLabels is the label set every object of one sandbox carries.
func k8sLabels(names k8sNames, userID, projectID string) map[string]string {
	return map[string]string{
		K8sNameLabel:                  "piforge-sandbox",
		K8sManagedByLabel:             K8sManagedBy,
		K8sPartOfLabel:                "piforge",
		K8sSandboxLabel:               names.Hash,
		K8sUserLabel:                  K8sLabelValue(userID),
		K8sProjectLabel:               K8sLabelValue(projectID),
		"app.kubernetes.io/component": "sandbox",
	}
}

// k8sAnnotations keeps the ids verbatim beside the labels that had to sanitise
// them, so the object graph is still an audit record of which account it belongs
// to.
func k8sAnnotations(userID, projectID string) map[string]string {
	return map[string]string{
		K8sUserAnnotation:    userID,
		K8sProjectAnnotation: projectID,
	}
}

// k8sOwnerReferences makes the pod and the service follow their PVC? No: it is
// deliberately the other way round and deliberately absent.
//
// Nothing here sets an ownerReference. A Kubernetes garbage-collection edge from
// the PVC to the Pod would be *wrong* (deleting the volume should not be how a
// pod dies), and an edge from the Pod to the PVC would make Pause — which deletes
// the pod — take the workspace with it, which is the one thing Pause must not do.
// Destroy deletes the three objects in the order it names, so no cascade is
// needed and none can surprise an operator.

// k8sPod builds the Pod for one sandbox.
func (k *K8s) k8sPod(names k8sNames, userID, projectID string, withAPIKey bool, limits Limits) (*corev1.Pod, error) {
	resources, err := k8sResources(limits)
	if err != nil {
		return nil, err
	}
	mounts := []corev1.VolumeMount{{
		Name:      K8sPVCVolumeName,
		MountPath: containerWorkspace,
	}}
	volumes := []corev1.Volume{{
		Name: K8sPVCVolumeName,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: names.PVC,
			},
		},
	}}
	for _, dir := range k.writableDirs() {
		name := k8sVolumeName(dir)
		mounts = append(mounts, corev1.VolumeMount{Name: name, MountPath: dir})
		volumes = append(volumes, corev1.Volume{
			Name:         name,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
	}

	env := []corev1.EnvVar{
		// The image treats $WORKSPACE as "the workspace directory" and chowns it
		// at startup; its own default is /home/gem while this provider mounts the
		// project at /workspace. Same rule as the container provider: without it
		// the first `mkdir /workspace/repo` through the API fails.
		{Name: "WORKSPACE", Value: containerWorkspace},
	}
	if k.cfg.APIPort > 0 {
		// The image's own switch for starting Chromium without its own sandbox
		// (gem.sh folds it into BROWSER_EXTRA_ARGS). Inside gVisor the inner
		// sandbox cannot create its namespaces; the outer boundary is the one
		// this provider promises. Same value the container provider passes.
		env = append(env, corev1.EnvVar{Name: "BROWSER_NO_SANDBOX", Value: "--no-sandbox"})
	}
	if withAPIKey {
		// The key lives with the sandbox, in its own Secret: the control plane
		// reads it back off the cluster (apiKeyFor) instead of remembering it, so
		// a restarted process and a resumed sandbox both address the API with the
		// key it actually runs with. A Secret rather than a literal so that
		// `kubectl describe pod` — a thing operators paste into tickets — does not
		// print it; `get secret` can, which is why the RBAC grants that only to the
		// control plane's service account.
		env = append(env, corev1.EnvVar{
			Name: SandboxAPIKeyEnv,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: names.Secret},
					Key:                  K8sAPIKeySecretKey,
				},
			},
		})
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        names.Pod,
			Labels:      k8sLabels(names, userID, projectID),
			Annotations: k8sAnnotations(userID, projectID),
		},
		Spec: corev1.PodSpec{
			// One instance, and a crash restarts it in place with the same PVC —
			// the container provider's `--restart unless-stopped`, said in the
			// object the cluster reconciles.
			RestartPolicy: corev1.RestartPolicyAlways,
			// No API token in the sandbox. The agent has no business with the
			// Kubernetes API, and a mounted token would be a credential one
			// prompt-injection away from being used against its own namespace.
			AutomountServiceAccountToken: boolPtr(false),
			ServiceAccountName:           strings.TrimSpace(k.cfg.ServiceAccount),
			// Never inject `<service>_SERVICE_HOST` style variables: they leak the
			// shape of the namespace into a tenant's environment for no benefit.
			EnableServiceLinks: boolPtr(false),
			SecurityContext:    k.podSecurityContext(),
			ImagePullSecrets:   k.cfg.ImagePullSecrets,
			NodeSelector:       k.cfg.NodeSelector,
			Containers: []corev1.Container{{
				Name:            K8sContainerName,
				Image:           k.cfg.Image,
				ImagePullPolicy: k.pullPolicy(),
				WorkingDir:      containerWorkspace,
				// The same argv the container provider appends (docker run <image>
				// sleep infinity): it overrides the image's CMD, not its ENTRYPOINT, so
				// an image whose default command exits still yields a sandbox that stays
				// up between prompts — and an image whose entrypoint starts services
				// receives exactly the arguments it receives under Docker today.
				Args: []string{"sleep", "infinity"},
				Env:  env,
				Ports: []corev1.ContainerPort{{
					Name:          K8sAPIPortName,
					ContainerPort: int32(k.apiPort()),
					Protocol:      corev1.ProtocolTCP,
				}},
				VolumeMounts: mounts,
				Resources:    resources,
				SecurityContext: &corev1.SecurityContext{
					// The container-level half of the posture. Both of these are
					// the container provider's argv flags, named identically here:
					// `--security-opt no-new-privileges` and
					// `--cap-drop ALL --cap-add <the measured five>`.
					AllowPrivilegeEscalation: boolPtr(false),
					ReadOnlyRootFilesystem:   boolPtr(k.readOnlyRootFS()),
					Capabilities:             k8sCapabilities(),
				},
			}},
			Volumes:                       volumes,
			TerminationGracePeriodSeconds: int64Ptr(int64(k.graceStop().Seconds())),
		},
	}
	if class := strings.TrimSpace(k.cfg.RuntimeClass); class != "" {
		pod.Spec.RuntimeClassName = &class
	}
	return pod, nil
}

// podSecurityContext is the pod-level half of the posture: one non-root account
// for the whole pod, a seccomp profile the runtime enforces, and a group that
// owns the volume.
//
// RunAsUser defaults to 1000 — the account the sandbox image itself uses for
// everything it serves — so the agent, its shell and the file API are the same
// identity the container provider's services drop to. `fsGroup` is what makes the
// PVC writable for that account on a CSI driver that applies ownership.
func (k *K8s) podSecurityContext() *corev1.PodSecurityContext {
	out := &corev1.PodSecurityContext{
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	if k.cfg.AllowRootUser {
		// Explicitly asked for: an image whose entrypoint needs root for its own
		// bootstrap (a useradd/chown pass) cannot start under runAsNonRoot. The
		// consequence is named in docs/SANDBOX-K8S.md: the bootstrap runs as
		// root, and the dropped capability set plus the runtime class are what
		// constrain it.
		return out
	}
	uid := k.cfg.RunAsUser
	if uid <= 0 {
		uid = DefaultK8sRunAsUser
	}
	out.RunAsNonRoot = boolPtr(true)
	out.RunAsUser = int64Ptr(uid)
	out.RunAsGroup = int64Ptr(uid)
	out.FSGroup = int64Ptr(uid)
	return out
}

// k8sCapabilities is the container-level capability set: drop everything, add
// back exactly the five the image was measured to need.
//
// It reuses sandboxCapAdd rather than restating it, because the set is the result
// of a measurement against the image (see its comment) and two copies would drift
// the first time somebody adds a capability for a new image.
//
// With runAsUser 1000 these grants are inert — a non-root process holds no
// effective capabilities — and that is the honest description of the pair: the
// non-root account is the stronger half, and the add-back list is what keeps the
// same image working in the AllowRootUser shape, where the bootstrap needs them.
func k8sCapabilities() *corev1.Capabilities {
	out := &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}
	for _, capability := range sandboxCapAdd {
		out.Add = append(out.Add, corev1.Capability(capability))
	}
	return out
}

// k8sService builds the headless Service that gives the sandbox a stable name.
//
// Headless (ClusterIP: None) on purpose: the sandbox is one pod, so a virtual IP
// in front of it would add a hop and a load-balancing decision that cannot mean
// anything. What the control plane needs is a name that resolves to this pod's
// address, and that is exactly what `clusterIP: None` publishes.
func k8sService(names k8sNames, userID, projectID string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        names.Service,
			Labels:      k8sLabels(names, userID, projectID),
			Annotations: k8sAnnotations(userID, projectID),
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			// Only a pod of this sandbox answers: the selector is the pair hash,
			// not the deployment's name label.
			Selector: map[string]string{
				K8sSandboxLabel: names.Hash,
			},
			Ports: []corev1.ServicePort{{
				Name:       K8sAPIPortName,
				Port:       8080,
				TargetPort: intstr.FromString(K8sAPIPortName),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

// k8sPVC builds the per-sandbox volume that holds the workspace.
//
// ReadWriteOnce because the only writer is the sandbox's own pod: a shared
// ReadWriteMany volume would be a filesystem two tenants' pods could mount, which
// is the property this provider exists to avoid. The storage class and the size
// are the deployment's (`-sandbox-k8s-storage-class`, `-sandbox-k8s-disk`).
func (k *K8s) k8sPVC(names k8sNames, userID, projectID string) (*corev1.PersistentVolumeClaim, error) {
	size, err := resource.ParseQuantity(k.diskSize())
	if err != nil {
		return nil, fmt.Errorf("sandbox: k8s: disk size %q: %w", k.cfg.DiskSize, err)
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        names.PVC,
			Labels:      k8sLabels(names, userID, projectID),
			Annotations: k8sAnnotations(userID, projectID),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: size},
			},
		},
	}
	if class := strings.TrimSpace(k.cfg.StorageClass); class != "" {
		pvc.Spec.StorageClassName = &class
	}
	return pvc, nil
}

// k8sSecret builds the per-sandbox API key Secret.
//
// It is created once and never overwritten: Resume re-attaches an existing pod to
// the key its sandbox already uses, and rewriting it would invalidate the copy the
// control plane has already stored with the sandbox row (and the one the running
// agent was started with, for the moment before the pod is replaced).
func (k *K8s) k8sSecret(names k8sNames, userID, projectID, apiKey string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        names.Secret,
			Labels:      k8sLabels(names, userID, projectID),
			Annotations: k8sAnnotations(userID, projectID),
		},
		Type: corev1.SecretTypeOpaque,
		// Data, not StringData: the API server's own representation, and the one a
		// reader has to decode deliberately.
		Data: map[string][]byte{K8sAPIKeySecretKey: []byte(apiKey)},
	}
}

// k8sResources turns a tenant's Limits into the pod's requests and limits.
//
// The policy is the same one the container providers apply, in Docker's units
// ("2", "0.5", "2g"); Kubernetes wants its own quantities, and the conversion is
// the *unit table's*, not a second policy: mebibytes ("Mi") for a Docker memory
// value (whose `g`/`m` suffixes are powers of 1024), millicores for a CPU count.
// Requests equal limits, which is the shape a sandbox wants: a tenant that was
// promised 2 GiB gets a pod that is *scheduled* for 2 GiB, not one that competes
// for it after the node has already overcommitted.
//
// PID limits have no PodSpec field, so they are not applied here — the fork-bomb
// guard on Kubernetes is the image's own (node-level) or a policy the operator
// applies; docs/SANDBOX-K8S.md states that rather than implying parity.
func k8sResources(limits Limits) (corev1.ResourceRequirements, error) {
	out := corev1.ResourceRequirements{}
	if cpuMilli := ParseCPUMilli(limits.CPUs); cpuMilli > 0 {
		quantity, err := resource.ParseQuantity(strconv.Itoa(cpuMilli) + "m")
		if err != nil {
			return out, fmt.Errorf("sandbox: k8s: cpu limit %q: %w", limits.CPUs, err)
		}
		out.Requests = addResource(out.Requests, corev1.ResourceCPU, quantity)
		out.Limits = addResource(out.Limits, corev1.ResourceCPU, quantity)
	}
	if memMB, err := ParseMemMB(limits.Memory); err != nil {
		return out, fmt.Errorf("sandbox: k8s: %w", err)
	} else if memMB > 0 {
		quantity, qerr := resource.ParseQuantity(strconv.Itoa(memMB) + "Mi")
		if qerr != nil {
			return out, fmt.Errorf("sandbox: k8s: memory limit %q: %w", limits.Memory, qerr)
		}
		out.Requests = addResource(out.Requests, corev1.ResourceMemory, quantity)
		out.Limits = addResource(out.Limits, corev1.ResourceMemory, quantity)
	}
	return out, nil
}

func addResource(list corev1.ResourceList, name corev1.ResourceName, quantity resource.Quantity) corev1.ResourceList {
	if list == nil {
		list = corev1.ResourceList{}
	}
	list[name] = quantity
	return list
}

// appliedLimitsFromPod reads the limits a pod was created with, out of its own
// spec, so the sandbox row records a fact about the object rather than a
// re-derivation from the policy that may since have changed. It is the Kubernetes
// half of Docker.appliedLimits.
func appliedLimitsFromPod(pod *corev1.Pod) Limits {
	out := Limits{}
	if pod == nil {
		return out
	}
	for _, container := range pod.Spec.Containers {
		if container.Name != K8sContainerName {
			continue
		}
		if quantity, ok := container.Resources.Limits[corev1.ResourceCPU]; ok {
			out.CPUs = strconv.FormatFloat(quantity.AsApproximateFloat64(), 'f', -1, 64)
		}
		if quantity, ok := container.Resources.Limits[corev1.ResourceMemory]; ok {
			out.Memory = strconv.FormatInt(quantity.Value()/(1<<20), 10) + "m"
		}
	}
	return out
}

// podAPIKey is the Secret reference a sandbox pod carries, if it has one.
//
// The value is not in the pod: a paused sandbox has no pod at all, so the key has
// to live in an object that belongs to the sandbox for its whole life. What the
// pod carries is the reference, which is what a control plane that adopted the pod
// follows (apiKeyFor in k8s.go).
func podAPIKey(pod *corev1.Pod) *corev1.SecretKeySelector {
	if pod == nil {
		return nil
	}
	for _, container := range pod.Spec.Containers {
		if container.Name != K8sContainerName {
			continue
		}
		for _, env := range container.Env {
			if env.Name != SandboxAPIKeyEnv || env.ValueFrom == nil {
				continue
			}
			return env.ValueFrom.SecretKeyRef
		}
	}
	return nil
}

// podReady reports whether the pod's Ready condition is true.
func podReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// podFailure is the sentence an operator needs when a pod will not run: the
// container's termination reason/message, or the scheduling condition that kept
// it Pending.
func podFailure(pod *corev1.Pod) string {
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Waiting != nil && status.State.Waiting.Message != "" {
			return fmt.Sprintf("%s: %s", status.State.Waiting.Reason, status.State.Waiting.Message)
		}
		if status.State.Terminated != nil && status.State.Terminated.Message != "" {
			return fmt.Sprintf("%s: %s", status.State.Terminated.Reason, status.State.Terminated.Message)
		}
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Status == corev1.ConditionFalse && condition.Message != "" {
			return fmt.Sprintf("%s: %s", condition.Reason, condition.Message)
		}
	}
	if pod.Status.Message != "" {
		return pod.Status.Message
	}
	return "the pod is " + string(pod.Status.Phase)
}

// k8sVolumeName turns a mount path into a legal volume name ("/var/tmp" ->
// "var-tmp"). Deterministic, so the pod spec a test pins is the pod spec an
// operator reads in `kubectl describe`.
func k8sVolumeName(mountPath string) string {
	trimmed := strings.Trim(path.Clean("/"+mountPath), "/")
	if trimmed == "" {
		return "writable"
	}
	return K8sLabelValue(strings.ReplaceAll(trimmed, "/", "-"))
}

// k8sWritableDirsAreSorted keeps the emptyDir mounts in a stable order so the
// object graph is diffable (and a test's expectation is not map iteration).
func sortedDirs(dirs []string) []string {
	out := append([]string{}, dirs...)
	sort.Strings(out)
	return out
}

func boolPtr(v bool) *bool    { return &v }
func int64Ptr(v int64) *int64 { return &v }
