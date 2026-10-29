package deployment

import (
	"context"
	"strings"
	"testing"
)

func TestQueuedPortainerReleaseRefusesMissingOrChangedBinding(t *testing.T) {
	for _, fixture := range []string{"legacy", "changed"} {
		t.Run(fixture, func(t *testing.T) {
			p, b, _ := newPortainerNeeds(t)
			target := p.target(t, IntranetTargetName)
			if _, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, "lan", target.ID, Attributes{Feed: "promotion", Audience: "organisation", DataClass: "non-production"}); err != nil {
				t.Fatal(err)
			}
			accepted, err := p.s.RequestRelease(t.Context(), DeploySelection{ProjectID: p.project, DeploymentName: "lan", BundleID: b.ID})
			if err != nil {
				t.Fatal(err)
			}
			// sql-guarantee: simulate a released pre-association request or stale recorded identity; admission never creates either today.
			statement := "UPDATE release_requests SET provider_binding_json=NULL WHERE release_id=$1"
			if fixture == "changed" {
				// sql-guarantee: simulate a queued request recorded for a different provider identity, unavailable through current admission.
				statement = "UPDATE release_requests SET provider_binding_json=jsonb_set(provider_binding_json,'{scope,daemonId}', '\"different-engine\"'::jsonb) WHERE release_id=$1"
			}
			if _, err := p.s.db.SQL().ExecContext(context.Background(), statement, accepted.ID); err != nil {
				t.Fatal(err)
			}
			err = p.s.ExecuteRelease(t.Context(), accepted.ID)
			if err == nil || !strings.Contains(err.Error(), "provider association") {
				t.Fatalf("unverified queued binding accepted: %v", err)
			}
			p.mu.Lock()
			effects := len(p.stacks)
			p.mu.Unlock()
			if effects != 0 {
				t.Fatalf("unverified queued request performed %d stack effects", effects)
			}
			release, err := p.s.Releases.Last(t.Context(), p.project, "lan")
			if err != nil || release.State != ReleaseFailed {
				t.Fatalf("refusal evidence: %+v %v", release, err)
			}
		})
	}
}
