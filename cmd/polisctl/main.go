package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/adamtopaz/polis/internal/client"
	"github.com/adamtopaz/polis/internal/model"
	"github.com/adamtopaz/polis/internal/sessionhistory"
	"github.com/adamtopaz/polis/internal/token"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

var fetchLatestSession = kubernetesLatestSession

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	args := os.Args[1:]
	var err error
	if useKubernetesRelay(os.Getenv, args) {
		err = runViaKubernetes(ctx, args, os.Stdout, os.Stderr)
	} else {
		err = run(ctx, args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "polisctl:", err)
		os.Exit(1)
	}
}

func useKubernetesRelay(getenv func(string) string, args []string) bool {
	if len(args) == 0 || args[0] == "history" || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		return false
	}
	value := strings.ToLower(strings.TrimSpace(getenv("POLIS_VIA_KUBERNETES")))
	return value == "1" || value == "true" || value == "yes"
}

func runViaKubernetes(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	namespace := env("POLIS_KUBERNETES_NAMESPACE", "polis")
	config, err := kubernetesConfig(os.Getenv("POLIS_KUBERNETES_CONTEXT"))
	if err != nil {
		return fmt.Errorf("load Kubernetes configuration: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create Kubernetes client: %w", err)
	}
	pod, err := newestPod(ctx, clientset, namespace, "app.kubernetes.io/component=mailbox", "mailbox")
	if err != nil {
		return fmt.Errorf("find mailbox pod: %w", err)
	}
	command := append([]string{"/bin/polisctl"}, args...)
	return execInPod(ctx, config, clientset, namespace, pod, "mailbox", nil, stdout, stderr, command...)
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "agent":
		return runAgent(ctx, args[1:])
	case "message":
		return runMessage(ctx, args[1:])
	case "events":
		return runEvents(ctx, args[1:])
	case "history":
		return runHistory(ctx, args[1:])
	case "help", "-h", "--help":
		fmt.Print(usage())
		return nil
	default:
		return usageError()
	}
}

func runHistory(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("history", flag.ContinueOnError)
	namespace := flags.String("namespace", env("POLIS_KUBERNETES_NAMESPACE", "polis"), "Kubernetes namespace")
	kubeContext := flags.String("context", "", "Kubernetes context (defaults to the current context)")
	tail := flags.Int("tail", 0, "return only the last N session entries (zero returns all)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("history requires an agent id")
	}
	if *tail < 0 {
		return errors.New("tail must not be negative")
	}
	agentID := flags.Arg(0)
	if problems := validation.IsDNS1123Label(agentID); len(problems) > 0 || len(agentID) > 50 {
		return errors.New("agent id must be a DNS label of at most 50 characters")
	}
	sessionFile, contents, err := fetchLatestSession(ctx, *namespace, *kubeContext, agentID)
	if err != nil {
		return err
	}
	history, err := sessionhistory.Decode(agentID, sessionFile, contents, *tail)
	return printJSON(history, err)
}

func kubernetesLatestSession(ctx context.Context, namespace, kubeContext, agentID string) (string, []byte, error) {
	config, err := kubernetesConfig(kubeContext)
	if err != nil {
		return "", nil, fmt.Errorf("load Kubernetes configuration: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return "", nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	pod, err := newestPod(ctx, clientset, namespace, "app.kubernetes.io/component=agent,polis.dev/agent="+agentID, "agent")
	if err != nil {
		return "", nil, fmt.Errorf("find agent pod: %w", err)
	}
	const sessionDirectory = "/workspace/.polis/pi-sessions"
	output, err := execInAgentPod(ctx, config, clientset, namespace, pod,
		"/bin/find", sessionDirectory, "-maxdepth", "1", "-type", "f", "-name", "*.jsonl", "-print")
	if err != nil {
		return "", nil, fmt.Errorf("list agent sessions: %w", err)
	}
	files := strings.Fields(string(output))
	if len(files) == 0 {
		return "", nil, fmt.Errorf("agent %q has no persisted Pi session", agentID)
	}
	sort.Strings(files)
	sessionFile := files[len(files)-1]
	contents, err := execInAgentPod(ctx, config, clientset, namespace, pod, "/bin/cat", sessionFile)
	if err != nil {
		return "", nil, fmt.Errorf("read agent session: %w", err)
	}
	return sessionFile, contents, nil
}

func newestPod(ctx context.Context, clientset kubernetes.Interface, namespace, selector, container string) (string, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return "", err
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pod in namespace %q matching %q", namespace, selector)
	}
	sort.Slice(pods.Items, func(i, j int) bool {
		return pods.Items[i].CreationTimestamp.After(pods.Items[j].CreationTimestamp.Time)
	})
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == container && status.Ready {
				return pod.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no ready pod in namespace %q matching %q", namespace, selector)
}

func kubernetesConfig(contextName string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{CurrentContext: contextName}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
}

func execInAgentPod(
	ctx context.Context,
	config *rest.Config,
	clientset kubernetes.Interface,
	namespace, pod string,
	command ...string,
) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	err := execInPod(ctx, config, clientset, namespace, pod, "agent", nil, &stdout, &stderr, command...)
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, errors.New(detail)
	}
	return stdout.Bytes(), nil
}

