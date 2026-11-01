package deployment

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

func TestAppliedNeedSizeReplaysExactCommandAndCap(t *testing.T) {
	base := &appcontract.Contract{Needs: []string{"mariadb"}, NeedSizes: map[string]appcontract.NeedSize{"mariadb": {Name: "small"}}}
	resolved := *base
	resolved.NeedSizes = map[string]appcontract.NeedSize{"mariadb": {Name: "small", Command: []string{"--captured"}}}
	resolved.NeedResources = map[string]appcontract.Resources{"mariadb": {MemoryBytes: 128 << 20}}
	applied := appliedOf(&resolved, base, "preview", appcontract.ProfileChoice("declared"), nil, map[string]bool{"mariadb": true})
	raw, err := json.Marshal(applied)
	if err != nil {
		t.Fatal(err)
	}
	var saved AppliedProfile
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	replay := saved.On(base)
	if !reflect.DeepEqual(replay.NeedSizes, resolved.NeedSizes) || !reflect.DeepEqual(replay.NeedResources, resolved.NeedResources) {
		t.Fatalf("snapshot lost: %s", raw)
	}
	replay.NeedSizes["mariadb"].Command[0] = "mutated"
	if saved.Parts["mariadb"].Size.Command[0] != "--captured" {
		t.Fatal("snapshot changed")
	}
	old := AppliedProfile{Profile: "default", Parts: map[string]AppliedPart{"mariadb": {Need: true}}}
	if len(old.On(base).NeedSizes) != 0 {
		t.Fatal("historical ignored selector reinterpreted")
	}
}

func TestPreRecordReplayDoesNotReinterpretAnIgnoredSize(t *testing.T) {
	c := &appcontract.Contract{Needs: []string{"mariadb"}, NeedSizes: map[string]appcontract.NeedSize{"mariadb": {Name: "small"}}, NeedResources: map[string]appcontract.Resources{"mariadb": {MemoryBytes: 256 << 20}}}
	out := (AppliedProfile{}).On(c)
	if len(out.NeedSizes) != 0 {
		t.Fatal("pre-record replay retained a newly meaningful source selector")
	}
	if out.NeedResources["mariadb"].MemoryBytes != 256<<20 || len(c.NeedSizes) != 1 {
		t.Fatal("legacy resource behavior or source changed")
	}
}
func TestPreRecordRebuildDoesNotReinterpretAnIgnoredSize(t *testing.T) {
	t.Parallel()
	w := newDigestWorldWith(t, "", `app:
  web: {static: site, public: true, ports: ["8080"]}
needs:
  mariadb: {size: small}
`, nil)
	art, cleanup, err := docker.PrepareSource(t.Context(), w.repo, w.revision, "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	dep, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "legacy", w.lab.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	// Model a pre-profile generation through outcome admission, rather than
	// deploy today's contract and then falsify its recorded applied evidence.
	inputs := Inputs{Revision: art.Source.Revision, ArtifactHash: art.Hash, DockerSource: string(art.Body)}
	at, _, err := w.s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: w.lab.ID, Key: "legacy", Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Outcomes.Complete(t.Context(), deploymentCompletionFor(at, w.names(t, "legacy").App())); err != nil {
		t.Fatal(err)
	}
	if out, err := w.rebuild(t, dep); err != nil || out["skipped"] != nil {
		t.Fatalf("legacy rebuild: %v %v", out, err)
	}
	command := ranWith(t, w, "lab", w.names(t, "legacy").Service("mariadb"), 0)
	if !strings.Contains(command, "--innodb-buffer-pool-size=64M") || strings.Contains(command, "--innodb-buffer-pool-size=32M") {
		t.Fatalf("legacy source was reinterpreted: %s", command)
	}
}
