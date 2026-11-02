package attention

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/maintenance"
	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type authority struct {
	projects []ids.ID
	instance bool
	err      error
	revoked  bool
}

func (a *authority) VisibleProjects(context.Context, access.Capability) ([]ids.ID, error) {
	return a.projects, a.err
}
func (a *authority) Require(_ context.Context, p access.Permission) (access.Caller, error) {
	if a.err != nil {
		return access.Caller{}, a.err
	}
	if p.Resource.Type == "instance" && !a.instance || p.Resource.Type == "project" && a.revoked {
		return access.Caller{}, fault.New(fault.Forbidden, "denied")
	}
	return access.Caller{}, nil
}

type instanceSource struct {
	snapshot Snapshot
	err      error
	calls    int
}

func (s *instanceSource) Snapshot(context.Context) (Snapshot, error) {
	s.calls++
	return s.snapshot, s.err
}

type projectSource struct {
	snapshots map[ids.ID]Snapshot
	calls     []ids.ID
}

func (s *projectSource) ProjectSnapshot(_ context.Context, id ids.ID) (Snapshot, error) {
	s.calls = append(s.calls, id)
	return s.snapshots[id], nil
}

// Reading a global count must not contact hidden sources or count their items.
// Revoking project access also removes cached observations on the next read.
func TestCurrentAuthorityControlsCollectionAndCount(t *testing.T) {
	t.Parallel()
	visible, hidden := ids.New(time.Now()), ids.New(time.Now())
	auth := &authority{projects: []ids.ID{visible, visible}}
	global := &instanceSource{snapshot: Snapshot{Known: true, Items: []Item{{Key: "secret", Title: "hidden host"}}}}
	projects := &projectSource{snapshots: map[ids.ID]Snapshot{
		visible: {Known: true, Items: []Item{{Key: "release", Title: "visible app"}}},
		hidden:  {Known: true, Items: []Item{{Key: "release", Title: "hidden app"}}},
	}}
	svc := New(auth, Sources{Disks: global, Deployments: projects})
	got, err := svc.Read(t.Context())
	if err != nil || len(got.Items) != 1 || got.Items[0].Title != "visible app" || global.calls != 0 || len(projects.calls) != 1 || projects.calls[0] != visible {
		t.Fatalf("visibility: %+v, %v, calls %d/%v", got, err, global.calls, projects.calls)
	}
	if len(got.Sources) != 3 {
		t.Fatalf("hidden source counts leaked: %+v", got.Sources)
	}
	auth.revoked = true
	got, err = svc.Read(t.Context())
	if err != nil || len(got.Items) != 0 || len(got.Sources) != 0 || len(projects.calls) != 1 {
		t.Fatalf("revoked view: %+v %v", got, err)
	}
	auth.err = errors.New("authority unavailable")
	if _, err = svc.Read(t.Context()); err == nil || global.calls != 0 || len(projects.calls) != 1 {
		t.Fatal("authority failure collected observations")
	}
}

// A failed authorized source can retain old conditions, but cannot retimestamp
// them, disclose its raw error, or turn missing coverage into an all-clear view.
func TestUnknownCoverageRetainsStaleConditionsWithoutRawErrors(t *testing.T) {
	t.Parallel()
	observed := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	disks := &instanceSource{snapshot: Snapshot{Known: true, ObservedAt: observed, Items: []Item{{Key: "disk-a", Title: "disk low", ObservedAt: observed}}}, err: errors.New("secret-provider-address-and-token")}
	svc := New(&authority{instance: true}, Sources{Disks: disks})
	got, err := svc.Read(t.Context())
	if err != nil || len(got.Items) != 1 || !got.Items[0].Stale || got.Items[0].ObservedAt != observed || len(got.Sources) != 5 {
		t.Fatalf("failed source: %+v %v", got, err)
	}
	for _, source := range got.Sources {
		if !source.Unknown || strings.Contains(source.Name, "secret") {
			t.Fatalf("source coverage: %+v", source)
		}
	}
	disks.err = nil
	disks.snapshot = Snapshot{Known: true, ObservedAt: observed.Add(time.Minute)}
	got, err = svc.Read(t.Context())
	if err != nil || len(got.Items) != 0 {
		t.Fatalf("recovery: %+v %v", got, err)
	}
	for _, source := range got.Sources {
		if source.Key == "disks" && source.Unknown {
			t.Fatal("successful disk recovery stayed unknown")
		}
		if source.Key != "disks" && !source.Unknown {
			t.Fatal("disk recovery cleared another source")
		}
	}
}

