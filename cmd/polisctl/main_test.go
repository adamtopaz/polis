package main

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adamtopaz/polis/internal/api"
	"github.com/adamtopaz/polis/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestHistoryCommand(t *testing.T) {
	previous := fetchLatestSession
	fetchLatestSession = func(_ context.Context, namespace, kubeContext, agentID string) (string, []byte, error) {
		if namespace != "agents" || kubeContext != "test-cluster" || agentID != "researcher" {
			t.Fatalf("history target = namespace %q, context %q, agent %q", namespace, kubeContext, agentID)
		}
		return "/workspace/.polis/pi-sessions/current.jsonl", []byte("" +
			`{"type":"session","id":"session-1","cwd":"/workspace"}` + "\n" +
			`{"type":"message","id":"one","message":{"role":"user","content":"first"}}` + "\n" +
			`{"type":"message","id":"two","message":{"role":"assistant","content":[{"type":"text","text":"second"}]}}` + "\n"), nil
	}
	t.Cleanup(func() { fetchLatestSession = previous })

	if err := run(context.Background(), []string{"history", "--namespace", "agents", "--context", "test-cluster", "--tail", "1", "researcher"}); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryCommandRejectsInvalidArguments(t *testing.T) {
	for _, command := range [][]string{
		{"history"},
		{"history", "UPPERCASE"},
		{"history", "--tail", "-1", "researcher"},
	} {
		if err := run(context.Background(), command); err == nil {
			t.Fatalf("polisctl %v succeeded", command)
		}
	}
}

func TestKubernetesRelaySelection(t *testing.T) {
	getenv := func(name string) string {
		if name == "POLIS_VIA_KUBERNETES" {
			return "true"
		}
		return ""
	}
	for _, test := range []struct {
		args []string
		want bool
	}{
		{args: []string{"agent", "list"}, want: true},
		{args: []string{"events", "researcher"}, want: true},
		{args: []string{"history", "researcher"}, want: false},
		{args: []string{"help"}, want: false},
		{args: nil, want: false},
	} {
		if got := useKubernetesRelay(getenv, test.args); got != test.want {
			t.Fatalf("useKubernetesRelay(%v) = %v, want %v", test.args, got, test.want)
		}
	}
}

func TestNewestPodSelectsNewestReadyPod(t *testing.T) {
	readyPod := func(name string, created time.Time, ready bool) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              name,
				Namespace:         "polis",
				Labels:            map[string]string{"polis.dev/agent": "researcher"},
				CreationTimestamp: metav1.NewTime(created),
			},
			Status: corev1.PodStatus{
				Phase:             corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{Name: "agent", Ready: ready}},
			},
		}
	}
	now := time.Now()
	clientset := fake.NewClientset(
		readyPod("old-ready", now.Add(-time.Minute), true),
		readyPod("new-ready", now, true),
		readyPod("newest-unready", now.Add(time.Minute), false),
	)

	pod, err := newestPod(context.Background(), clientset, "polis", "polis.dev/agent=researcher", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if pod != "new-ready" {
		t.Fatalf("newestPod = %q, want new-ready", pod)
	}
}

func TestNewestPodRequiresReadyContainer(t *testing.T) {
	clientset := fake.NewClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unready",
			Namespace: "polis",
			Labels:    map[string]string{"polis.dev/agent": "researcher"},
		},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{Name: "agent", Ready: false}},
		},
	})

	if _, err := newestPod(context.Background(), clientset, "polis", "polis.dev/agent=researcher", "agent"); err == nil {
		t.Fatal("newestPod selected an unready pod")
	}
}

func TestOperatorCommands(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "polis.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	server := httptest.NewServer(api.New(database, slog.New(slog.NewTextHandler(io.Discard, nil)), "operator-secret", "worker-secret").Handler())
	defer server.Close()

	t.Setenv("POLIS_URL", server.URL)
	t.Setenv("POLIS_OPERATOR_TOKEN", "operator-secret")
	t.Setenv("POLIS_OPERATOR_TOKEN_FILE", "")
	lease, err := database.Acquire("declared", "worker", 30*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Exit(lease.Token, "registered"); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"agent", "list"},
		{"agent", "get", "declared"},
		{"message", "declared", `{"goal":"work"}`},
		{"events", "declared"},
		{"agent", "state", "declared", "paused"},
	}
	for _, command := range commands {
		if err := run(context.Background(), command); err != nil {
			t.Fatalf("polisctl %v: %v", command, err)
		}
	}
}

func TestOperatorCLIRejectsAgentAndInfrastructureCommands(t *testing.T) {
	for _, command := range [][]string{
		{"inspect"},
		{"send", "alpha", `{}`},
		{"spawn"},
		{"server"},
		{"worker"},
	} {
		err := run(context.Background(), command)
		if err == nil || !strings.Contains(err.Error(), "operator control CLI") {
			t.Fatalf("non-operator command %v returned %v", command, err)
		}
	}
}

func TestOperatorCLIRejectsImperativeAgentCreation(t *testing.T) {
	for _, command := range [][]string{
		{"agent", "create", "--charter", "No longer supported.", "--runtime", `["runtime"]`},
		{"agent", "apply", "--id", "also-not-supported", "--charter", "No longer supported.", "--runtime", `["runtime"]`},
	} {
		err := run(context.Background(), command)
		if err == nil || !strings.Contains(err.Error(), "agent requires list") {
			t.Fatalf("imperative agent configuration %v returned %v", command, err)
		}
	}
}
