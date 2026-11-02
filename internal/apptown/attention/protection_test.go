package attention

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type protectionProject struct{}

func (protectionProject) ByID(context.Context, ids.ID) (project.Project, error) {
	return project.Project{Slug: "team-app"}, nil
}

type protectionDeployments struct{ rows []*deployment.Deployment }

func (p protectionDeployments) ForProject(context.Context, ids.ID) ([]*deployment.Deployment, error) {
	return p.rows, nil
}

type protectionEvidence struct {
	value dataprotection.ProtectionSnapshot
	calls int
	err   error
}

func (p *protectionEvidence) Snapshot(context.Context, ids.ID) (dataprotection.ProtectionSnapshot, error) {
	p.calls++
	return p.value, p.err
}

// The view must keep the owner's copy classification and distinguish missing
// recovery evidence from an observed failure or an interrupted unknown result.
func TestProtectionSourceDistinguishesMissingFailedAndUnknownRecovery(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, outcome string
		recorded      bool
		severity      string
		known         bool
	}{
		{"never", "", false, severityAdvisory, true},
		{"failed", dataprotection.RehearsalFailed, true, SeverityWarning, true},
		{"interrupted", dataprotection.RehearsalUnknown, true, SeverityWarning, false},
		{"passed", dataprotection.RehearsalPassed, true, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evidence := &protectionEvidence{value: dataprotection.ProtectionSnapshot{ObservedAt: at, OffHost: true, Items: []dataprotection.ProtectionItemObservation{{Key: "data", Need: "database", Copy: dataprotection.CopyFresh}}, Rehearsed: tc.recorded, RehearsalPassed: tc.outcome == dataprotection.RehearsalPassed, RehearsalOutcome: tc.outcome, RehearsalAt: at, RestoreTested: tc.recorded, RestoreOutcome: tc.outcome, RestoreAt: at, AttemptedAt: at.Add(time.Minute)}}
			source := Protection{Projects: protectionProject{}, Deployments: protectionDeployments{[]*deployment.Deployment{{ID: idstest.N(1), Kind: deployment.Standing, DataClass: "production", DisplayLabel: "Office"}}}, Observer: evidence, Now: func() time.Time { return at }}
			got, err := source.ProjectSnapshot(t.Context(), idstest.N(2))
			if err != nil || got.Known != tc.known {
				t.Fatalf("coverage %+v %v", got, err)
			}
			want := 2
			if tc.severity == "" {
				want = 0
			}
			if len(got.Items) != want {
				t.Fatalf("recovery conditions %+v", got.Items)
			}
			for _, item := range got.Items {
				if !item.AttemptedAt.IsZero() {
					t.Fatalf("recovery acquired the coverage attempt time: %+v", item)
				}
				if !strings.Contains(item.Title, "Office: ") {
					t.Fatalf("unnamed condition: %+v", item)
				}
				if item.Severity != tc.severity || item.ObservedAt != at || item.Href != "/projects/team-app/settings/deployments" {
					t.Fatalf("evidence %+v", item)
				}
				if tc.name == "interrupted" && strings.Contains(item.Title, "failed") {
					t.Fatalf("unknown claimed failure %+v", item)
				}
			}
		})
	}
}

func TestProtectionSourcePreservesStaleCopiesAndExcludesDisposableRequirements(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	evidence := &protectionEvidence{value: dataprotection.ProtectionSnapshot{ObservedAt: at, Stale: true, OffHost: true, Rehearsed: true, RehearsalPassed: true, RestoreTested: true, RestoreOutcome: dataprotection.RehearsalPassed, Items: []dataprotection.ProtectionItemObservation{
		{Key: "stale", Need: "database", Copy: dataprotection.CopyStale, CopyAt: at.Add(-time.Hour)},
		{Key: "none", Need: "files", Copy: dataprotection.CopyNone},
		{Key: "disposable", Need: "cache", Copy: dataprotection.CopyDisposable},
	}}}
	dep := &deployment.Deployment{ID: idstest.N(1), Kind: deployment.Standing, Name: "Office", DataClass: "production", DesiredGeneration: 3, ObservedGeneration: 2}
	source := Protection{Projects: protectionProject{}, Deployments: protectionDeployments{[]*deployment.Deployment{dep}}, Observer: evidence, Now: func() time.Time { return at.Add(time.Minute) }}
	got, err := source.ProjectSnapshot(t.Context(), idstest.N(2))
	if err != nil || len(got.Items) != 2 || !got.Stale || got.ObservedAt != at {
		t.Fatalf("copy evidence %+v %v", got, err)
	}
	for _, item := range got.Items {
		if !strings.Contains(item.Title, "Office: ") {
			t.Fatalf("unnamed condition: %+v", item)
		}
		if !item.Stale || item.Severity != SeverityWarning || !strings.Contains(item.Key, "/2/3/") {
			t.Fatalf("copy identity %+v", item)
		}
	}
	dep.DataClass = "development"
	evidence.value.Unknown = true
	got, err = source.ProjectSnapshot(t.Context(), idstest.N(2))
	if err != nil || got.Known || len(got.Items) != 0 {
		t.Fatalf("nonproduction acquired a backup requirement %+v %v", got, err)
	}
}