func TestStableSourceKeysDoNotMutateOwnerSnapshots(t *testing.T) {
	t.Parallel()
	items := []Item{{Key: "same", Title: "one"}}
	a := &instanceSource{snapshot: Snapshot{Known: true, Items: items}}
	b := &instanceSource{snapshot: Snapshot{Known: true, Items: items}}
	svc := New(&authority{instance: true}, Sources{Disks: a, Capacity: b})
	first, err := svc.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].Key == first.Items[1].Key || first.Items[0].Key != second.Items[0].Key || items[0].Key != "same" {
		t.Fatalf("keys or source mutated: %+v %+v", first, items)
	}
}

// Malformed source identity cannot leave one apparently fresh condition after
// another row from the same snapshot made its coverage unreliable.
func TestMalformedSnapshotKeysKeepCoverageAndItemsUncertain(t *testing.T) {
	t.Parallel()
	for _, keys := range [][]string{{"disk", "disk"}, {"disk", ""}} {
		source := &instanceSource{snapshot: Snapshot{Known: true, Items: []Item{{Key: keys[0]}, {Key: keys[1]}}}}
		got, err := New(&authority{instance: true}, Sources{Disks: source}).Read(t.Context())
		if err != nil || len(got.Items) != 1 || !got.Items[0].Stale {
			t.Fatalf("ambiguous source appeared fresh: %+v %v", got, err)
		}
		for _, coverage := range got.Sources {
			if coverage.Key == "disks" && !coverage.Unknown {
				t.Fatal("malformed source coverage appeared known")
			}
		}
	}
}

type diskEvidence struct{ value maintenance.DiskSnapshot }

func (d *diskEvidence) Snapshot(context.Context) (maintenance.DiskSnapshot, error) {
	return d.value, nil
}

// Owner warnings, refusal and uncertainty stay distinct; an unreadable disk's
// Refuses bit cannot masquerade as witnessed low space, nor expose raw errors.
func TestDiskSourcePreservesEvidenceAndHidesReadErrors(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	source := Disks{Observer: &diskEvidence{maintenance.DiskSnapshot{ObservedAt: at, Disks: []maintenance.DiskObservation{
		{Reading: diskspace.Reading{What: "worker state", Path: "/state", Low: true, ReadAt: at}, Stale: true, FailedAt: at},
		{Reading: diskspace.Reading{What: "worker cache", Path: "/cache", Refuses: true, Low: true, ReadAt: at}},
		{Reading: diskspace.Reading{What: "controller", Refuses: true, Unknown: "provider secret", ReadAt: at}},
		{Reading: diskspace.Reading{What: "healthy", ReadAt: at}},
	}}}}
	got, err := source.Snapshot(t.Context())
	if err != nil || got.Known || len(got.Items) != 3 {
		t.Fatalf("coverage: %+v %v", got, err)
	}
	if got.Items[1].StatusLabel != labelLow {
		t.Fatalf("low disk became failed: %+v", got.Items[1])
	}
	if got.Items[0].Title != "worker state: Disk check failed" || !got.Items[0].Stale || got.Items[0].ObservedAt != at || got.Items[1].Title != "worker cache: Disk space blocks new work" || got.Items[2].Title != "controller: Disk space could not be assessed" {
		t.Fatalf("condition meaning lost: %+v", got)
	}
	for _, item := range got.Items {
		if strings.Contains(item.Detail, "secret") || item.Href != "/infrastructure" {
			t.Fatalf("unsafe detail/link: %+v", item)
		}
	}
}

