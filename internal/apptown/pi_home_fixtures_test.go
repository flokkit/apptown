package apptown

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
)

func fakeAgentLabels(labels map[string]string) docker.Client {
	return dockerClientFunc(func(_ context.Context, _ string, req docker.Command) ([]byte, error) {
		for label, value := range labels {
			if strings.Contains(strings.Join(req.Args, " "), "\""+label+"\"") {
				return []byte(value), nil
			}
		}
		return nil, nil
	})
}

func readSettings(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}