// A queued production protection check is coverage only. Failed checks and
// stale completed readings must raise a warning, even with no copy items.
func TestProtectionFirstCheckAndStaleReading(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		reading   dataprotection.ProtectionSnapshot
		wantItems int
	}{
		{"pending", dataprotection.ProtectionSnapshot{Unknown: true, Stale: true}, 0},
		{"failed", dataprotection.ProtectionSnapshot{Unknown: true, Stale: true, AttemptedAt: at}, 1},
		{"stale", dataprotection.ProtectionSnapshot{ObservedAt: at, Stale: true}, 0},
		{"fresh", dataprotection.ProtectionSnapshot{ObservedAt: at}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := Protection{Projects: protectionProject{}, Deployments: protectionDeployments{[]*deployment.Deployment{{ID: idstest.N(1), Kind: deployment.Standing, DataClass: "production"}}}, Observer: &protectionEvidence{value: tc.reading}, Now: func() time.Time { return at.Add(time.Hour) }}
			got, err := source.ProjectSnapshot(t.Context(), idstest.N(2))
			if err != nil || got.Known == tc.reading.Unknown || got.Stale != tc.reading.Stale || len(got.Items) != tc.wantItems {
				t.Fatalf("protection coverage: %+v %v", got, err)
			}
			if tc.wantItems > 0 && got.Items[0].AttemptedAt != tc.reading.AttemptedAt {
				t.Fatalf("coverage attempt time lost: %+v", got.Items[0])
			}
			if tc.wantItems > 0 && (!strings.Contains(got.Items[0].Title, "Backups not known") || !got.Items[0].Stale) {
				t.Fatalf("protection warning: %+v", got.Items)
			}
		})
	}
}

// Coverage uses the same public deployment label as conditions and never calls
// a first check a failed read. Every failed attempt retains its safe reason.
func TestProtectionCoverageWords(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, label string
		readErr     error
		nextStep    string
		reading     dataprotection.ProtectionSnapshot
		want        string
	}{
		{"first", "Office", nil, "AppTown schedules a background check every five minutes.", dataprotection.ProtectionSnapshot{Unknown: true, Stale: true}, "Office: Backups have not been checked yet."},
		{"failed", "Office", nil, "Open deployment settings to check the deployment.", dataprotection.ProtectionSnapshot{Unknown: true, Stale: true, AttemptedAt: at, Reason: "The protection check timed out."}, "Office: The protection check timed out."},
		{"fallback name", "", nil, "AppTown schedules a background check every five minutes.", dataprotection.ProtectionSnapshot{Unknown: true, Stale: true}, "internal-name: Backups have not been checked yet."},
		{"changed", "Office", nil, "Open deployment settings to check the deployment.", dataprotection.ProtectionSnapshot{Unknown: true, Stale: true, ObservedAt: at, AttemptedAt: at, Reason: "The deployment changed. Waiting for a protection check."}, "Office: The deployment changed. Waiting for a protection check."},
		{"recorded check unreadable", "Office", errors.New("private database details"), "Reload the page to try reading the recorded backup check again.", dataprotection.ProtectionSnapshot{Unknown: true}, "Office: The recorded backup check could not be read."},
		{"absent", "Office", nil, "Open deployment settings to check the deployment.", dataprotection.ProtectionSnapshot{Unknown: true, AttemptedAt: at, Reason: "The data this deployment names is not on its host.", NextStep: "Open deployment settings to check the deployment."}, "Office: The data this deployment names is not on its host."},
		{"retry", "Office", nil, "The background check will try again in five minutes.", dataprotection.ProtectionSnapshot{Unknown: true, AttemptedAt: at, Reason: "AppTown could not read what this deployment holds.", NextStep: "The background check will try again in five minutes."}, "Office: AppTown could not read what this deployment holds."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := Protection{Projects: protectionProject{}, Deployments: protectionDeployments{[]*deployment.Deployment{{ID: idstest.N(1), Kind: deployment.Standing, Name: "internal-name", DisplayLabel: tc.label}}}, Observer: &protectionEvidence{value: tc.reading, err: tc.readErr}, Now: func() time.Time { return at }}
			got, err := source.ProjectSnapshot(t.Context(), idstest.N(2))
			if err != nil || got.Reason != tc.want || got.AttemptedAt != tc.reading.AttemptedAt || got.NextStep != strings.Split(tc.want, ": ")[0]+": "+tc.nextStep {
				t.Fatalf("coverage: %+v %v", got, err)
			}
		})
	}
}

// byDeployment answers each deployment's own backup reading.
type byDeployment map[ids.ID]dataprotection.ProtectionSnapshot

func (b byDeployment) Snapshot(_ context.Context, id ids.ID) (dataprotection.ProtectionSnapshot, error) {
	return b[id], nil
}

// A healthy deployment beside a failing one adds no next step: the row names
// only the deployment that needs something.
func TestProtectionNextStepNamesOnlyDeploymentsWithAProblem(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	healthy, failing := idstest.N(1), idstest.N(3)
	source := Protection{
		Projects: protectionProject{},
		Deployments: protectionDeployments{[]*deployment.Deployment{
			{ID: healthy, Kind: deployment.Standing, Name: "main", DisplayLabel: "Shop"},
			{ID: failing, Kind: deployment.Standing, Name: "office", DisplayLabel: "Office"},
		}},
		Observer: byDeployment{
			healthy: {ObservedAt: at, AttemptedAt: at},
			failing: {Unknown: true, AttemptedAt: at, Reason: "The data this deployment names is not on its host.", NextStep: "Open deployment settings to check the deployment."},
		},
		Now: func() time.Time { return at },
	}
	got, err := source.ProjectSnapshot(t.Context(), idstest.N(2))
	if err != nil {
		t.Fatal(err)
	}
	if got.NextStep != "Office: Open deployment settings to check the deployment." || strings.Contains(got.Reason, "Shop") {
		t.Fatalf("coverage named the healthy deployment: %+v", got)
	}
}
