package worker

import (
	"slices"
	"testing"
)

func TestOperatorCredentialsAreNotPassedToAgentRuntime(t *testing.T) {
	environment := []string{
		"PATH=/bin",
		"POLIS_OPERATOR_TOKEN=operator-secret",
		"POLIS_OPERATOR_TOKEN_FILE=/run/secrets/operator-token",
		"POLIS_AGENT_ID=alpha",
	}
	filtered := withoutOperatorCredentials(environment)
	if slices.Contains(filtered, environment[1]) || slices.Contains(filtered, environment[2]) {
		t.Fatalf("operator credentials reached runtime: %#v", filtered)
	}
	if !slices.Contains(filtered, "PATH=/bin") || !slices.Contains(filtered, "POLIS_AGENT_ID=alpha") {
		t.Fatalf("ordinary environment was removed: %#v", filtered)
	}
}
