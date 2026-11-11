package deployment

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// classified makes a deployment with audience and data and admits a
// generation on it, so it has run.
func classified(t *testing.T, audience, data string) (*Service, *configurationWorld, *Deployment) {
	t.Helper()
	s, w, in, target := configurationFixture(t)
	in.Audience, in.DataClass = audience, data
	dep := configureForTest(t, s, in)
	if _, _, err := s.Outcomes.Admit(context.Background(), Start{DeploymentID: dep.ID, TargetID: target.ID, Key: "admitted"}); err != nil {
		t.Fatal(err)
	}
	return s, w, dep
}

func edit(dep *Deployment, audience, data, meaning string) ClassificationSettings {
	in := ClassificationSettings{Audience: audience, Data: data, Meaning: meaning}
	in.Was.Audience, in.Was.Data = dep.Audience, dep.DataClass
	return in
}

func TestRaisingADeploymentThatHasRunToPublicIsRecorded(t *testing.T) {
	t.Parallel()
	s, _, dep := classified(t, "team", "non-production")
	got, err := s.SetClassification(t.Context(), dep.ProjectID, dep.Name, edit(dep, "public", "non-production", ""))
	if err != nil {
		t.Fatal(err)
	}
	if got.Audience != "public" || !got.Protected() {
		t.Fatalf("not raised: %+v", got)
	}
	r := receipts(t, s.db, "deployment.classified")
	wantN(t, "classified receipts", r, 1)
	if r[0].Actor != "alice" || !strings.Contains(string(r[0].Payload), `"protectedAfter": true`) && !strings.Contains(string(r[0].Payload), `"protectedAfter":true`) {
		t.Fatalf("receipt: %+v %s", r[0], r[0].Payload)
	}
	wantN(t, "lowered receipts", receipts(t, s.db, "deployment.classification-lowered"), 0)
}

