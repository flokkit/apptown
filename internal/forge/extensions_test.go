package forge

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOptInExtensions(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{DockerClient: fakeAgentLabels(map[string]string{"forge.pi-lsp.version": "0.49.6", "forge.pi-subagents.version": "0.61.0"})})
	testAdministrator(t, a, "bls")
	pid := plainTestProject(t, a.DB, "p", "P")

	packages := func() []string {
		t.Helper()
		dir := t.TempDir()
		if err := piHomeFor(a).StageAgentDir(t.Context(), dir, pid); err != nil {
			t.Fatal(err)
		}
		var enabled []string
		for _, entry := range readSettings(t, dir)["packages"].([]any) {
			ref := entry.(string)
			if !strings.HasPrefix(ref, "npm:pi-subagents@") {
				enabled = append(enabled, ref)
			}
		}
		return enabled
	}

	// Off by default.
	if pkgs := packages(); len(pkgs) != 0 {
		t.Fatalf("no extensions should be enabled by default, got %v", pkgs)
	}

	// Enable lsp → resolves to the baked-version npm ref.
	h := a.Handler()
	if w := authzCall(h, "bls", "POST", "/api/v1/projects/p/extensions", `{"name":"lsp","enabled":true}`); w.Code != 200 {
		t.Fatalf("enable: %d %s", w.Code, w.Body.String())
	}
	w := authzCall(h, "bls", "GET", "/api/v1/projects/p/extensions", "")
	var view struct {
		Extensions []struct {
			Name, Package, Version string
			Enabled                bool
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || w.Code != 200 || len(view.Extensions) != 1 {
		t.Fatalf("catalog: %d %s %v", w.Code, w.Body.String(), err)
	}
	ext := view.Extensions[0]
	if ext.Name != "lsp" || ext.Package != "@narumitw/pi-lsp" || ext.Version != "0.49.6" || !ext.Enabled {
		t.Fatalf("catalog entry: %+v", ext)
	}
	if w := authzCall(h, "bls", "POST", "/api/v1/projects/p/extensions", `{"name":"arbitrary-code","enabled":true}`); w.Code != 422 {
		t.Fatalf("unknown extension: %d", w.Code)
	}
	pkgs := packages()
	if len(pkgs) != 1 || pkgs[0] != "npm:@narumitw/pi-lsp@0.49.6" {
		t.Fatalf("lsp should resolve to the baked ref, got %v", pkgs)
	}

	// An unknown extension name is ignored (never a dangling package ref).
	_ = a.Adoption.SetExtension(t.Context(), pid, "not-a-real-ext", true)
	if got := packages(); len(got) != 1 || got[0] != "npm:@narumitw/pi-lsp@0.49.6" {
		t.Fatalf("only blessed extensions resolve, got %v", got)
	}

	// Disable.
	_ = a.Adoption.SetExtension(t.Context(), pid, "lsp", false)
	if pkgs := packages(); len(pkgs) != 0 {
		t.Fatalf("disabled extension must drop out, got %v", pkgs)
	}
}
