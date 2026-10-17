package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/remediation"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestRemediationHTTPPreviewControlsAndItemScope(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	alpha, _ := failingFixture(t, a)
	own, _, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("t"), "manual", conformance.ReviewOptions{Projects: []ids.ID{alpha}})
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's adoptions queue their own reviews (not run here), so
	// review numbers are read, not assumed: the fixture's estate review is
	// the one before this one.
	listed, err := a.conformance.Reviews(testConformanceContext("t"), 50)
	if err != nil {
		t.Fatal(err)
	}
	var fixture ids.ID
	for _, r := range listed {
		if r.ID.Compare(own.ID) < 0 && r.ID.Compare(fixture) > 0 {
			fixture = r.ID
		}
	}
	// Test the HTTP commands with admitted items still queued: no pass runs
	// here (passes are queue jobs, and this test runs none).
	h := a.Handler()

	for _, tc := range []struct {
		slug   string
		count  int
		latest ids.ID
	}{{"alpha", 2, own.ID}, {"beta", 1, fixture}} {
		w := authzCall(h, "t", "GET", "/api/v1/conformance/projects/"+tc.slug+"/history", "")
		var history struct{ History []conformance.HistoryPoint }
		if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil || w.Code != 200 || len(history.History) != tc.count || history.History[len(history.History)-1].ReviewID != tc.latest || history.History[0].Total != 2 || history.History[0].Findings != 1 {
			t.Fatalf("project review history: %d %s %v", w.Code, w.Body.String(), err)
		}
	}
	detailResponse := authzCall(h, "t", "GET", fmt.Sprintf("/api/v1/conformance/reviews/%d", own.ID), "")
	var reviewDetail conformance.ReviewDetail
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &reviewDetail); err != nil || detailResponse.Code != 200 || reviewDetail.Review == nil || reviewDetail.Review.ID != own.ID || len(reviewDetail.Verdicts) != 2 {
		t.Fatalf("review detail: %d %s %v", detailResponse.Code, detailResponse.Body.String(), err)
	}
	if w := authzCall(h, "t", "GET", "/api/v1/conformance/projects/alpha/fixes", ""); w.Code != 200 || w.Body.String() != "{\"fixes\":[]}\n" {
		t.Fatalf("unfixed findings: %d %s", w.Code, w.Body.String())
	}
	base := "/api/v1/conformance/remediations"
	w := authzCall(h, "t", "POST", base, `{"scope":"all","preview":true}`)
	var preview struct{ Items, Projects int }
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil || w.Code != 200 || preview.Items != 2 || preview.Projects != 2 {
		t.Fatalf("preview: %d %s %v", w.Code, w.Body.String(), err)
	}
	jobs, err := a.Fixes.Remediations(testConformanceContext("t"), 50)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("preview created jobs: %+v %v", jobs, err)
	}
	for _, tc := range []struct {
		body string
		code int
	}{{`{`, 400}, {`{"scope":"wrong"}`, 422}} {
		w := authzCall(h, "t", "POST", base, tc.body)
		if w.Code != tc.code {
			t.Fatalf("invalid start: %d %s", w.Code, w.Body.String())
		}
	}
	w = authzCall(h, "t", "POST", base, `{"scope":"project","ref":" alpha ","concurrency":1}`)
	var started struct {
		Remediation remediation.Remediation
		Items       []remediation.Item
	}
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil || w.Code != 202 || started.Remediation.ID.IsZero() || len(started.Items) != 1 || started.Items[0].ProjectSlug != "alpha" {
		t.Fatalf("start: %d %s %v", w.Code, w.Body.String(), err)
	}
	id, item := started.Remediation.ID, started.Items[0].ID
	path := fmt.Sprintf("%s/%d", base, id)
	check := func(state string) {
		t.Helper()
		w := authzCall(h, "t", "GET", path, "")
		var detail struct {
			Remediation remediation.Remediation
			Items       []remediation.Item
			Attempts    map[int64][]remediation.Attempt
		}
		if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || w.Code != 200 || detail.Remediation.ID != id || detail.Remediation.Status != state || len(detail.Items) != 1 || detail.Items[0].ID != item || detail.Items[0].ProjectSlug != "alpha" {
			t.Fatalf("detail: %d %s %v", w.Code, w.Body.String(), err)
		}
	}
	check(remediation.RemRunning)
	if w := authzCall(h, "t", "GET", base+"/0000000f-423f-7000-8000-000000000000", ""); w.Code != 404 {
		t.Fatalf("unknown remediation: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct{ action, state string }{{"pause", remediation.RemPaused}, {"resume", remediation.RemRunning}} {
		w := authzCall(h, "t", "POST", path+"/"+tc.action, "")
		if w.Code != 200 {
			t.Fatalf("control: %d %s", w.Code, w.Body.String())
		}
		check(tc.state)
	}
	for _, tc := range []struct {
		path, body string
		code       int
	}{
		{path + "/unknown", `{}`, 404},
		{fmt.Sprintf("%s/%s/items/%s/skip", base, idstest.N(9999), item), `{"reason":"wrong job"}`, 404},
		{fmt.Sprintf("%s/items/%s/skip", path, item), `{"reason":false}`, 400},
		{fmt.Sprintf("%s/items/%s/retry", path, item), `{"note":"only stuck items retry"}`, 422},
		{fmt.Sprintf("%s/items/%s/skip", path, idstest.N(999999)), `{"reason":"no such item"}`, 404},
	} {
		w := authzCall(h, "t", "POST", tc.path, tc.body)
		if w.Code != tc.code {
			t.Fatalf("refused control %s: %d %s", tc.path, w.Code, w.Body.String())
		}
		items, err := a.Fixes.Items(testConformanceContext("t"), &id)
		if err != nil || len(items) != 1 || items[0].State != remediation.ItemQueued {
			t.Fatalf("refusal changed item: %+v %v", items, err)
		}
	}
	w = authzCall(h, "t", "POST", fmt.Sprintf("%s/items/%s/skip", path, item), `{"reason":"Handled elsewhere"}`)
	if w.Code != 200 {
		t.Fatalf("skip: %d %s", w.Code, w.Body.String())
	}
	items, err := a.Fixes.Items(testConformanceContext("t"), &id)
	if err != nil || len(items) != 1 || items[0].State != remediation.ItemSkipped || items[0].Reason != "skipped by t: Handled elsewhere" {
		t.Fatalf("skip effect: %+v %v", items, err)
	}
	w = authzCall(h, "t", "POST", path+"/cancel", "")
	if w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	check(remediation.RemCancelled)
	w = authzCall(h, "t", "GET", base, "")
	var list struct{ Remediations []remediation.Remediation }
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || len(list.Remediations) != 1 || list.Remediations[0].ID != id || list.Remediations[0].Status != remediation.RemCancelled {
		t.Fatalf("list: %d %s %v", w.Code, w.Body.String(), err)
	}
	for _, tc := range []struct {
		suffix string
		code   int
	}{{"bad", 400}, {idstest.N(9999).String(), 404}} {
		w := authzCall(h, "t", "GET", base+"/"+tc.suffix, "")
		if w.Code != tc.code {
			t.Fatalf("bad detail: %d %s", w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct {
		body string
		code int
	}{{`{`, 400}, {`{"kind":"unknown","count":2}`, 422}, {`{"count":-1}`, 422}, {`{"count":0}`, 200}} {
		w := authzCall(h, "t", "PUT", "/api/v1/workers", tc.body)
		if w.Code != tc.code {
			t.Fatalf("workers: %d %s", w.Code, w.Body.String())
		}
	}
	if a.Fixes.WorkerCount(context.Background(), remediation.WorkerKindRemediation) != 0 {
		t.Fatal("pool update lost")
	}
}
