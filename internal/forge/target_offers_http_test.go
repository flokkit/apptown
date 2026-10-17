package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestTargetOfferHTTPWithdrawalPreventsNewBindingsAndPreservesExistingDeployment(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t, Config{DockerClient: newFakeDocker()})
	target, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: "offered", Kind: deployment.KindDockerHost, Origin: deployment.OriginAttached, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: "other", Kind: deployment.KindDockerHost, Origin: deployment.OriginAttached, Status: deployment.TargetReady}); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	base := "/api/v1/targets/offered/offers"
	for _, tc := range []struct {
		who, body string
		code      int
	}{
		{"outsider", `{"group":"*"}`, 403}, {"boss", `{`, 400}, {"boss", `{}`, 422}, {"boss", `{"group":"missing"}`, 404}, {"boss", `{"group":"*","audiences":["nonsense"]}`, 422},
	} {
		if w := authzCall(h, tc.who, "POST", base, tc.body); w.Code != tc.code {
			t.Fatalf("refused offer: %d %s", w.Code, w.Body.String())
		}
	}
	if offers, err := a.Deployment.Targets.Offers(context.Background(), target.ID); err != nil || len(offers) != 0 {
		t.Fatalf("refused offers persisted: %+v %v", offers, err)
	}
	var offer deployment.Offer
	for _, cap := range []int{1, 2} {
		w := authzCall(h, "boss", "POST", base, fmt.Sprintf(`{"group":" * ","maxStanding":%d,"audiences":["team"]}`, cap))
		var response struct{ Offers []deployment.Offer }
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 || len(response.Offers) != 1 || response.Offers[0].TargetID != target.ID || response.Offers[0].Group != "*" || response.Offers[0].Caps.MaxStanding != cap || response.Offers[0].CreatedBy != "boss" {
			t.Fatalf("offer receipt: %d %s %v", w.Code, w.Body.String(), err)
		}
		offer = response.Offers[0]
	}
	bound, err := a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), "chosen", target.ID, (deployment.Attributes{}).WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	checkAvailable := func(want bool) {
		t.Helper()
		w := authzCall(h, "owner", "GET", "/api/v1/projects/game/targets", "")
		var response struct {
			Targets []struct {
				Name  string
				Bound int
				Offer deployment.Offer
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 {
			t.Fatalf("project targets: %d %s %v", w.Code, w.Body.String(), err)
		}
		found := false
		for _, v := range response.Targets {
			if v.Name == target.Name {
				found = true
				if v.Bound != 1 || v.Offer.ID != offer.ID || v.Offer.Caps.MaxStanding != 2 {
					t.Fatalf("selected offer: %+v", v)
				}
			}
		}
		if found != want {
			t.Fatalf("offered=%v want %v: %s", found, want, w.Body.String())
		}
	}
	checkAvailable(true)
	path := fmt.Sprintf("%s/%d", base, offer.ID)
	for _, tc := range []struct {
		who, path string
		code      int
	}{{"outsider", path, 403}, {"boss", fmt.Sprintf("/api/v1/targets/other/offers/%d", offer.ID), 404}} {
		if w := authzCall(h, tc.who, "DELETE", tc.path, ""); w.Code != tc.code {
			t.Fatalf("refused withdrawal: %d %s", w.Code, w.Body.String())
		}
	}
	checkAvailable(true)
	if w := authzCall(h, "boss", "DELETE", path, ""); w.Code != 200 {
		t.Fatalf("withdrawal: %d %s", w.Code, w.Body.String())
	}
	checkAvailable(false)
	after, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "chosen")
	if err != nil || after.ID != bound.ID || after.TargetID != target.ID || !after.Standing() {
		t.Fatalf("withdrawal changed bound deployment: %+v %v", after, err)
	}
	if offers, err := a.Deployment.Targets.Offers(context.Background(), target.ID); err != nil || len(offers) != 0 {
		t.Fatalf("offer remained: %+v %v", offers, err)
	}
}
