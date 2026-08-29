package token

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Setenv("TEST_POLIS_TOKEN", "")
	if _, err := Load("TEST_POLIS_TOKEN", "", "test"); err == nil {
		t.Fatal("missing token was accepted")
	}
	t.Setenv("TEST_POLIS_TOKEN", "from-environment")
	if value, err := Load("TEST_POLIS_TOKEN", "", "test"); err != nil || value != "from-environment" {
		t.Fatalf("environment token = %q, %v", value, err)
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if value, err := Load("TEST_POLIS_TOKEN", path, "test"); err != nil || value != "from-file" {
		t.Fatalf("file token = %q, %v", value, err)
	}
}