func TestDiskKeysAreOpaqueStableAndDistinctInTheWholeView(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	source := Disks{Observer: &diskEvidence{maintenance.DiskSnapshot{ObservedAt: at, Disks: []maintenance.DiskObservation{
		{Reading: diskspace.Reading{What: "worker state", Path: "/private-marker/first", Low: true, ReadAt: at}},
		{Reading: diskspace.Reading{What: "worker state", Path: "/private-marker/second", Low: true, ReadAt: at}},
	}}}}
	s := New(&authority{instance: true}, Sources{Disks: source})
	first, err := s.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-marker") {
		t.Fatalf("global view exposed path: %s", raw)
	}
	if len(first.Items) != 2 || first.Items[0].Key == first.Items[1].Key || first.Items[0].Key != second.Items[0].Key || first.Items[1].Key != second.Items[1].Key {
		t.Fatalf("unstable or colliding identities: %+v %+v", first, second)
	}
}

type capacityTargets struct{ targets []*deployment.Target }

func (c capacityTargets) All(context.Context) ([]*deployment.Target, error) { return c.targets, nil }

type capacityEvidence struct {
	reads map[ids.ID]deployment.CapacitySnapshot
	calls []ids.ID
}

func (c *capacityEvidence) Snapshot(_ context.Context, t *deployment.Target) (deployment.CapacitySnapshot, error) {
	c.calls = append(c.calls, t.ID)
	return c.reads[t.ID], nil
}

func TestCapacitySourceKeepsEachDiskTimeAndDoesNotReadElasticTargets(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	old := now.Add(-30 * time.Minute)
	one, two, elastic := idstest.N(1), idstest.N(2), idstest.N(3)
	evidence := &capacityEvidence{reads: map[ids.ID]deployment.CapacitySnapshot{one: {ObservedAt: now, Stale: true, FailedAt: now, Disk: &deployment.DiskCapacity{Free: deployment.Figure{Known: true, Value: 1 << 30}, Total: deployment.Figure{Known: true, Value: 100 << 30}, Low: true, ReadAt: old}}, two: {Unknown: true, Stale: true, AttemptedAt: now}}}
	source := Capacity{Targets: capacityTargets{[]*deployment.Target{{ID: one, Name: "team host", DisplayLabel: "Office", Kind: deployment.KindDockerHost}, {ID: two, Name: "second", Kind: deployment.KindPortainer}, {ID: elastic, Name: "cloud", Kind: deployment.KindDOApp}}}, Observer: evidence, Now: func() time.Time { return now }}
	got, err := source.Snapshot(t.Context())
	if err != nil || got.Known || !got.Stale || len(got.Items) != 3 || len(evidence.calls) != 2 {
		t.Fatalf("coverage: %+v %v calls%v", got, err, evidence.calls)
	}
	if got.Items[1].ObservedAt != old || !got.Items[1].Stale || got.Items[1].Href != "/infrastructure/team%20host" || !strings.Contains(got.Items[1].Detail, "Office: 1.0 GiB free of 100.0 GiB") {
		t.Fatalf("disk evidence changed: %+v", got.Items[1])
	}
	if got.Items[2].Title != "second: disk space is unknown" {
		t.Fatalf("unread target claimed empty: %+v", got.Items[1])
	}
}

func TestDiskConditionsExplainRoomAndActualAdmissionBoundary(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	source := Disks{Observer: &diskEvidence{maintenance.DiskSnapshot{ObservedAt: at, Disks: []maintenance.DiskObservation{
		{Controller: true, Reading: diskspace.Reading{What: "controller", Low: true, FreeBytes: 2 << 30, TotalBytes: 100 << 30, WarningBytes: 10 << 30, ReadAt: at}},
		{Reading: diskspace.Reading{What: "worker", Unknown: "private error", ReadAt: at}},
	}}}}
	got, err := source.Snapshot(t.Context())
	if err != nil || len(got.Items) != 2 {
		t.Fatalf("conditions %+v %v", got, err)
	}
	if !strings.Contains(got.Items[0].Detail, "2.0 GiB free of 100.0 GiB") || !strings.Contains(got.Items[0].Detail, "controller admission is not protected") {
		t.Fatalf("controller guarantee %+v", got.Items[0])
	}
	if !strings.Contains(got.Items[1].Detail, "refuses new tasks") || strings.Contains(got.Items[1].Detail, "private error") {
		t.Fatalf("worker guarantee %+v", got.Items[1])
	}
}

