package diag

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// PodRef identifies a specific pod and optional container target for diagnostic execution.
type PodRef struct {
	Namespace string
	Name      string
	Container string
}

func (p PodRef) String() string {
	if p.Container != "" {
		return fmt.Sprintf("%s/%s[%s]", p.Namespace, p.Name, p.Container)
	}
	return fmt.Sprintf("%s/%s", p.Namespace, p.Name)
}

// Executor defines the interface for running privileged read-only diagnostic commands
// inside Kube-OVN/OVS daemon pods.
type Executor interface {
	Exec(ctx context.Context, pod PodRef, cmd []string) (stdout, stderr []byte, err error)
	Stream(ctx context.Context, pod PodRef, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error
}

// KubeExecutor implements Executor using client-go remotecommand (SPDY/WebSocket).
type KubeExecutor struct {
	client kubernetes.Interface
	config *rest.Config
}

// NewKubeExecutor creates a new KubeExecutor using a Kubernetes client and rest config.
func NewKubeExecutor(client kubernetes.Interface, config *rest.Config) *KubeExecutor {
	return &KubeExecutor{
		client: client,
		config: config,
	}
}

// Exec executes a typed command and buffers stdout and stderr into byte slices.
func (e *KubeExecutor) Exec(ctx context.Context, pod PodRef, cmd []string) ([]byte, []byte, error) {
	var stdoutBuf, stderrBuf bytes.Buffer
	err := e.Stream(ctx, pod, cmd, nil, &stdoutBuf, &stderrBuf)
	return stdoutBuf.Bytes(), stderrBuf.Bytes(), err
}

// Stream executes a typed command and streams I/O to the provided readers and writers.
func (e *KubeExecutor) Stream(ctx context.Context, pod PodRef, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(cmd) == 0 {
		return fmt.Errorf("empty command")
	}

	req := e.client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod.Name).
		Namespace(pod.Namespace).
		SubResource("exec")

	req.VersionedParams(&corev1.PodExecOptions{
		Container: pod.Container,
		Command:   cmd,
		Stdin:     stdin != nil,
		Stdout:    stdout != nil,
		Stderr:    stderr != nil,
		TTY:       false,
	}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(e.config, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("failed to create SPDY executor for %s: %w", pod, err)
	}

	streamOptions := remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Tty:    false,
	}

	if err := exec.StreamWithContext(ctx, streamOptions); err != nil {
		return fmt.Errorf("exec error in %s (cmd=%v): %w", pod, cmd[0], err)
	}

	return nil
}

// MockExecCall records an execution invocation for testing.
type MockExecCall struct {
	Pod PodRef
	Cmd []string
}

// MockExecutor provides a controllable Executor implementation for unit testing.
type MockExecutor struct {
	Calls       []MockExecCall
	StdoutMap   map[string][]byte
	StderrMap   map[string][]byte
	ErrMap      map[string]error
	DefaultOut  []byte
	DefaultErr  []byte
	DefaultFail error
}

// NewMockExecutor initializes an empty MockExecutor.
func NewMockExecutor() *MockExecutor {
	return &MockExecutor{
		StdoutMap: make(map[string][]byte),
		StderrMap: make(map[string][]byte),
		ErrMap:    make(map[string]error),
	}
}

// Exec simulates executing a command and returning predefined outputs.
func (m *MockExecutor) Exec(ctx context.Context, pod PodRef, cmd []string) ([]byte, []byte, error) {
	m.Calls = append(m.Calls, MockExecCall{Pod: pod, Cmd: cmd})

	fullKey := strings.Join(cmd, " ")
	key := ""
	if len(cmd) > 0 {
		key = cmd[0]
	}

	if err, ok := m.ErrMap[fullKey]; ok && err != nil {
		return nil, m.StderrMap[fullKey], err
	}
	if err, ok := m.ErrMap[key]; ok && err != nil {
		return nil, m.StderrMap[key], err
	}
	if m.DefaultFail != nil {
		return nil, m.DefaultErr, m.DefaultFail
	}

	out := m.DefaultOut
	if customOut, ok := m.StdoutMap[fullKey]; ok {
		out = customOut
	} else if customOut, ok := m.StdoutMap[key]; ok {
		out = customOut
	}

	errOut := m.DefaultErr
	if customErr, ok := m.StderrMap[fullKey]; ok {
		errOut = customErr
	} else if customErr, ok := m.StderrMap[key]; ok {
		errOut = customErr
	}

	return out, errOut, nil
}

// Stream simulates streaming I/O using the predefined outputs.
func (m *MockExecutor) Stream(ctx context.Context, pod PodRef, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	out, errOut, err := m.Exec(ctx, pod, cmd)
	if stdout != nil && len(out) > 0 {
		_, _ = stdout.Write(out)
	}
	if stderr != nil && len(errOut) > 0 {
		_, _ = stderr.Write(errOut)
	}
	return err
}
