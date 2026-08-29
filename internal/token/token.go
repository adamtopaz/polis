package token

import (
	"fmt"
	"os"
	"strings"
)

func Load(environmentName, path, role string) (string, error) {
	value := strings.TrimSpace(os.Getenv(environmentName))
	if path != "" {
		contents, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s token: %w", role, err)
		}
		value = strings.TrimSpace(string(contents))
	}
	if value == "" {
		return "", fmt.Errorf("%s token required: set %s or its token file", role, environmentName)
	}
	return value, nil
}
