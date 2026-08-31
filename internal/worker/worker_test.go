package worker

import (
	"slices"
	"testing"
)

func TestControlPlaneCredentialsAreNotPassedToAgentRuntime(t *testing.T) {
	environment := []string{
		"PATH=/bin",
		"POLIS_OPERATOR_TOKEN=operator-secret",
		"POLIS_OPERATOR_TOKEN_FILE=/run/secrets/operator-token",
		"POLIS_WORKER_TOKEN=worker-secret",
		"POLIS_WORKER_TOKEN_FILE=/run/secrets/worker-token",
		"POLIS_AGENT_ID=alpha",
		`POLIS_ALLOWED_RECIPIENTS=["beta"]`,
	}
	filtered := withoutControlPlaneCredentials(environment)
	for _, credential := range environment[1:5] {
		if slices.Contains(filtered, credential) {
			t.Fatalf("control-plane credential reached runtime: %#v", filtered)
		}
	}
	if !slices.Contains(filtered, "PATH=/bin") {
		t.Fatalf("ordinary environment was removed: %#v", filtered)
	}
	if slices.Contains(filtered, "POLIS_AGENT_ID=alpha") {
		t.Fatalf("supervisor identity reached runtime unchanged: %#v", filtered)
	}
	if slices.Contains(filtered, `POLIS_ALLOWED_RECIPIENTS=["beta"]`) {
		t.Fatalf("supervisor messaging policy reached runtime unchanged: %#v", filtered)
	}
}
