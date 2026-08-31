package main

import (
	"os"
	"slices"
	"testing"
)

func TestAllowedRecipientsFromEnvironment(t *testing.T) {
	t.Setenv("POLIS_ALLOWED_RECIPIENTS", `["alpha","beta"]`)
	recipients, err := allowedRecipientsFromEnvironment()
	if err != nil || recipients == nil || !slices.Equal(*recipients, []string{"alpha", "beta"}) {
		t.Fatalf("recipients = %#v, %v", recipients, err)
	}

	t.Setenv("POLIS_ALLOWED_RECIPIENTS", `[]`)
	recipients, err = allowedRecipientsFromEnvironment()
	if err != nil || recipients == nil || len(*recipients) != 0 {
		t.Fatalf("empty recipients = %#v, %v", recipients, err)
	}

	t.Setenv("POLIS_ALLOWED_RECIPIENTS", `not-json`)
	if _, err := allowedRecipientsFromEnvironment(); err == nil {
		t.Fatal("invalid policy was accepted")
	}

	if err := os.Unsetenv("POLIS_ALLOWED_RECIPIENTS"); err != nil {
		t.Fatal(err)
	}
	recipients, err = allowedRecipientsFromEnvironment()
	if err != nil || recipients != nil {
		t.Fatalf("omitted recipients = %#v, %v", recipients, err)
	}
}