func TestCapacityConditionsRespectRetirementAndProviderAdmission(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	portainer, retired, unknown := idstest.N(7), idstest.N(8), idstest.N(9)
	evidence := &capacityEvidence{reads: map[ids.ID]deployment.CapacitySnapshot{
		portainer: {ObservedAt: at, Disk: &deployment.DiskCapacity{Low: true, Refuses: true, Free: deployment.Figure{Known: true, Value: 1 << 30}, Total: deployment.Figure{Known: true, Value: 100 << 30}, ReadAt: at}},
		unknown:   {Unknown: true, Disk: &deployment.DiskCapacity{ReadAt: at}},
	}}
	source := Capacity{Targets: capacityTargets{[]*deployment.Target{
		{ID: portainer, Name: "office", Kind: deployment.KindPortainer},
		{ID: retired, Name: "removed", Kind: deployment.KindDockerHost, Status: deployment.TargetRetired},
		{ID: unknown, Name: "unread", Kind: deployment.KindDockerHost},
	}}, Observer: evidence, Now: func() time.Time { return at }}
	got, err := source.Snapshot(t.Context())
	if err != nil || len(got.Items) != 2 || len(evidence.calls) != 2 {
		t.Fatalf("retired target sampled %+v calls%v %v", got, evidence.calls, err)
	}
	if strings.Contains(got.Items[0].Title, "blocks") || !strings.Contains(got.Items[0].Detail, "advisory: Portainer") || strings.Contains(got.Items[0].Detail, "are refused") {
		t.Fatalf("invented Portainer admission %+v", got.Items[0])
	}
	if got.Items[1].Title != "unread: disk space is unknown" || !strings.Contains(got.Items[1].Detail, "disk cannot be read") {
		t.Fatalf("unknown disk lost actual admission boundary %+v", got.Items[1])
	}
}

// Snapshot coverage must not change the assessment of a fresh disk.
func TestCapacityAssessesDiskIndependentlyOfSnapshotCoverage(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	id := idstest.N(10)
	for _, snapshot := range []deployment.CapacitySnapshot{
		{Unknown: true, ObservedAt: at, Reason: "Container memory use could not all be read."}, // memory failed
		{Stale: true, ObservedAt: at.Add(-3 * time.Minute)},
	} {
		snapshot.Disk = &deployment.DiskCapacity{Free: deployment.Figure{Known: true, Value: 50 << 30}, Total: deployment.Figure{Known: true, Value: 100 << 30}, ReadAt: at}
		evidence := &capacityEvidence{reads: map[ids.ID]deployment.CapacitySnapshot{id: snapshot}}
		source := Capacity{Targets: capacityTargets{[]*deployment.Target{{ID: id, Name: "office", Kind: deployment.KindDockerHost}}}, Observer: evidence, Now: func() time.Time { return at }}
		got, err := source.Snapshot(t.Context())
		if err != nil || got.Known == snapshot.Unknown || got.Stale != snapshot.Stale || len(got.Items) != 0 || (snapshot.Reason != "" && got.Reason != "office: "+snapshot.Reason) {
			t.Fatalf("snapshot status became disk failure: %+v %v", got, err)
		}
		snapshot.Disk.Low = true
		got, err = source.Snapshot(t.Context())
		if err != nil || len(got.Items) != 1 || got.Items[0].Stale || got.Items[0].ObservedAt != at {
			t.Fatalf("fresh pressure inherited snapshot status: %+v %v", got, err)
		}
		snapshot.Disk.Low = false
		snapshot.Disk.ReadAt = at.Add(-30 * time.Minute)
		snapshot.FailedAt = at
		evidence.reads[id] = snapshot
		got, err = source.Snapshot(t.Context())
		if err != nil || len(got.Items) != 1 || !got.Items[0].Stale || !strings.HasSuffix(got.Items[0].Key, "/disk-unassessed") {
			t.Fatalf("stale disk hidden: %+v %v", got, err)
		}
	}
}

