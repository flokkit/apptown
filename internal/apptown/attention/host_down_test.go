package attention

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type hostOutages map[ids.ID]deployment.HostReachability

func (h hostOutages) HostReachability(_ context.Context, id ids.ID) (deployment.HostReachability, error) {
	return h[id], nil
}

// One unreachable host replaces its unassessed runtime, data and resource rows.
// Another host's uncertainty and already confirmed conditions remain visible.
func TestHostDownFoldsUnknownsAndKeepsConfirmedConditions(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 3, 1, 2, 3, 0, time.UTC)
	host, other := idstest.N(1), idstest.N(2)
	outages := hostOutages{host: {Down: true, Since: at, Error: "connection refused", Deployments: 50}}
	targets := capacityTargets{[]*deployment.Target{{ID: host, Name: "office", Kind: deployment.KindDockerHost}, {ID: other, Name: "other", Kind: deployment.KindDockerHost}}}
	inv := Inventory{Targets: targets, Records: &recordedInventory{}, Observer: &inventoryEvidence{InventoryReading{Unknown: true, AttemptedAt: at}}, Hosts: outages, Now: func() time.Time { return at }}
	got, err := inv.Snapshot(t.Context())
	if err != nil || len(got.Items) != 2 {
		t.Fatalf("host items: %+v %v", got, err)
	}
	outage := got.Items[0]
	if outage.Title != "office is not reachable" || outage.Unknown || outage.Href != "/targets/office" || !strings.Contains(outage.Detail, "50 deployments can't be checked") || !strings.Contains(outage.Detail, "connection refused") || !strings.Contains(outage.Detail, "3 Nov, 01:02 UTC") || !strings.Contains(outage.Detail, "then use Check target") || strings.Contains(outage.Detail, "Open the target page") {
		t.Fatalf("outage: %+v", outage)
	}
	// A host with no standing deployment (only a preview, say) is still
	// unreachable, but does not count nothing as something it can't check.
	inv.Hosts = hostOutages{host: {Down: true, Since: at, Error: "connection refused"}}
	if got, err := inv.Snapshot(t.Context()); err != nil || strings.Contains(got.Items[0].Detail, "can't be checked") || !strings.Contains(got.Items[0].Detail, "then use Check target") {
		t.Fatalf("outage without deployments: %+v %v", got, err)
	}
	inv.Hosts = outages
	// Confirmed missing-resource evidence survives the outage too.
	resource := deploy.Resource{Kind: "volume", Name: "data"}
	inv.Records = &recordedInventory{rows: []deploy.Resource{resource}}
	inv.Observer = &inventoryEvidence{InventoryReading{Unknown: true, AttemptedAt: at, Missing: []deploy.Resource{resource}}}
	got, err = inv.Snapshot(t.Context())
	if err != nil || len(got.Items) != 4 {
		t.Fatalf("missing evidence erased: %+v %v", got, err)
	}
	rows := []deployment.OperationalObservation{}
	for i := 0; i < 50; i++ {
		rows = append(rows, deployment.OperationalObservation{ID: idstest.N(uint64(100 + i)), ObservedGeneration: 1, TargetID: host, Name: "office", Runtime: deployment.RuntimeObservation{State: deployment.RuntimeUnknown, AttemptedAt: at, Err: errors.New("offline")}})
	}
	rows[0].Hold = &deployment.CrashLoopHold{Confirmed: true, At: at.Add(-time.Hour).Format(time.RFC3339Nano)}
	rows[1].Readiness = deployment.ReadinessFailed
	rows = append(rows, deployment.OperationalObservation{ID: idstest.N(500), ObservedGeneration: 1, TargetID: other, Name: "other", Runtime: rows[0].Runtime})
	runtime := Deployments{Projects: protectionProject{}, Runtime: operationalEvidence{rows}, Hosts: outages, Now: func() time.Time { return at }}
	got, err = runtime.projectSnapshotForTest(t.Context(), idstest.N(3))
	if err != nil || len(got.Items) != 3 {
		t.Fatalf("deployment items: %+v %v", got, err)
	}
	for _, item := range got.Items {
		if strings.HasSuffix(item.Key, "/runtime") && !strings.Contains(item.Title, "other:") {
			t.Fatalf("outage runtime leaked: %+v", item)
		}
	}
	data := Protection{Projects: protectionProject{}, Deployments: protectionDeployments{[]*deployment.Deployment{{ID: idstest.N(4), TargetID: host, Kind: deployment.Standing, ObservedGeneration: 1, DataClass: "production"}, {ID: idstest.N(5), TargetID: other, Kind: deployment.Standing, ObservedGeneration: 1, DataClass: "production"}}}, Observer: &protectionEvidence{value: dataprotection.ProtectionSnapshot{Unknown: true, AttemptedAt: at}}, Hosts: outages}
	got, err = data.projectSnapshotForTest(t.Context(), idstest.N(3))
	if err != nil || len(got.Items) != 1 || !strings.HasPrefix(got.Items[0].Key, idstest.N(5).String()) {
		t.Fatalf("backup items: %+v %v", got, err)
	}
	capacity := Capacity{Targets: targets, Hosts: outages, Observer: &capacityEvidence{reads: map[ids.ID]deployment.CapacitySnapshot{host: {Unknown: true, DiskUnknown: true, AttemptedAt: at}, other: {Unknown: true, DiskUnknown: true, AttemptedAt: at}}}}
	got, err = capacity.Snapshot(t.Context())
	if err != nil || len(got.Items) != 1 || !strings.HasPrefix(got.Items[0].Key, other.String()) {
		t.Fatalf("capacity items: %+v %v", got, err)
	}
	delete(outages, host)
	got, err = runtime.projectSnapshotForTest(t.Context(), idstest.N(3))
	if err != nil || len(got.Items) != 53 {
		t.Fatalf("recovered host still folds runtime: %d %v", len(got.Items), err)
	}
}

