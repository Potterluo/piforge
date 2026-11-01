package sandbox

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// pods/exec, as a seam.
//
// Everything else in this provider goes through the typed clientset, which is why
// client-go's fake covers it (k8s_test.go drives Ensure/Get/Pause/Resume/Destroy/
// Endpoint/ContainerPath against that fake). pods/exec is the exception: it is not
// a JSON request but a hijacked SPDY stream, and the fake clientset has no
// transport for it — `Pods().Exec()` on a fake returns a request whose Do() has no
// reactor to answer it, and there is no way to make one stream.
//
// So the primitive is one small interface. The real implementation is the SPDY
// executor below (the same one `kubectl exec` uses); a test replaces it with a
// recorder, which is how Exec's argv/cwd/env and Attach's duplex wiring are proven
// without a cluster — and how the test says out loud that the *transport* is the
// only thing a cluster still has to prove (k8s_cluster_test.go names the command).

// k8sStreamer is the pods/exec primitive this provider needs.
type k8sStreamer interface {
	// Exec runs one argv in a container and wires the three streams. It blocks
	// until the process exits (or the stream fails), and returns the exit status
	// as an error when it is non-zero.
	Exec(ctx context.Context, namespace, pod string, opts k8sExecOptions) error
}

// spdyStreamer is the real one.
type spdyStreamer struct {
	client kubernetes.Interface
	rest   *rest.Config
	// timeout bounds the *request* that opens the stream. The stream itself is
	// bounded by the caller's context (a long-lived agent has none).
	timeout time.Duration
}

func (s *spdyStreamer) Exec(ctx context.Context, namespace, pod string, opts k8sExecOptions) error {
	if s == nil || s.client == nil || s.rest == nil {
		return fmt.Errorf("sandbox: k8s: no Kubernetes client for pods/exec")
	}
	request := s.client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: opts.Container,
			Command:   opts.Argv,
			Stdin:     opts.Stdin != nil,
			Stdout:    opts.Stdout != nil,
			Stderr:    opts.Stderr != nil && !opts.TTY,
			TTY:       opts.TTY,
		}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(s.rest, "POST", request.URL())
	if err != nil {
		return fmt.Errorf("sandbox: k8s: pods/exec %s/%s: %w", namespace, pod, err)
	}
	streamErr := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  opts.Stdin,
		Stdout: opts.Stdout,
		Stderr: opts.Stderr,
		Tty:    opts.TTY,
	})
	if streamErr != nil {
		// A non-zero exit is the process's answer, not a transport failure, and the
		// caller's error has to say which: Exec returns the same error shape Docker's
		// `docker exec` failure does (output + exit code), and Attach's stream simply
		// ends. remotecommand already renders it as "command terminated with exit
		// code N"; this is only here to keep the wrapping local.
		return streamErr
	}
	return nil
}