func TestSourceReadErrorsUseOnlyOwnerEvidence(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	auth := &authority{instance: true}
	source := &instanceSource{snapshot: Snapshot{Known: true, ObservedAt: at, Items: []Item{{Key: "old-generation", Title: "disk low", ObservedAt: at}}}}
	svc := New(auth, Sources{Disks: source})
	read := func() View {
		t.Helper()
		v, err := svc.Read(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	read()
	source.snapshot = Snapshot{}
	source.err = errors.New("database private endpoint")
	failed := read()
	if len(failed.Items) != 0 {
		t.Fatalf("source error resurrected a process-memory reading: %+v", failed)
	}
	auth.instance = false
	calls := source.calls
	if denied := read(); len(denied.Items) != 0 || source.calls != calls {
		t.Fatalf("revoked source fell back %+v", denied)
	}
	auth.instance = true
	source.err = fault.New(fault.Forbidden, "newer revocation")
	if denied, err := svc.Read(t.Context()); fault.KindOf(err) != fault.Forbidden || len(denied.Items) != 0 {
		t.Fatalf("owner refusal used fallback %+v %v", denied, err)
	}
	source.err = nil
	// A successful unknown answer can mean a changed generation; it invalidates
	// old evidence just as a successful recovery does.
	source.snapshot = Snapshot{Known: false}
	if changed := read(); len(changed.Items) != 0 {
		t.Fatalf("successful replacement resurrected prior condition %+v", changed)
	}
	source.err = errors.New("unavailable")
	if later := read(); len(later.Items) != 0 {
		t.Fatalf("outage resurrected replaced generation %+v", later)
	}
	source.err = nil
	source.snapshot = Snapshot{Known: true, ObservedAt: at, Items: []Item{{Key: "new", Title: "new warning"}}}
	read()
	source.snapshot = Snapshot{Known: true, ObservedAt: at.Add(time.Minute)}
	read()
	source.err = errors.New("unavailable")
	if recovered := read(); len(recovered.Items) != 0 {
		t.Fatalf("outage resurrected recovered condition %+v", recovered)
	}
}

type instanceRead func(context.Context) (Snapshot, error)

func (f instanceRead) Snapshot(ctx context.Context) (Snapshot, error) { return f(ctx) }

func TestLateSourceSuccessCannotResurrectAConditionAfterNewerRecovery(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	svc := New(&authority{instance: true}, Sources{Disks: instanceRead(func(ctx context.Context) (Snapshot, error) {
		switch calls.Add(1) {
		case 1:
			return Snapshot{Known: true, Items: []Item{{Key: "warning"}}}, nil
		case 2:
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return Snapshot{}, ctx.Err()
			}
			return Snapshot{Known: true, Items: []Item{{Key: "old-warning"}}}, nil
		case 3:
			return Snapshot{Known: true}, nil
		default:
			return Snapshot{}, errors.New("source unavailable")
		}
	})})
	if _, err := svc.Read(t.Context()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := svc.Read(t.Context()); done <- err }()
	<-started
	recovered, err := svc.Read(t.Context())
	close(release)
	if oldErr := <-done; oldErr != nil {
		t.Fatal(oldErr)
	}
	if err != nil || len(recovered.Items) != 0 {
		t.Fatalf("newer recovery %+v %v", recovered, err)
	}
	after, err := svc.Read(t.Context())
	if err != nil || len(after.Items) != 0 {
		t.Fatalf("late successful read replaced newer recovery %+v %v", after, err)
	}
}

type projectRead func(context.Context, ids.ID) (Snapshot, error)

func (f projectRead) ProjectSnapshot(ctx context.Context, id ids.ID) (Snapshot, error) {
	return f(ctx, id)
}

func TestOwnerRevocationStopsOtherProjectSources(t *testing.T) {
	t.Parallel()
	id := idstest.N(15)
	later := &projectSource{snapshots: map[ids.ID]Snapshot{id: {Known: true, Items: []Item{{Key: "hidden"}}}}}
	svc := New(&authority{projects: []ids.ID{id}}, Sources{
		Deployments: projectRead(func(context.Context, ids.ID) (Snapshot, error) {
			return Snapshot{}, fault.New(fault.Forbidden, "revoked after initial check")
		}),
		Protection: later,
	})
	got, err := svc.Read(t.Context())
	if fault.KindOf(err) != fault.Forbidden || len(got.Items) != 0 || len(later.calls) != 0 {
		t.Fatalf("owner revocation allowed later collection %+v %v calls%v", got, err, later.calls)
	}
}

// A failed current read must be visible even when its retained evidence was
// healthy. Old pressure remains visible alongside the unread state until a
// successful read recovers that exact filesystem.
func TestDiskUnreadRetainsPressureAndExplainsEveryFilesystem(t *testing.T) {
	for _, kind := range []string{"controller", "worker state", "worker Docker"} {
		for _, pressure := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/healthy", true: "/low"}[pressure], func(t *testing.T) {
				at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
				evidence := &diskEvidence{maintenance.DiskSnapshot{ObservedAt: at, Unknown: true, Disks: []maintenance.DiskObservation{{Controller: kind == "controller", Stale: true, Unknown: true, Reading: diskspace.Reading{What: kind, Path: "/owned", ReadAt: at, Low: pressure, Refuses: pressure && kind != "controller"}}}}}
				source := Disks{Observer: evidence}
				got, err := source.Snapshot(t.Context())
				if err != nil || got.Known || len(got.Items) != 1 || !got.Items[0].Stale || got.Items[0].ObservedAt != at || !strings.Contains(got.Items[0].Detail, "latest disk reading is unavailable") {
					t.Fatalf("unread filesystem disappeared or renewed evidence: %+v %v", got, err)
				}
				item := got.Items[0]
				if pressure && !strings.Contains(item.Detail, "Last successful reading") {
					t.Fatalf("previous pressure disappeared: %+v", item)
				}
				if kind == "controller" {
					if !strings.Contains(item.Detail, "controller admission is not protected") || strings.Contains(item.Detail, "worker refuses") {
						t.Fatalf("invented controller admission: %+v", item)
					}
				} else if !strings.Contains(item.Detail, "worker refuses new tasks") {
					t.Fatalf("missing actual worker admission: %+v", item)
				}
				evidence.value = maintenance.DiskSnapshot{ObservedAt: at.Add(time.Minute), Disks: []maintenance.DiskObservation{{Controller: kind == "controller", Reading: diskspace.Reading{What: kind, Path: "/owned", ReadAt: at.Add(time.Minute)}}}}
				got, err = source.Snapshot(t.Context())
				if err != nil || !got.Known || len(got.Items) != 0 {
					t.Fatalf("recovered filesystem still warned: %+v %v", got, err)
				}
			})
		}
	}
}

