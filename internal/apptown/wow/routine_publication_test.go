package wow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

func routineMaterialPair() (routinePublication, routinePublication) {
	before := routinePublication{Kind: "block", ID: idstest.N(1), PublicationID: idstest.N(2), Version: 1, Name: "team/style", Source: SourceHere, Body: "Use descriptive names.", Checks: json.RawMessage(`[{"name":"test","run":"go test ./...","severity":"high"}]`)}
	after := before
	after.PublicationID = idstest.N(3)
	after.Version = 2
	after.Body = "Use descriptive names and explain error handling."
	return before, after
}

func TestRoutinePublicationOnlyChangesDisplayedGuidance(t *testing.T) {
	before, after := routineMaterialPair()
	if ok, reason := routinePublicationDelta(before, after); !ok || reason != "" {
		t.Fatalf("guidance update refused: %t %s", ok, reason)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*routinePublication)
	}{
		{"owner", func(p *routinePublication) { p.ID = idstest.N(9) }},
		{"source", func(p *routinePublication) { p.Source = "another-publisher" }},
		{"check removed", func(p *routinePublication) { p.Checks = json.RawMessage(`[]`) }},
		{"command weakened", func(p *routinePublication) {
			p.Checks = json.RawMessage(`[{"name":"test","run":"true","severity":"high"}]`)
		}},
		{"severity weakened", func(p *routinePublication) {
			p.Checks = json.RawMessage(`[{"name":"test","run":"go test ./...","severity":"low"}]`)
		}},
		{"workflow", func(p *routinePublication) { p.Kind = "workflow" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := after
			tc.mutate(&candidate)
			if ok, reason := routinePublicationDelta(before, candidate); ok || reason == "" {
				t.Fatalf("unsupported publication accepted: %t %s", ok, reason)
			}
		})
	}
	// Shipped release descriptions are provenance facts, not a requirement that
	// two different publications were signed by the same release description.
	before.Source, after.Source = SourceFlokkit, SourceFlokkit
	before.Signer, after.Signer = "shipped with old release, unsigned", "shipped with new release, unsigned"
	if ok, reason := routinePublicationDelta(before, after); !ok || reason != "" {
		t.Fatalf("recorded provenance transition refused: %t %s", ok, reason)
	}
	same := before
	same.Signer = "replacement"
	if ok, reason := routinePublicationDelta(before, same); ok || !strings.Contains(reason, "different material") {
		t.Fatalf("same publication changed: %t %s", ok, reason)
	}
}

func TestRoutinePackRequiresCompleteUnchangedExecutionAndAuthority(t *testing.T) {
	b, a := routineMaterialPair()
	before := routinePublication{Kind: "pack", ID: idstest.N(4), PublicationID: idstest.N(5), Version: 1, Name: "team", Source: SourceHere, Spec: json.RawMessage(`{"guidance":["team/style"],"requires":[]}`), Declaration: json.RawMessage(`{"guidance":["team/style"]}`), Pins: PinSet{Blocks: map[string]int{"team/style": 1}}, References: []routinePublication{b}}
	after := before
	after.PublicationID = idstest.N(6)
	after.Version = 2
	after.Pins = PinSet{Blocks: map[string]int{"team/style": 2}}
	after.References = []routinePublication{a}
	if ok, reason := routinePublicationDelta(before, after); !ok || reason != "" {
		t.Fatalf("pack guidance update refused: %t %s", ok, reason)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*routinePublication)
	}{
		{"new authority", func(p *routinePublication) {
			p.Spec = json.RawMessage(`{"guidance":["team/style"],"requires":["new-capability"]}`)
		}},
		{"new data service", func(p *routinePublication) {
			p.Spec = json.RawMessage(`{"guidance":["team/style"],"services":[{"capability":"postgres"}]}`)
		}},
		{"missing component", func(p *routinePublication) { p.References = nil }},
		{"changed policy", func(p *routinePublication) { p.Pins.Policies = map[string]string{"approval": "changed"} }},
		{"changed workflow", func(p *routinePublication) { p.Pins.Workflows = map[string]int{"land": 2} }},
		{"weakened child", func(p *routinePublication) {
			c := a
			c.Checks = json.RawMessage(`[]`)
			p.References = []routinePublication{c}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := after
			tc.mutate(&candidate)
			if ok, reason := routinePublicationDelta(before, candidate); ok || reason == "" {
				t.Fatalf("unsupported pack accepted: %t %s", ok, reason)
			}
		})
	}
}

func TestRoutinePublicationRejectsUnknownStoredSemantics(t *testing.T) {
	var spec PackSpec
	if err := routineJSON([]byte(`{"guidance":[],"newPermission":true}`), &spec); err == nil {
		t.Fatal("unknown publication field ignored")
	}
	if err := routineJSON([]byte(`{} {}`), &spec); err == nil {
		t.Fatal("trailing publication ignored")
	}
	// A publication from when a pack could declare memory is not routine:
	// its retired default is unknown semantics, and a person adopts it.
	if err := routineJSON([]byte(`{"guidance":[],"resources":{"memory":"48M"}}`), &spec); err == nil {
		t.Fatal("a retired pack memory default was read as routine")
	}
}
