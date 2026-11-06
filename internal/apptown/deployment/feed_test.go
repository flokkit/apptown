package deployment

import (
	"errors"
	"github.com/flokkit/apptown/internal/access"
	"testing"
)

func TestConfirmFeedRecordsAuthorityAndRevisionAndRechecksProtection(t *testing.T) {
	for _, scenario := range []string{"confirmed", "denied", "stale revision", "protected"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s, in, authority, _ := requestFixture(t)
			prior, err := s.Deployments.ByName(t.Context(), in.ProjectID, in.DeploymentName)
			if err != nil {
				t.Fatal(err)
			}
			audience := "team"
			if scenario == "protected" {
				audience = "public"
			}
			dep, err := s.Deployments.EnsureStanding(t.Context(), in.ProjectID, prior.Name, prior.TargetID, Attributes{Feed: "main", Audience: audience, DataClass: "synthetic"})
			if err != nil {
				t.Fatal(err)
			}
			if dep.FeedConfirmed {
				t.Fatal("recording a deployment invented consent")
			}
			revision := dep.FeedRevision
			if scenario == "denied" {
				authority.deny = &access.Denied{Reason: "deploy grant withdrawn"}
			}
			if scenario == "stale revision" {
				revision--
			}
			current, err := s.ConfirmFeed(t.Context(), in.ProjectID, dep.Name, revision)
			if scenario == "denied" || scenario == "stale revision" {
				if err == nil {
					t.Fatal("confirmation bypassed authority or revision")
				}
				return
			}
			if err != nil || !current.FeedConfirmed || current.FeedRevision != revision+1 {
				t.Fatalf("confirmation: %+v %v", current, err)
			}
			principal, err := releasePrincipal(current.feedAuthority)
			if err != nil || principal.Name != "alice" || principal.Credential != "header:alice" {
				t.Fatalf("authority: %+v %v", principal, err)
			}
			if _, err := s.ConfirmFeed(t.Context(), in.ProjectID, dep.Name, revision); !errors.Is(err, errFeedMoved) {
				t.Fatalf("stale settings write: %v", err)
			}
			if scenario == "protected" {
				promote := false
				for _, p := range authority.seen {
					promote = promote || p.Capability == access.CapReleasePromote
				}
				if !promote {
					t.Fatal("protected feed did not require promotion authority")
				}
			}
		})
	}
}

func TestFeedVocabularyHasNoCompatibilityValues(t *testing.T) {
	t.Parallel()
	for _, feed := range []string{"main", "promotion", "manual", "landing", "schedule"} {
		err := (Attributes{Feed: feed, Audience: "team", DataClass: "synthetic"}).Validate()
		if (err == nil) != (feed == "main" || feed == "promotion" || feed == "manual") {
			t.Fatalf("feed %s: %v", feed, err)
		}
	}
}