// Pending coverage must not inflate Attention; failures and stale healthy disks must.
func TestCapacityPendingCheckIsNotAnAttentionItem(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	id := idstest.N(42)
	evidence := &capacityEvidence{reads: map[ids.ID]deployment.CapacitySnapshot{id: {Unknown: true, Stale: true}}}
	source := Capacity{Targets: capacityTargets{[]*deployment.Target{{ID: id, Name: "demo-docker", Kind: deployment.KindDockerHost}}}, Observer: evidence, Now: func() time.Time { return at }}
	got, err := source.Snapshot(t.Context())
	if err != nil || got.Known || len(got.Items) != 0 {
		t.Fatalf("pending check raised attention: %+v %v", got, err)
	}
	evidence.reads[id] = deployment.CapacitySnapshot{Unknown: true, AttemptedAt: at}
	got, err = source.Snapshot(t.Context())
	if err != nil || len(got.Items) != 1 || got.Items[0].Title != "demo-docker: disk space is unknown" || got.Items[0].ObservedAt != at {
		t.Fatalf("failed first check hidden: %+v %v", got, err)
	}
	evidence.reads[id] = deployment.CapacitySnapshot{ObservedAt: at, Stale: true, Disk: &deployment.DiskCapacity{Free: deployment.Figure{Known: true, Value: 50}, Total: deployment.Figure{Known: true, Value: 100}, ReadAt: at.Add(-30 * time.Minute)}}
	got, err = source.Snapshot(t.Context())
	if err != nil || len(got.Items) != 0 {
		t.Fatalf("stale healthy check raised an item: %+v %v", got, err)
	}
}

