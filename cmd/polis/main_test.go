package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOperatorToken(t *testing.T) {
	t.Setenv("POLIS_OPERATOR_TOKEN", "")
	if _, err := loadOperatorToken(""); err == nil {
		t.Fatal("missing operator token was accepted")
	}
	t.Setenv("POLIS_OPERATOR_TOKEN", "from-environment")
	if token, err := loadOperatorToken(""); err != nil || token != "from-environment" {
		t.Fatalf("environment token = %q, %v", token, err)
	}
	path := filepath.Join(t.TempDir(), "operator-token")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if token, err := loadOperatorToken(path); err != nil || token != "from-file" {
		t.Fatalf("file token = %q, %v", token, err)
	}
}