// Folding includes source coverage rows, so the page and header receive exactly
// one confirmed host item rather than a second list of unknown source summaries.
func TestHostDownHasOneAttentionRow(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 3, 1, 2, 3, 0, time.UTC)
	host := idstest.N(1)
	outages := hostOutages{host: {Down: true, Since: at, Error: "connection refused", Deployments: 1}}
	targets := capacityTargets{[]*deployment.Target{{ID: host, Name: "office", Kind: deployment.KindDockerHost}}}
	sources := []instanceReader{
		&Inventory{Targets: targets, Records: &recordedInventory{}, Observer: &inventoryEvidence{InventoryReading{Unknown: true, AttemptedAt: at}}, Hosts: outages},
		Capacity{Targets: targets, Observer: &capacityEvidence{reads: map[ids.ID]deployment.CapacitySnapshot{host: {Unknown: true, DiskUnknown: true, AttemptedAt: at}}}, Hosts: outages},
	}
	view := View{}
	for i, source := range sources {
		snapshot, err := source.Snapshot(t.Context())
		view.add([]string{"inventory", "capacity"}[i], "hosts", snapshot, err)
	}
	runtime := Deployments{Projects: protectionProject{}, Runtime: operationalEvidence{[]deployment.OperationalObservation{{ID: idstest.N(2), ObservedGeneration: 1, TargetID: host, Runtime: deployment.RuntimeObservation{State: deployment.RuntimeUnknown, AttemptedAt: at, Err: errors.New("offline")}}}}, Hosts: outages}
	snapshot, err := runtime.projectSnapshotForTest(t.Context(), idstest.N(3))
	view.add("deployments/project", "deployments", snapshot, err)
	data := Protection{Projects: protectionProject{}, Deployments: protectionDeployments{[]*deployment.Deployment{{ID: idstest.N(2), TargetID: host, Kind: deployment.Standing, ObservedGeneration: 1, DataClass: "production"}}}, Observer: &protectionEvidence{value: dataprotection.ProtectionSnapshot{Unknown: true, AttemptedAt: at, Items: []dataprotection.ProtectionItemObservation{{Key: "data", Need: "database"}}}}, Hosts: outages}
	snapshot, err = data.projectSnapshotForTest(t.Context(), idstest.N(3))
	view.add("protection/project", "backups", snapshot, err)
	if len(view.Items) != 1 || view.Items[0].Unknown || len(view.Sources) != 0 || !strings.Contains(view.Items[0].Detail, "1 deployment can't be checked") {
		t.Fatalf("outage rows and coverage: %+v", view)
	}
	for _, source := range view.Sources {
		if source.Unknown {
			t.Fatalf("unfolded source: %+v", source)
		}
	}
	delete(outages, host)
	snapshot, err = sources[0].Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range snapshot.Items {
		if strings.HasSuffix(item.Key, "/unreachable") {
			t.Fatalf("recovery kept outage: %+v", item)
		}
	}
}

