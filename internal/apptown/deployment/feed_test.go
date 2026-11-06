package deployment

import (
	"context"
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

// An agent's deploy grant permits requests, never standing human assent.
type agentFeedAuthority struct{ access.Authorizer }

func (a agentFeedAuthority) Require(ctx context.Context, permission access.Permission) (access.Caller, error) {
	caller, err := a.Authorizer.Require(ctx, permission)
	caller.Principal.Via = "token"
	caller.Principal.Purpose = access.TokenAgent
	caller.Principal.Credential = "token:agent"
	return caller, err
}

func TestAgentCannotRecordFollowsMainSetter(t *testing.T) {
	for _, path := range []string{"set-feed", "confirm-feed", "confirm-all-feeds"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			s, in, authority, _ := requestFixture(t)
			dep, err := s.Deployments.ByName(t.Context(), in.ProjectID, in.DeploymentName)
			if err != nil {
				t.Fatal(err)
			}
			if path != "set-feed" {
				dep, err = s.Deployments.EnsureStanding(t.Context(), in.ProjectID, dep.Name, dep.TargetID, Attributes{Feed: "main", Audience: "team", DataClass: "synthetic"})
				if err != nil {
					t.Fatal(err)
				}
			}
			s.release.Authority = agentFeedAuthority{authority}
			switch path {
			case "set-feed":
				_, err = s.SetFeed(t.Context(), in.ProjectID, dep.Name, FeedSettings{Revision: dep.FeedRevision, Feed: "main"})
			case "confirm-feed":
				_, err = s.ConfirmFeed(t.Context(), in.ProjectID, dep.Name, dep.FeedRevision)
			case "confirm-all-feeds":
				confirmed, refused, bulkErr := s.ConfirmAllFeeds(t.Context())
				if bulkErr != nil || len(confirmed) != 0 || len(refused) != 1 {
					t.Fatalf("bulk confirmation: confirmed=%v refused=%v err=%v", confirmed, refused, bulkErr)
				}
			}
			if path != "confirm-all-feeds" && !errors.Is(err, errReleaseAuthority) {
				t.Fatalf("agent must be refused standing authority: %v", err)
			}
			saved, err := s.Deployments.ByID(t.Context(), dep.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.FeedConfirmed || len(saved.feedAuthority) != 0 || saved.FeedRevision != dep.FeedRevision || saved.Feed != dep.Feed {
				t.Fatalf("refused agent changed feed or recorded assent: %+v", saved)
			}
		})
	}
}

func TestConfirmFeedKeepsTheApprovalRuleOnTheSameRevision(t *testing.T) {
	s, in, _, _ := requestFixture(t)
	dep, err := s.Deployments.ByName(t.Context(), in.ProjectID, in.DeploymentName)
	if err != nil {
		t.Fatal(err)
	}
	dep, err = s.SetFeed(t.Context(), in.ProjectID, dep.Name, FeedSettings{Revision: dep.FeedRevision, Feed: "main", ApprovalRole: access.RoleReleaseManager})
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := s.ConfirmFeed(t.Context(), in.ProjectID, dep.Name, dep.FeedRevision)
	if err != nil || confirmed.ApprovalRole != access.RoleReleaseManager || confirmed.FeedRevision != dep.FeedRevision+1 {
		t.Fatalf("confirmation erased approval: %+v %v", confirmed, err)
	}
	if _, err := s.SetFeed(t.Context(), in.ProjectID, dep.Name, FeedSettings{Revision: dep.FeedRevision, Feed: "main"}); !errors.Is(err, errFeedMoved) {
		t.Fatalf("stale edit removed approval rule: %v", err)
	}
}
