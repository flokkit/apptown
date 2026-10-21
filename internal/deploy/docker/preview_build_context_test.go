package docker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/appcontract"
)

func TestPreviewBuildsUseEachDeclaredContextAndRecordActualImages(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	ws := t.TempDir()
	for _, dir := range []string{"web", "worker", "migration"} {
		if err := os.Mkdir(filepath.Join(ws, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, dir, "Dockerfile"), []byte("FROM scratch\n# "+dir), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c, err := appcontract.Parse([]byte(`app:
  web: {build: web, public: true}
  worker: {build: worker}
init:
  - {name: same-worker, build: worker}
  - {name: migrate, build: migration}
`))
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	plan := appcontract.Prepare(c, nil)
	if err := PreparePreviewBuilds(t.Context(), Scope{Client: client}, Current, ws, "apptown-preview-test", plan); err != nil {
		t.Fatal(err)
	}
	var builds [][]string
	for _, args := range client.args {
		if len(args) > 0 && args[0] == "build" {
			builds = append(builds, args)
		}
	}
	if len(builds) != 3 {
		t.Fatalf("expected one build per distinct context: %v", builds)
	}
	for _, dir := range []string{"web", "worker", "migration"} {
		found := false
		for _, args := range builds {
			if strings.HasSuffix(strings.Join(args, " "), " "+filepath.Join(ws, dir)) {
				found = true
			}
		}
		if !found {
			t.Fatalf("did not build context %s: %v", dir, builds)
		}
	}
	if plan.Parts[0].Image == plan.Parts[1].Image || plan.Parts[1].Image != plan.Init[0].Image || plan.Init[1].Image == plan.Parts[0].Image {
		t.Fatalf("wrong image sharing: %+v %+v", plan.Parts, plan.Init)
	}
	after, err := json.Marshal(c)
	if err != nil || string(after) != string(before) {
		t.Fatal("building mutated source declaration")
	}
}
