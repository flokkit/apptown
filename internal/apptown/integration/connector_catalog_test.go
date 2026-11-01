package integration

import (
	"strings"
	"testing"
)

// The catalog's suggested URLs come from the instance, never from code.
func TestPresetURLsComeFromCatalogDefaults(t *testing.T) {
	t.Parallel()
	var reg ConnectorSpec
	for _, c := range ConnectorCatalog() {
		if c.Capability == "registry" {
			reg = c
		}
	}
	if len(reg.Presets) == 0 {
		t.Fatal("no registry presets")
	}
	defaults := catalogDefaults{}
	for _, p := range InstancePresets(reg, defaults).Presets {
		if u, _ := p.Config["url"].(string); u != "" {
			t.Fatalf("with no mirror configured, preset %s must suggest no URL, got %q", p.Name, u)
		}
	}
	defaults = catalogDefaults{MirrorBase: "https://mirror.example.com/"}
	for _, p := range InstancePresets(reg, defaults).Presets {
		u, _ := p.Config["url"].(string)
		if u == "" || u[:len("https://mirror.example.com/repository/")] != "https://mirror.example.com/repository/" {
			t.Fatalf("preset %s: %q", p.Name, u)
		}
	}
}

// The mirror suggestion is the address of a mirror already added whose URL
// is that address plus its own preset's path; a mirror elsewhere suggests
// nothing, and the URL an administrator gives a mirror is what is suggested.
func TestTheMirrorSuggestionComesFromAMirrorAlreadyAdded(t *testing.T) {
	t.Parallel()
	s := NewService(testDB(t), Dependencies{})
	save := func(name, format, url string) {
		t.Helper()
		if _, err := s.Bindings.Save(t.Context(), name, CapRegistry, "registry", `{"format":"`+format+`","url":"`+url+`"}`, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.CatalogDefaults(t.Context()); got.MirrorBase != "" {
		t.Fatalf("no mirror suggested %q", got.MirrorBase)
	}
	save("a-docker", "docker-hub", "http://nexus.example.com:8082")
	save("b-npm", "npm", "https://elsewhere.example.com/npm/")
	if got := s.CatalogDefaults(t.Context()); got.MirrorBase != "" {
		t.Fatalf("mirrors off their preset paths suggested %q", got.MirrorBase)
	}
	save("c-go", "go-proxy", "https://nexus.example.com/repository/go-proxy")
	if got := s.CatalogDefaults(t.Context()); got.MirrorBase != "https://nexus.example.com" {
		t.Fatalf("suggestion %q, want https://nexus.example.com", got.MirrorBase)
	}
	save("c-go", "go-proxy", "https://mirror.example.org/repository/go-proxy/")
	if got := s.CatalogDefaults(t.Context()); got.MirrorBase != "https://mirror.example.org" {
		t.Fatalf("after the edit the suggestion is %q", got.MirrorBase)
	}
}

func TestServiceConnectorSpecDerivation(t *testing.T) {
	t.Parallel()
	k := &Definition{Capability: "mailer", Title: "Mail Relay", Description: "sends mail", Icon: "mail"}
	k.Connector.Fields = []Field{
		{Key: "host", Label: "Host", Help: "smtp", Placeholder: "smtp.example"},
		{Key: "token", Label: "Token", Type: "secretRef"},
	}
	spec := ServiceConnectorSpec(k)
	if spec.Capability != "mailer" || spec.Provider != "service" || !spec.Multi {
		t.Fatalf("derived spec basics: %+v", spec)
	}
	if !strings.Contains(spec.Description, "runtime-defined service kind") {
		t.Fatalf("description not annotated: %q", spec.Description)
	}
	if len(spec.Fields) != 2 {
		t.Fatalf("field count: %+v", spec.Fields)
	}
	// A field with no explicit type defaults to string; secretRef is preserved.
	if spec.Fields[0].Type != "string" || spec.Fields[1].Type != "secretRef" {
		t.Fatalf("field types: %+v", spec.Fields)
	}
}
