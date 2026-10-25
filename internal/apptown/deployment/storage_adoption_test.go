package deployment

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
)

func storageAdoptionFixture(t *testing.T) (*Service, *Deployment, *dockerfake.Daemon, *requestAuthority) {
	t.Helper()
	s, _, daemon, _, pid := hostWorld(t, "")
	authority := &requestAuthority{}
	s.authority = authority
	raw := resolvedStackFixture + "  redis: {}\n"
	s.release.Sources = &revisionSources{by: map[string]string{"running": raw}}
	target, err := s.Targets.ByName(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.EnsureStanding(t.Context(), pid, "main", target.ID, Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err := s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: target.ID, Key: "initial", Inputs: Inputs{Revision: "running"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Outcomes.Complete(t.Context(), Completion{Attempt: attempt, Observed: deploy.Observation{Generation: attempt.Generation, Rollout: "active", Health: "healthy"}}); err != nil {
		t.Fatal(err)
	}
	dep, err = s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := appcontract.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := s.env.ResolveDockerStack(t.Context(), contract, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	names, err := dep.HostNames()
	if err != nil {
		t.Fatal(err)
	}
	for _, need := range stack.Needs {
		if need.KeepsImage && need.Data != "" {
			daemon.SetVolume(names.NeedVolume(need.Name), true)
			daemon.SetStorageContainer(names.Service(need.Name), need.Image, names.NeedVolume(need.Name), need.Data)
			daemon.SetRunning(names.Service(need.Name), true)
		}
	}
	daemon.ResetCommands()
	return s, dep, daemon, authority
}

func TestStorageAdoptionFreshAtomicEvidence(t *testing.T) {
	s, dep, daemon, authority := storageAdoptionFixture(t)
	ctx := t.Context()
	owner := s.Deployments.StorageOf(dep)
	preview, err := s.PreviewStorage(ctx, dep.ProjectID, dep.Name)
	if err != nil || !preview.Complete || preview.Adopted || len(preview.Needs) != 2 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	if got := authority.seen[len(authority.seen)-1]; got.Capability != access.CapDeploymentRead || got.Context.Consequential {
		t.Fatalf("preview permission: %+v", got)
	}
	for _, row := range preview.Needs {
		if _, found, err := owner.Get(ctx, row.Need, row.Volume); err != nil || found {
			t.Fatalf("preview wrote binding: %t %v", found, err)
		}
	}
	// A broken second resource must not commit the first candidate.
	broken := preview.Needs[1]
	daemon.SetVolume(broken.Volume, false)
	if _, err = s.AdoptStorage(ctx, dep.ProjectID, dep.Name); err == nil {
		t.Fatal("missing engine data adopted")
	}
	for _, row := range preview.Needs {
		if _, found, err := owner.Get(ctx, row.Need, row.Volume); err != nil || found {
			t.Fatalf("partial adoption: %t %v", found, err)
		}
	}
	daemon.SetVolume(broken.Volume, true)
	dbtest.FailNextStatement(t, s.db, "INSERT INTO audit_log")
	if _, err = s.AdoptStorage(ctx, dep.ProjectID, dep.Name); err == nil {
		t.Fatal("audit failure adopted")
	}
	for _, row := range preview.Needs {
		if _, found, err := owner.Get(ctx, row.Need, row.Volume); err != nil || found {
			t.Fatalf("audit failure left binding: %t %v", found, err)
		}
	}
	adopted, err := s.AdoptStorage(ctx, dep.ProjectID, dep.Name)
	if err != nil || !adopted.Adopted {
		t.Fatalf("adoption: %+v %v", adopted, err)
	}
	if got := authority.seen[len(authority.seen)-1]; got.Capability != access.CapDeploymentOperate || !got.Context.Consequential {
		t.Fatalf("adopt permission: %+v", got)
	}
	for _, row := range adopted.Needs {
		b, found, err := owner.Get(ctx, row.Need, row.Volume)
		if err != nil || !found || !sameStorage(b, *row.Observed) {
			t.Fatalf("committed evidence: %+v %t %v", b, found, err)
		}
	}
	// A normal bound boot starts by immutable ID, not the original tag.
	names, _ := dep.HostNames()
	contract, _ := appcontract.Parse([]byte(resolvedStackFixture + "  redis: {}\n"))
	stack, _ := s.env.ResolveDockerStack(ctx, contract, nil, nil)
	for _, need := range stack.Needs {
		for _, row := range adopted.Needs {
			if need.Name == row.Need {
				daemon.SetStorageContainer(names.Service(need.Name), row.Observed.ImageID, row.Volume, need.Data)
			}
		}
	}
	if _, err = s.AdoptStorage(ctx, dep.ProjectID, dep.Name); err != nil {
		t.Fatalf("idempotent readback: %v", err)
	}
	// A conflicting physical incarnation must never overwrite an accepted row.
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 1 && args[0] == "volume" && args[1] == "inspect" {
			return []byte(`{"Name":"changed","Driver":"local","CreatedAt":"2026-10-25T00:00:00Z"}`), nil, true
		}
		return nil, nil, false
	})
	if _, err = s.AdoptStorage(ctx, dep.ProjectID, dep.Name); err == nil {
		t.Fatal("conflicting incarnation adopted")
	}
	for _, row := range adopted.Needs {
		b, found, err := owner.Get(ctx, row.Need, row.Volume)
		if err != nil || !found || !sameStorage(b, *row.Observed) {
			t.Fatalf("conflict altered accepted binding: %+v %t %v", b, found, err)
		}
	}
	for _, args := range daemon.Commands() {
		if !strings.Contains(strings.Join(args, " "), "inspect") {
			t.Fatalf("adoption mutated Docker: %v", args)
		}
	}
}

func TestStorageAdoptionRefusesBeforeObservation(t *testing.T) {
	for _, scenario := range []string{"authority", "source", "restore", "data-operation", "attempt"} {
		t.Run(scenario, func(t *testing.T) {
			s, dep, daemon, authority := storageAdoptionFixture(t)
			switch scenario {
			case "authority":
				authority.deny = errors.New("denied")
			case "source":
				s.release.Sources = &revisionSources{by: map[string]string{"latest": resolvedStackFixture}}
			case "restore":
				if err := s.Deployments.HoldForFill(t.Context(), []ids.ID{dep.ID}); err != nil {
					t.Fatal(err)
				}
			case "data-operation":
				if err := jobs.Migrate(t.Context(), s.db.Pool()); err != nil {
					t.Fatal(err)
				}
				ops := operations.New(s.db, nil, nil, nil)
				ops.Register(operations.Kind{Name: "test.restore-set", Capability: access.CapDeploymentOperate, Execute: func(context.Context, operations.Operation, *operations.StepLog) operations.Outcome {
					t.Error("admission executed restore")
					return operations.Outcome{}
				}})
				_, err := database.Transact(t.Context(), s.db, func(tx *database.Tx) (*operations.Operation, error) {
					if err := LockProjectIn(t.Context(), tx, dep.ProjectID); err != nil {
						return nil, err
					}
					return ops.AdmitIn(t.Context(), tx, operations.Spec{Kind: "test.restore-set", ProjectID: dep.ProjectID, Subject: SubjectOf(dep), Holds: []string{DataHold(dep)}, Authority: operations.System("storage-fixture")})
				})
				if err != nil {
					t.Fatal(err)
				}
			case "attempt":
				if _, _, err := s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "pending", Inputs: Inputs{Revision: "running"}}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.AdoptStorage(t.Context(), dep.ProjectID, dep.Name); err == nil {
				t.Fatal("unsafe adoption allowed")
			}
			if len(daemon.Commands()) != 0 {
				t.Fatalf("refusal reached provider: %v", daemon.Commands())
			}
		})
	}
}

type heldStorageInspection struct {
	docker.Client
	once             sync.Once
	entered, release chan struct{}
}

func (h *heldStorageInspection) Run(ctx context.Context, host string, cmd docker.Command) ([]byte, error) {
	h.once.Do(func() {
		close(h.entered)
		select {
		case <-h.release:
		case <-ctx.Done():
		}
	})
	return h.Client.Run(ctx, host, cmd)
}

func TestStorageAdoptionExcludesAttemptAdmission(t *testing.T) {
	s, dep, daemon, _ := storageAdoptionFixture(t)
	held := &heldStorageInspection{Client: daemon, entered: make(chan struct{}), release: make(chan struct{})}
	s.hosts.settings.Scope.Client = held
	adopted := make(chan error, 1)
	go func() { _, err := s.AdoptStorage(t.Context(), dep.ProjectID, dep.Name); adopted <- err }()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("adoption never inspected")
	}
	defer func() {
		select {
		case <-held.release:
		default:
			close(held.release)
		}
	}()
	started := make(chan struct{})
	admitted := make(chan error, 1)
	go func() {
		close(started)
		_, _, err := s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "later", Inputs: Inputs{Revision: "running"}})
		admitted <- err
	}()
	<-started
	select {
	case err := <-admitted:
		t.Fatalf("attempt passed locked inspection: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(held.release)
	if err := <-adopted; err != nil {
		t.Fatal(err)
	}
	if err := <-admitted; err != nil {
		t.Fatal(err)
	}
}

// A supported custom-catalogue update cannot make an established engine vanish
// from the preview simply by clearing its new selector.
func TestStorageAdoptionPreservesBindingAcrossCustomCatalogueChange(t *testing.T) {
	for _, scenario := range []string{"engine-flag", "data-path"} {
		t.Run(scenario, func(t *testing.T) {
			s, dep, daemon, _ := storageAdoptionFixture(t)
			raw, err := stock.FS.ReadFile("dependencies/postgres.yaml")
			if err != nil {
				t.Fatal(err)
			}
			yaml := strings.Replace(string(raw), "capability: postgres", "capability: custom-db", 1)
			if _, err = s.env.needs.Save(t.Context(), yaml, "alice"); err != nil {
				t.Fatal(err)
			}
			contractYAML := strings.Replace(resolvedStackFixture, "postgres: {}", "custom-db: {}", 1)
			s.release.Sources = &revisionSources{by: map[string]string{"running": contractYAML}}
			contract, err := appcontract.Parse([]byte(contractYAML))
			if err != nil {
				t.Fatal(err)
			}
			stack, err := s.env.ResolveDockerStack(t.Context(), contract, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			names, _ := dep.HostNames()
			need := stack.Needs[0]
			volume := names.NeedVolume(need.Name)
			daemon.SetVolume(volume, true)
			daemon.SetRunning(names.Service(need.Name), true)
			daemon.SetStorageContainer(names.Service(need.Name), need.Image, volume, need.Data)
			before, err := s.AdoptStorage(t.Context(), dep.ProjectID, dep.Name)
			if err != nil || len(before.Needs) != 1 {
				t.Fatalf("initial custom binding: %+v %v", before, err)
			}
			changed := strings.Replace(yaml, "keepsImage: true", "keepsImage: false", 1)
			if scenario == "data-path" {
				changed = strings.Replace(yaml, "data: "+need.Data, "data: \"\"", 1)
			}
			if changed == yaml {
				t.Fatal("fixture did not change catalogue selector")
			}
			if _, err = s.env.needs.Save(t.Context(), changed, "alice"); err != nil {
				t.Fatal(err)
			}
			daemon.ResetCommands()
			preview, err := s.PreviewStorage(t.Context(), dep.ProjectID, dep.Name)
			if err != nil || preview.Complete || len(preview.Needs) != 1 || preview.Needs[0].Why == "" || preview.Needs[0].Current == nil {
				t.Fatalf("selector conflict hidden: %+v %v", preview, err)
			}
			if _, err = s.AdoptStorage(t.Context(), dep.ProjectID, dep.Name); err == nil {
				t.Fatal("changed selector certified")
			}
			got, found, err := s.Deployments.StorageOf(dep).Get(t.Context(), need.Name, volume)
			if err != nil || !found || !sameStorage(got, *before.Needs[0].Observed) {
				t.Fatalf("changed selector lost binding: %+v %t %v", got, found, err)
			}
			if len(daemon.Commands()) != 0 {
				t.Fatalf("selector conflict reached Docker: %v", daemon.Commands())
			}
		})
	}
}