func execInPod(
	ctx context.Context,
	config *rest.Config,
	clientset kubernetes.Interface,
	namespace, pod, container string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	command ...string,
) error {
	request := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdin:     stdin != nil,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(config, "POST", request.URL())
	if err != nil {
		return err
	}
	return executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Stderr: stderr})
}

func runAgent(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("agent requires list, get, or state")
	}
	switch args[0] {
	case "list":
		flags := flag.NewFlagSet("agent list", flag.ContinueOnError)
		url := flags.String("url", env("POLIS_URL", "http://localhost:8080"), "mailbox URL")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		api, err := operatorClient(*url)
		if err != nil {
			return err
		}
		agents, err := api.ListAgents(ctx)
		return printJSON(map[string]any{"items": agents}, err)
	case "get":
		flags := flag.NewFlagSet("agent get", flag.ContinueOnError)
		url := flags.String("url", env("POLIS_URL", "http://localhost:8080"), "mailbox URL")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("agent get requires an agent id")
		}
		api, err := operatorClient(*url)
		if err != nil {
			return err
		}
		agent, err := api.GetAgent(ctx, flags.Arg(0))
		return printJSON(agent, err)
	case "state":
		flags := flag.NewFlagSet("agent state", flag.ContinueOnError)
		url := flags.String("url", env("POLIS_URL", "http://localhost:8080"), "mailbox URL")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 2 {
			return errors.New("agent state requires an agent id and active, paused, or terminated")
		}
		api, err := operatorClient(*url)
		if err != nil {
			return err
		}
		agent, err := api.SetState(ctx, flags.Arg(0), model.State(flags.Arg(1)))
		return printJSON(agent, err)
	default:
		return errors.New("agent requires list, get, or state")
	}
}

func runMessage(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("message", flag.ContinueOnError)
	url := flags.String("url", env("POLIS_URL", "http://localhost:8080"), "mailbox URL")
	sender := flags.String("sender", "operator", "sender label")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return errors.New("message requires an agent id and a JSON body")
	}
	body := json.RawMessage(flags.Arg(1))
	if !json.Valid(body) {
		return errors.New("message body must be valid JSON")
	}
	api, err := operatorClient(*url)
	if err != nil {
		return err
	}
	message, err := api.SendControlMessage(ctx, flags.Arg(0), *sender, body)
	return printJSON(message, err)
}

func runEvents(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("events", flag.ContinueOnError)
	url := flags.String("url", env("POLIS_URL", "http://localhost:8080"), "mailbox URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("events accepts at most one agent id")
	}
	id := ""
	if flags.NArg() == 1 {
		id = flags.Arg(0)
	}
	api, err := operatorClient(*url)
	if err != nil {
		return err
	}
	events, err := api.Events(ctx, id)
	return printJSON(map[string]any{"items": events}, err)
}

func operatorClient(url string) (*client.Client, error) {
	operatorToken, err := token.Load("POLIS_OPERATOR_TOKEN", os.Getenv("POLIS_OPERATOR_TOKEN_FILE"), "operator")
	if err != nil {
		return nil, err
	}
	return client.NewOperator(url, operatorToken), nil
}

func printJSON(value any, err error) error {
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func usageError() error {
	return errors.New(strings.TrimSpace(usage()))
}

func usage() string {
	return `Polisctl is the operator control CLI for a Polis fleet.

Usage:
  polisctl agent list
  polisctl agent get ID
  polisctl agent state ID active|paused|terminated
  polisctl message ID JSON
  polisctl events [ID]
  polisctl history [--namespace NAMESPACE] [--context CONTEXT] [--tail N] ID
`
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
