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

// imageInspectAnswer is what `docker image inspect` answers the image probe
// (execution.InspectImage) for an image carrying labels.
func imageInspectAnswer(labels map[string]string) string {
	body, _ := json.Marshal(labels)
	return `"sha256:` + strings.Repeat("a", 64) + `" ` + string(body)
}

func fakeAgentLabels(labels map[string]string) docker.Client {
	return dockerClientFunc(func(_ context.Context, _ string, req docker.Command) ([]byte, error) {
		if strings.HasPrefix(strings.Join(req.Args, " "), "image inspect") {
			return []byte(imageInspectAnswer(labels)), nil
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
