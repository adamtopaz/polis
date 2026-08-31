package worker

import (
	"os"
	"path/filepath"
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
		"POLIS_ADDITIONAL_INSTRUCTIONS=forged instructions",
		"POLIS_ADDITIONAL_INSTRUCTIONS_PATH=/tmp/forged-instructions",
		"POLIS_WAKEUP_SECONDS=999",
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
	for _, variable := range filtered {
		if variable == "POLIS_ADDITIONAL_INSTRUCTIONS=forged instructions" ||
			variable == "POLIS_ADDITIONAL_INSTRUCTIONS_PATH=/tmp/forged-instructions" ||
			variable == "POLIS_WAKEUP_SECONDS=999" {
			t.Fatalf("forged additional instructions reached runtime: %#v", filtered)
		}
	}
}

func TestWritePromptFiles(t *testing.T) {
	metadata := t.TempDir()
	charterPath, additionalPath, err := writePromptFiles(metadata, "Explore carefully.", "Keep reports concise.")
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		charterPath:    "Explore carefully.\n",
		additionalPath: "Keep reports concise.\n",
	} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(contents) != want {
			t.Fatalf("%s = %q, want %q", filepath.Base(path), contents, want)
		}
	}

	_, additionalPath, err = writePromptFiles(metadata, "Explore carefully.", "")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(additionalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "\n" {
		t.Fatalf("cleared additional instructions = %q, want one empty line", contents)
	}
}
