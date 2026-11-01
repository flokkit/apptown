package attention

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type recordedInventory struct {
	rows []deploy.Resource
	err  error
}

func (r *recordedInventory) Recorded(context.Context, *deployment.Target) ([]deploy.Resource, error) {
	return r.rows, r.err
}

type inventoryEvidence struct{ value InventoryReading }

func (r *inventoryEvidence) Snapshot(context.Context, *deployment.Target) (InventoryReading, error) {
	return r.value, nil
}

func TestInventoryConditionsNeedCompleteCurrentEvidenceAndRetainOnlyTheirOwnIdentity(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	target := &deployment.Target{ID: idstest.N(1), Name: "office", Status: deployment.TargetReady}
	records := &recordedInventory{rows: []deploy.Resource{{Kind: "volume", Name: "private-name", Hosts: []deploy.ResourceHost{{DeploymentID: idstest.N(2), Generation: 1}}}}}
	evidence := &inventoryEvidence{InventoryReading{Unknown: true}}
	source := &Inventory{Targets: capacityTargets{[]*deployment.Target{target}}, Records: records, Observer: evidence, Now: func() time.Time { return at }}
	read := func() Snapshot {
		t.Helper()
		got, err := source.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	missing := func(s Snapshot) int {
		n := 0
		for _, i := range s.Items {
			if i.Title == "office: resource missing" {
				n++
			}
		}
		return n
	}
	if got := read(); got.Known || missing(got) != 0 {
		t.Fatalf("cold inventory invented missing %+v", got)
	}
	evidence.value = InventoryReading{ObservedAt: at, Missing: deployment.MergeInventory(records.rows, nil)}
	got := read()
	if !got.Known || missing(got) != 1 || got.Items[0].Stale || strings.Contains(got.Items[0].Key, "private-name") {
		t.Fatalf("complete missing evidence %+v", got)
	}
	originalKey := got.Items[0].Key
	evidence.value.Unknown = true
	evidence.value.FailedAt = at
	got = read()
	if got.Known || missing(got) != 1 || !got.Items[0].Stale || got.Items[0].ObservedAt != at {
		t.Fatalf("failure erased missing evidence %+v", got)
	}
	records.rows[0].Hosts[0].DeploymentID = idstest.N(3)
	if got = read(); missing(got) != 0 {
		t.Fatalf("prior identity crossed replacement %+v", got)
	}
	evidence.value = InventoryReading{ObservedAt: at, Stale: true}
	if got = read(); missing(got) != 0 {
		t.Fatalf("stale inventory established missing %+v", got)
	}
	evidence.value.Stale = false
	evidence.value.Missing = deployment.MergeInventory(records.rows, nil)
	got = read()
	if missing(got) != 1 || got.Items[0].Key == originalKey {
		t.Fatalf("new identity missing evidence %+v", got)
	}
	evidence.value.Missing = nil
	evidence.value.Items = []deploy.Resource{{Kind: "volume", Name: "private-name"}}
	if got = read(); !got.Known || len(got.Items) != 0 {
		t.Fatalf("fresh recovery did not clear own condition %+v", got)
	}
}

// A changed sibling record is not evidence that an existing missing volume recovered.
func TestReviewInventoryRetainsUnchangedMissingResourceWhenSiblingChanges(t *testing.T) {
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	target := &deployment.Target{ID: idstest.N(1), Name: "office", Status: deployment.TargetReady}
	records := &recordedInventory{rows: []deploy.Resource{{Kind: "volume", Name: "existing-data", Hosts: []deploy.ResourceHost{{DeploymentID: idstest.N(2), Generation: 1}}}}}
	evidence := &inventoryEvidence{InventoryReading{ObservedAt: at, Missing: deployment.MergeInventory(records.rows, nil)}}
	source := &Inventory{Targets: capacityTargets{[]*deployment.Target{target}}, Records: records, Observer: evidence, Now: func() time.Time { return at }}
	first, err := source.Snapshot(t.Context())
	if err != nil || len(first.Items) != 1 {
		t.Fatalf("initial evidence: %+v %v", first, err)
	}
	key := first.Items[0].Key
	records.rows = append(records.rows, deploy.Resource{Kind: "container", Name: "another-app", Hosts: []deploy.ResourceHost{{DeploymentID: idstest.N(3), Generation: 1}}})
	evidence.value.Unknown = true
	evidence.value.FailedAt = at
	evidence.value.Stale = true
	next, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range next.Items {
		if item.Key == key && item.Stale && item.ObservedAt == at {
			return
		}
	}
	t.Fatalf("unchanged missing resource lost without recovery evidence: %+v", next)
}

func TestInventoryRecordFailureRetainsExistingMissingEvidence(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	target := &deployment.Target{ID: idstest.N(1), Name: "office", Status: deployment.TargetReady}
	records := &recordedInventory{rows: []deploy.Resource{{Kind: "volume", Name: "one"}}}
	evidence := &inventoryEvidence{InventoryReading{ObservedAt: at, Missing: deployment.MergeInventory(records.rows, nil)}}
	source := &Inventory{Targets: capacityTargets{[]*deployment.Target{target}}, Records: records, Observer: evidence, Now: func() time.Time { return at }}
	first, err := source.Snapshot(t.Context())
	if err != nil || len(first.Items) != 1 {
		t.Fatalf("initial %+v %v", first, err)
	}
	records.err = errors.New("database secret")
	records.rows = nil
	failed, err := source.Snapshot(t.Context())
	if err != nil || failed.Known || len(failed.Items) != 2 || failed.Items[0].Key != first.Items[0].Key || !failed.Items[0].Stale || failed.Items[0].ObservedAt != at {
		t.Fatalf("record error erased warning %+v %v", failed, err)
	}
}

// Unknown coverage becomes actionable only with a completed attempt or a read failure.
func TestInventoryPendingFirstCheckIsCoverageOnly(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		reading   InventoryReading
		wantKnown bool
		wantItems int
	}{
		{name: "queued", reading: InventoryReading{Unknown: true, Stale: true}},
		{name: "failed first attempt", reading: InventoryReading{Unknown: true, Stale: true, AttemptedAt: at}, wantItems: 1},
		{name: "stale completed reading", reading: InventoryReading{ObservedAt: at, AttemptedAt: at, Stale: true}, wantKnown: true, wantItems: 0},
		{name: "fresh completed reading", reading: InventoryReading{ObservedAt: at, AttemptedAt: at}, wantKnown: true},
		{name: "no inventory reader", reading: InventoryReading{Unknown: true, Unreported: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target := &deployment.Target{ID: idstest.N(1), Name: "office", Status: deployment.TargetReady}
			source := &Inventory{Targets: capacityTargets{[]*deployment.Target{target}}, Records: &recordedInventory{}, Observer: &inventoryEvidence{tc.reading}, Now: func() time.Time { return at.Add(24 * time.Hour) }}
			got, err := source.Snapshot(t.Context())
			if err != nil || got.Known != tc.wantKnown || len(got.Items) != tc.wantItems || got.Stale != tc.reading.Stale {
				t.Fatalf("inventory coverage and conditions: %+v %v", got, err)
			}
			if tc.wantItems > 0 && (got.Items[0].Title != "office: resources not known" || !got.Items[0].Stale) {
				t.Fatalf("missing unassessed condition: %+v", got.Items)
			}
		})
	}
}