// Coverage carries only the owner's safe failure summary for presentation.
func TestCoverageReportsSafeFailureReason(t *testing.T) {
	var view View
	view.add("capacity", "Capacity", Snapshot{Reason: "Container memory use could not all be read."}, nil)
	if view.Sources[0].Reason != "Container memory use could not all be read." {
		t.Fatalf("reason lost: %+v", view)
	}
	view = View{}
	view.add("capacity", "Capacity", Snapshot{Reason: "old reason"}, errors.New("provider secret"))
	if view.Sources[0].Reason != "The source check failed." {
		t.Fatalf("raw error leaked or failure hidden: %+v", view)
	}
}

func TestRetainedKnownFailureIsStaleWithoutSerializingAsUnknown(t *testing.T) {
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, unknown := range []bool{false, true} {
		var view View
		view.add("capacity", "Capacity", Snapshot{Known: !unknown, Stale: true, ObservedAt: at, Items: []Item{{Key: "host", Stale: true, Unknown: unknown, ObservedAt: at}}}, nil)
		raw, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Items []struct {
				Status string `json:"statusLabel"`
				Stale  bool   `json:"stale"`
			}
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		want := "waiting"
		if unknown {
			want = "unknown"
		}
		if len(got.Items) != 1 || got.Items[0].Status != want || !got.Items[0].Stale {
			t.Fatalf("age conflated with unknown: %s", raw)
		}
	}
}

type scopedAuthority struct {
	allow   map[access.Capability]bool
	project ids.ID
}

func (a scopedAuthority) VisibleProjects(_ context.Context, cap access.Capability) ([]ids.ID, error) {
	if a.allow[cap] {
		return []ids.ID{a.project}, nil
	}
	return nil, nil
}
func (a scopedAuthority) Require(_ context.Context, p access.Permission) (access.Caller, error) {
	if a.allow[p.Capability] {
		return access.Caller{}, nil
	}
	return access.Caller{}, fault.New(fault.Forbidden, "denied")
}

func TestAttentionCollectsIntegrationsAndChecksUnderTheirOwnPermissions(t *testing.T) {
	t.Parallel()
	id := idstest.N(71)
	integration := &instanceSource{snapshot: Snapshot{Known: true, Items: []Item{{Key: "registry", Title: "Registry failed", Href: "/integrations/registry"}}}}
	checks := &batchChecks{}
	deployments := &projectSource{}
	auth := scopedAuthority{project: id, allow: map[access.Capability]bool{access.CapProjectRead: true, access.CapConformanceRead: true}}
	svc := New(auth, Sources{Integrations: integration, Checks: checks, Deployments: deployments})
	got, err := svc.Read(t.Context())
	if err != nil || len(got.Items) != 2 || integration.calls != 1 || len(checks.calls) != 1 || len(deployments.calls) != 0 {
		t.Fatalf("new sources: %+v %v calls=%d/%v/%v", got, err, integration.calls, checks.calls, deployments.calls)
	}
	for _, s := range got.Sources {
		if strings.HasPrefix(s.Key, "deployments") || strings.HasPrefix(s.Key, "protection") {
			t.Fatalf("deployment coverage leaked: %+v", s)
		}
	}
	auth.allow[access.CapProjectRead] = false
	auth.allow[access.CapConformanceRead] = false
	got, err = svc.Read(t.Context())
	if err != nil || len(got.Items) != 0 || len(got.Sources) != 0 || integration.calls != 1 || len(checks.calls) != 1 {
		t.Fatalf("revoked new sources: %+v %v", got, err)
	}
}

type batchChecks struct{ calls []ids.ID }

func (s *batchChecks) Snapshot(_ context.Context, projects []ids.ID) (Snapshot, error) {
	s.calls = append(s.calls, projects...)
	return Snapshot{Known: true, Items: []Item{{Key: "confirmed", Title: "Calendar checks", Count: 2}}}, nil
}

func TestDefaultNextStepNamesAnActionForAnyReader(t *testing.T) {
	t.Parallel()
	var view View
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	view.add("protection/project", "Backups", Snapshot{AttemptedAt: at}, nil)
	if view.Sources[0].AttemptedAt != at {
		t.Fatalf("last attempt lost: %+v", view.Sources[0])
	}
	if got := view.Sources[0].NextStep; got != "Open the resource to check its settings." {
		t.Fatalf("next step: %q", got)
	}
}