// A project grant cannot replace conditions with an instance item the caller
// cannot read. Authority failures remain failures rather than a quiet view.
func TestHostDownFoldingRequiresVisibilityOfTheHostItem(t *testing.T) {
	t.Parallel()
	host := idstest.N(1)
	observer := hostOutages{host: {Down: true}}
	for _, allowed := range []bool{false, true} {
		ctx := context.WithValue(t.Context(), instanceAccessKey{}, allowed)
		got, err := hostReachability(ctx, observer, host)
		if err != nil || got.Down != allowed {
			t.Fatalf("instance visibility %v: %+v %v", allowed, got, err)
		}
	}

}

// A source still reports checks for healthy hosts, including successful checks.
func TestHostDownKeepsMixedSourceCoverage(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 3, 1, 2, 3, 0, time.UTC)
	host, other := idstest.N(1), idstest.N(2)
	outages := hostOutages{host: {Down: true, Since: at}}
	for _, known := range []bool{true, false} {
		t.Run(fmt.Sprint(known), func(t *testing.T) {
			t.Parallel()
			targets := capacityTargets{[]*deployment.Target{{ID: host, Kind: deployment.KindDockerHost}, {ID: other, Kind: deployment.KindDockerHost}}}
			inv := &Inventory{Targets: targets, Records: &recordedInventory{}, Observer: &inventoryEvidence{InventoryReading{Unknown: !known, ObservedAt: at, AttemptedAt: at}}, Hosts: outages}
			rows := []deployment.OperationalObservation{{ID: idstest.N(3), ObservedGeneration: 1, TargetID: host, Runtime: deployment.RuntimeObservation{State: deployment.RuntimeUnknown, AttemptedAt: at}}}
			deps := []*deployment.Deployment{{ID: idstest.N(3), TargetID: host, Kind: deployment.Standing, ObservedGeneration: 1}}
			for i := 0; i < 5; i++ {
				state := deployment.RuntimeRunning
				if !known {
					state = deployment.RuntimeUnknown
				}
				rows = append(rows, deployment.OperationalObservation{ID: idstest.N(uint64(10 + i)), ObservedGeneration: 1, TargetID: other, Runtime: deployment.RuntimeObservation{State: state, ObservedAt: at.Add(-10 * time.Minute)}})
				deps = append(deps, &deployment.Deployment{ID: idstest.N(uint64(10 + i)), TargetID: other, Kind: deployment.Standing, ObservedGeneration: 1})
			}
			runtime := Deployments{Projects: protectionProject{}, Runtime: operationalEvidence{rows}, Hosts: outages, Now: func() time.Time { return at }}
			data := Protection{Projects: protectionProject{}, Deployments: protectionDeployments{deps}, Observer: &protectionEvidence{value: dataprotection.ProtectionSnapshot{Unknown: !known, ObservedAt: at, AttemptedAt: at}}, Hosts: outages}
			for _, source := range []struct {
				name string
				read func() (Snapshot, error)
			}{
				{"inventory", func() (Snapshot, error) { return inv.Snapshot(t.Context()) }},
				{"deployments", func() (Snapshot, error) { return runtime.projectSnapshotForTest(t.Context(), idstest.N(4)) }},
				{"protection", func() (Snapshot, error) { return data.projectSnapshotForTest(t.Context(), idstest.N(4)) }},
			} {
				snapshot, err := source.read()
				view := View{}
				view.add(source.name, source.name, snapshot, err)
				if err != nil || len(view.Sources) != 1 || view.Sources[0].Unknown == known {
					t.Errorf("%s mixed coverage (known=%v): %+v, %v", source.name, known, view.Sources, err)
				}
			}
		})
	}
}

func TestEmptySourcesKeepCoverage(t *testing.T) {
	t.Parallel()
	runtime := Deployments{Projects: protectionProject{}, Runtime: operationalEvidence{}}
	data := Protection{Projects: protectionProject{}, Deployments: protectionDeployments{}, Observer: &protectionEvidence{}}
	inv := &Inventory{Targets: capacityTargets{}, Records: &recordedInventory{}, Observer: &inventoryEvidence{}}
	runtimeSnapshot, runtimeErr := runtime.projectSnapshotForTest(t.Context(), idstest.N(1))
	dataSnapshot, dataErr := data.projectSnapshotForTest(t.Context(), idstest.N(1))
	inventorySnapshot, inventoryErr := inv.Snapshot(t.Context())
	view := View{}
	view.add("deployments", "deployments", runtimeSnapshot, runtimeErr)
	view.add("protection", "protection", dataSnapshot, dataErr)
	view.add("inventory", "inventory", inventorySnapshot, inventoryErr)
	if len(view.Sources) != 3 {
		t.Fatalf("empty source coverage: %+v", view)
	}
}
