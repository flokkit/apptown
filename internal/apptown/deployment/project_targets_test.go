package deployment

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/project"
)

// Availability must explain offered refusals without revealing unoffered targets
// or applying the wizard's fixed classification to an existing project.
func TestProjectTargetsExplainsOnlyOfferedTargets(t *testing.T) {
	t.Parallel()
	s, _, in, full := configurationFixture(t)
	ctx := t.Context()
	configureForTest(t, s, in)
	if err := s.Targets.Offer(ctx, full, "*", OfferCaps{MaxStanding: 1}, "setup"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"unavailable", "hidden", "public"} {
		status := TargetReady
		if name != "public" {
			status = TargetUnreachable
		}
		target, err := s.Targets.Put(ctx, &Target{Name: name, Kind: KindDockerHost, Origin: OriginAttached, Status: status})
		if err != nil {
			t.Fatal(err)
		}
		if name != "hidden" {
			caps := OfferCaps{}
			if name == "public" {
				caps = OfferCaps{Audiences: []string{"public"}, Data: []string{"synthetic"}}
			}
			if err := s.Targets.Offer(ctx, target, "*", caps, "setup"); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := s.ProjectTargets(ctx, project.Project{ID: in.ProjectID, Slug: "config"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("offered targets: %+v", got)
	}
	wizard, err := s.CreationTargets(ctx, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range got {
		switch item.Target.Name {
		case "cloud":
			want := "cloud is offered for 1 standing deployment(s) per project; config already has 1"
			if item.Bound != 1 || item.Reason != want || item.Reason != item.Offer.permits(full, "config", Attributes{}, 1) {
				t.Fatalf("standing refusal: %+v", item)
			}
		case "unavailable":
			if item.Reason != "unavailable is unreachable. Check the target before choosing it." {
				t.Fatalf("status refusal: %q", item.Reason)
			}
			for _, option := range wizard {
				if option.Name == item.Target.Name && option.Reason != item.Reason {
					t.Fatalf("status differs: %q / %q", item.Reason, option.Reason)
				}
			}
		case "public":
			if item.Reason != "" {
				t.Fatalf("project inherited wizard classification: %s", item.Reason)
			}
		default:
			t.Fatalf("unoffered target leaked: %+v", item)
		}
	}
}

func TestProjectTargetsFailsOnUnreadableOffer(t *testing.T) {
	t.Parallel()
	s, _, in, target := configurationFixture(t)
	// sql-guarantee: no offer operation can encode malformed caps; unreadable storage must fail the read.
	mustExec(t, s.db, `UPDATE target_offers SET caps_json='{"audiences":42}'::jsonb WHERE target_id=$1`, target.ID)
	got, err := s.ProjectTargets(t.Context(), project.Project{ID: in.ProjectID})
	if err == nil || got != nil {
		t.Fatalf("offer read failure became a list: %+v, %v", got, err)
	}
}
