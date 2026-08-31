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

func TestWakeupSecondsFromEnvironment(t *testing.T) {
	if err := os.Unsetenv("POLIS_WAKEUP_SECONDS"); err != nil {
		t.Fatal(err)
	}
	wakeup, err := wakeupSecondsFromEnvironment()
	if err != nil || wakeup != nil {
		t.Fatalf("omitted wakeup = %#v, %v", wakeup, err)
	}

	t.Setenv("POLIS_WAKEUP_SECONDS", "120")
	wakeup, err = wakeupSecondsFromEnvironment()
	if err != nil || wakeup == nil || *wakeup != 120 {
		t.Fatalf("configured wakeup = %#v, %v", wakeup, err)
	}

	for _, value := range []string{"", "0", "-1", "1.5", "later"} {
		t.Setenv("POLIS_WAKEUP_SECONDS", value)
		if _, err := wakeupSecondsFromEnvironment(); err == nil {
			t.Fatalf("invalid wakeup %q was accepted", value)
		}
	}
}