func TestLoweringNeedsTheStatedMeaningAndIsItsOwnAct(t *testing.T) {
	t.Parallel()
	s, _, dep := classified(t, "team", "production")
	view, err := s.Classification(t.Context(), dep.ProjectID, dep.Name, &Attributes{Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	p := view.Proposed
	if p == nil || !p.Lowers || p.Meaning == "" || len(p.Removes) != 3 || len(p.Adds) != 0 {
		t.Fatalf("proposal: %+v", p)
	}
	for _, meaning := range []string{"", "I agree"} {
		_, err := s.SetClassification(t.Context(), dep.ProjectID, dep.Name, edit(dep, "team", "synthetic", meaning))
		if fault.KindOf(err) != fault.Unprocessable || !strings.Contains(err.Error(), p.Meaning) {
			t.Fatalf("lowered with meaning %q: %v", meaning, err)
		}
	}
	got, err := s.SetClassification(t.Context(), dep.ProjectID, dep.Name, edit(dep, "team", "synthetic", p.Meaning))
	if err != nil {
		t.Fatal(err)
	}
	if got.DataClass != "synthetic" {
		t.Fatalf("not lowered: %+v", got)
	}
	r := receipts(t, s.db, "deployment.classification-lowered")
	wantN(t, "lowered receipts", r, 1)
	if !strings.Contains(string(r[0].Payload), p.Meaning) {
		t.Fatalf("meaning not recorded: %s", r[0].Payload)
	}
}

func TestLoweringAProtectedDeploymentNeedsTheReleaseGrantAndAPerson(t *testing.T) {
	t.Parallel()
	s, w, dep := classified(t, "public", "non-production")
	meaning := classificationMeaning(dep.DisplayLabel, Attributes{Audience: "team", DataClass: "non-production"})
	w.deny = access.CapReleasePromote
	if _, err := s.SetClassification(t.Context(), dep.ProjectID, dep.Name, edit(dep, "team", "non-production", meaning)); fault.KindOf(err) != fault.Forbidden {
		t.Fatalf("lowered without the release grant: %v", err)
	}
	w.deny, w.agent = "", true
	if _, err := s.SetClassification(t.Context(), dep.ProjectID, dep.Name, edit(dep, "team", "non-production", meaning)); !errors.Is(err, errLoweringByAgent) {
		t.Fatalf("an agent lowered: %v", err)
	}
	if got, _ := s.Deployments.ByID(t.Context(), dep.ID); got.Audience != "public" {
		t.Fatalf("refusals changed it: %+v", got)
	}
	wantN(t, "receipts", receipts(t, s.db, "deployment.classif"), 0)
}

func TestConfigureNoLongerChangesAnExistingDeploymentsClassification(t *testing.T) {
	t.Parallel()
	s, _, in, target := configurationFixture(t)
	in.DataClass = "production"
	dep := configureForTest(t, s, in)
	if _, _, err := s.Outcomes.Admit(context.Background(), Start{DeploymentID: dep.ID, TargetID: target.ID, Key: "admitted"}); err != nil {
		t.Fatal(err)
	}
	in.DataClass = "synthetic"
	if _, err := s.Configure(t.Context(), in); fault.KindOf(err) != fault.Conflict {
		t.Fatalf("configure lowered production data: %v", err)
	}
	if got, _ := s.Deployments.ByID(t.Context(), dep.ID); got.DataClass != "production" {
		t.Fatalf("data class: %s", got.DataClass)
	}
}

func TestClassificationRefusesWhatTheOfferOrTargetRefuses(t *testing.T) {
	t.Parallel()
	t.Run("offer", func(t *testing.T) {
		t.Parallel()
		s, _, dep := classified(t, "team", "non-production")
		target, _ := s.Targets.ByID(t.Context(), dep.TargetID)
		if err := s.Targets.Offer(t.Context(), target, "*", OfferCaps{Audiences: []string{"team", "organisation"}}, "setup"); err != nil {
			t.Fatal(err)
		}
		view, err := s.Classification(t.Context(), dep.ProjectID, dep.Name, nil)
		if err != nil {
			t.Fatal(err)
		}
		if view.Audiences[3].Value != "public" || !strings.Contains(view.Audiences[3].Refusal, "offered for audiences") || view.Audiences[1].Refusal != "" {
			t.Fatalf("choices: %+v", view.Audiences)
		}
		if _, err := s.SetClassification(t.Context(), dep.ProjectID, dep.Name, edit(dep, "public", "non-production", "")); fault.KindOf(err) != fault.Conflict || !strings.Contains(err.Error(), "offered for audiences") {
			t.Fatalf("offer cap: %v", err)
		}
	})
	t.Run("fly", func(t *testing.T) {
		t.Parallel()
		s, _, in, _ := configurationFixture(t)
		fly, err := s.Targets.Put(t.Context(), &Target{Name: "fly-syd", Kind: kindFlyMachines, Origin: OriginAttached, Status: TargetReady})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Targets.Offer(t.Context(), fly, "*", OfferCaps{}, "setup"); err != nil {
			t.Fatal(err)
		}
		in.Target, in.Audience = fly.Name, "team"
		dep := configureForTest(t, s, in)
		view, err := s.Classification(t.Context(), dep.ProjectID, dep.Name, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range view.Audiences {
			if refused := strings.Contains(c.Refusal, "fly.dev"); refused != (c.Value == "organisation" || c.Value == "partners") {
				t.Fatalf("choice %+v", c)
			}
		}
		if _, err := s.SetClassification(t.Context(), dep.ProjectID, dep.Name, edit(dep, "organisation", dep.DataClass, "")); fault.KindOf(err) != fault.Conflict || !strings.Contains(err.Error(), "choose public") {
			t.Fatalf("fly organisation: %v", err)
		}
		if got, err := s.SetClassification(t.Context(), dep.ProjectID, dep.Name, edit(dep, "public", dep.DataClass, "")); err != nil || got.Audience != "public" {
			t.Fatalf("fly public: %+v %v", got, err)
		}
	})
}

func TestAProductionCopyKeepsItsDeploymentProduction(t *testing.T) {
	for _, class := range []string{"production", "non-production"} {
		t.Run(class, func(t *testing.T) {
			t.Parallel()
			s, _, dep := classified(t, "team", "production")
			s.starter = &copyStarter{class: class}
			if n, err := deploymentsql.New(s.db.SQL()).SetStartFrom(t.Context(), deploymentsql.SetStartFromParams{BackupID: idstest.N(7), ID: dep.ID}); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			meaning := classificationMeaning(dep.DisplayLabel, Attributes{Audience: "team", DataClass: "non-production"})
			_, err := s.SetClassification(t.Context(), dep.ProjectID, dep.Name, edit(dep, "team", "non-production", meaning))
			if refused := err != nil && strings.Contains(err.Error(), "copy of production data"); refused != (class == "production") {
				t.Fatalf("copy of %s data: %v", class, err)
			}
		})
	}
}

func TestClassificationEditFromStaleValuesIsRefused(t *testing.T) {
	t.Parallel()
	s, _, dep := classified(t, "team", "non-production")
	stale := edit(dep, "public", "non-production", "")
	if _, err := s.SetClassification(t.Context(), dep.ProjectID, dep.Name, edit(dep, "organisation", "non-production", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetClassification(t.Context(), dep.ProjectID, dep.Name, stale); !errors.Is(err, errClassificationMoved) {
		t.Fatalf("overwrote a newer value: %v", err)
	}
}

// What a classification adds is said per target: AppTown's daily copies are
// promised only where it takes them (App Platform), retention counts sets,
// and the synthetic-data rule is a Docker host's.
func TestClassificationSaysWhatItsTargetDoes(t *testing.T) {
	t.Parallel()
	s, _, in, _ := configurationFixture(t)
	docker, err := s.Targets.Put(t.Context(), &Target{Name: "shed", Kind: KindDockerHost, Origin: OriginAttached, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Targets.Offer(t.Context(), docker, "*", OfferCaps{}, "setup"); err != nil {
		t.Fatal(err)
	}
	cloud := configureForTest(t, s, in)
	in.Name, in.Target, in.DataClass = "shed", docker.Name, "synthetic"
	shed := configureForTest(t, s, in)
	adds := func(dep *Deployment, data string) string {
		view, err := s.Classification(t.Context(), dep.ProjectID, dep.Name, &Attributes{Audience: dep.Audience, DataClass: data})
		if err != nil {
			t.Fatal(err)
		}
		if view.Proposed.Lowers || len(view.Proposed.Removes) != 0 {
			t.Fatalf("raise read as lowering: %+v", view.Proposed)
		}
		return strings.Join(view.Proposed.Adds, "\n")
	}
	got := adds(cloud, "production")
	if !strings.Contains(got, "newest 14 backup sets instead of 7") || !strings.Contains(got, "takes a copy each day") || !strings.Contains(got, "release permission") || strings.Contains(got, "discarding") {
		t.Fatalf("App Platform: %s", got)
	}
	got = adds(shed, "production")
	if strings.Contains(got, "each day") || !strings.Contains(got, "release permission") || !strings.Contains(got, "discarding") || !strings.Contains(got, "newest 14") {
		t.Fatalf("Docker host: %s", got)
	}
	if got = adds(shed, "non-production"); got != "A release keeps a service's data when the service's settings change, instead of discarding it" {
		t.Fatalf("Docker host leaving synthetic: %q", got)
	}
}
