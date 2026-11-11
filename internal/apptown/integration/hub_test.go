package integration

import (
	"strings"
	"testing"
)

func TestEveryProjectUsesTheOneHubWithoutAnAttachment(t *testing.T) {
	t.Parallel()
	s, id, _ := runtimeFixture(t)
	if got, err := s.Registry(t.Context(), id); err != nil || got != nil {
		t.Fatalf("no hub: %+v %v", got, err)
	}
	if _, err := s.Bindings.Upsert(t.Context(), "hub", CapContainerRegistry, "direct", `{"url":"registry.example","group":"team"}`, false); err != nil {
		t.Fatal(err)
	}
	got, err := s.Registry(t.Context(), id)
	if err != nil || got == nil || got.Repo != "registry.example/team/demo" {
		t.Fatalf("derived hub: %+v %v", got, err)
	}
	if _, err := s.Bindings.Upsert(t.Context(), "second", CapContainerRegistry, "direct", `{"url":"other.example"}`, false); err == nil || !strings.Contains(err.Error(), "one hub registry") {
		t.Fatalf("second hub: %v", err)
	}
	if err := s.Attachments.Attach(t.Context(), id, CapContainerRegistry, "hub", `{}`, "test", nil); err == nil {
		t.Fatal("project registry attachment admitted")
	}
}
