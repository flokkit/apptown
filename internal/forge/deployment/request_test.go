package deployment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/stock"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/secret"
)

type requestAuthority struct {
	deny error
	seen []access.Permission
}

func (a *requestAuthority) Require(_ context.Context, p access.Permission) (access.Caller, error) {
	a.seen = append(a.seen, p)
	return access.Caller{Principal: access.Principal{Name: "alice"}, Verified: true}, a.deny
}

type requestSources struct {
	before     func()
	calls      int
	err        error
	contract   *appcontract.Contract
	noContract bool
}

func (*requestSources) Project(_ context.Context, id ids.ID) (ReleaseProject, error) {
	return ReleaseProject{Application: Application{ID: id, Slug: "release-create"}, Repository: "/no-such-release-repository"}, nil
}
func (s *requestSources) Contract(context.Context, ReleaseProject, string) (*appcontract.Contract, string, error) {
	s.calls++
	if s.before != nil {
		s.before()
	}
	if s.err != nil {
		return nil, "", s.err
	}
	if s.noContract {
		return nil, "selected", nil
	}
	if s.contract != nil {
		return s.contract, "selected", nil
	}
	return &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true}}}, "selected", nil
}

type requestSecrets struct{}

func (requestSecrets) ResolveRelease(context.Context, Application, string, string, *appcontract.Contract) (deploy.ConsumerSecrets, []string, error) {
	return nil, nil, nil
}

type requestQueue struct{}

func (requestQueue) EnqueueReleaseIn(ctx context.Context, tx *database.Tx, id ids.ID) error {
	var recorded ids.ID
	// sql-guarantee: requestQueue is a test-owned transactional port double whose insertion must share the release transaction for queue-veto rollback; production queries do not target test_release_queue.
	return tx.SQL().QueryRowContext(ctx, `INSERT INTO test_release_queue(release_id) VALUES ($1) RETURNING release_id`, id).Scan(&recorded)
}
func requestFixture(t *testing.T) (*Service, DeploySelection, *requestAuthority, *requestSources) {
	t.Helper()
	db, _, draft := releaseCreationFixture(t, ReleaseFromBundle)
	target, err := NewTargets(db, nil).Put(context.Background(), &Target{Name: "target", Kind: "request-test", Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewDeployments(db).EnsureStanding(t.Context(), draft.ProjectID, draft.Deployment, target.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `CREATE TABLE test_release_queue(release_id UUID PRIMARY KEY REFERENCES releases(id))`)
	authority, sources := &requestAuthority{}, &requestSources{}
	s := New(context.Background(), db, assembled(db, testRuntime{Drivers: map[string]DriverFactory{"request-test": func(context.Context, *Target, *Deployment) (deploy.Driver, error) { return fakeDriver{}, nil }}}, Dependencies{Releases: ReleaseDependencies{Authority: authority, Sources: sources, Secrets: requestSecrets{}, Queue: requestQueue{}}}))
	return s, DeploySelection{ProjectID: draft.ProjectID, DeploymentName: draft.Deployment, BundleID: draft.BundleID}, authority, sources
}
func TestRequestReleaseAtomicQueue(t *testing.T) {
	for _, table := range []string{"releases", "release_changes", "release_events", "release_requests", "test_release_queue"} {
		t.Run(table, func(t *testing.T) {
			t.Parallel()
			s, in, _, _ := requestFixture(t)
			change := shippedChangeFixture(t, s.db, in.ProjectID)
			in.BundleID = cutBundleFixture(t, s.db, in.ProjectID, "selected", BundleOrigin{})
			mustExec(t, s.db, `CREATE FUNCTION veto_request() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
			mustExec(t, s.db, `CREATE TRIGGER veto_request BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION veto_request()`)
			got, err := s.RequestRelease(context.Background(), in)
			if err == nil || !got.ID.IsZero() {
				t.Fatalf("suppressed %s admitted: %+v %v", table, got, err)
			}
			// Changes, events, requests and the queue row all reference their
			// release, so no release on the instance means none was left behind.
			wantN(t, "releases", everyRelease(t, s.db), 0)
			mustExec(t, s.db, `DROP TRIGGER veto_request ON `+table)
			got, err = s.RequestRelease(context.Background(), in)
			if err != nil || got.Number != 1 || got.State != Releasing {
				t.Fatalf("retry: %+v %v", got, err)
			}
			wantN(t, "releases", everyRelease(t, s.db), 1)
			wantN(t, "release events", releaseEvents(t, s.db, got.ID), 1)
			wantN(t, "releases carrying the shipped change", changeReleases(t, s.db, in.ProjectID, change), 1)
			releaseRequest(t, s.db, got.ID)
			// sql-guarantee: test_release_queue is this test's own queue double; no owner reads it.
			dbtest.Count(t, s.db, 1, `SELECT COUNT(*) FROM test_release_queue`)
		})
	}
}
func TestRequestReleaseCheckedSelection(t *testing.T) {
	for _, scenario := range []string{"denied", "protected", "missing-deployment", "foreign-bundle", "missing-bundle", "no-bundle", "landing", "preview", "retargeted", "attributes-changed", "generation-changed", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s, in, authority, sources := requestFixture(t)
			ctx := context.Background()
			var want error
			switch scenario {
			case "denied":
				want = errors.New("denied")
				authority.deny = want
			case "protected":
				mutateStoredDeployment(t, s.db, in.ProjectID, in.DeploymentName, func(r *deploymentsql.Deployment) { r.Audience = "public" })
			case "missing-deployment":
				in.DeploymentName = "absent"
				want = ErrDeploymentMissing
			case "foreign-bundle":
				other := createTestProject(t, s.db, "other", "Other")
				in.BundleID = cutBundleFixture(t, s.db, other, "selected", BundleOrigin{})
				want = ErrBundleMissing
			case "missing-bundle":
				in.BundleID = idstest.N(99999)
				want = ErrBundleMissing
			case "no-bundle":
				if _, err := deploymentsql.New(s.db.SQL()).RemoveProjectReleaseBundles(ctx, in.ProjectID); err != nil {
					t.Fatal(err)
				}
				in.BundleID = ids.Nil
				want = ErrNoBundle
			case "landing":
				mutateStoredDeployment(t, s.db, in.ProjectID, in.DeploymentName, func(r *deploymentsql.Deployment) { r.Feed = "landing" })
				want = ErrFedByLanding
			case "preview":
				r, err := deploymentsql.New(s.db.SQL()).DeploymentByName(ctx, deploymentsql.DeploymentByNameParams{ProjectID: in.ProjectID, Name: in.DeploymentName})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := deploymentsql.New(s.db.SQL()).DeleteDeployment(ctx, r.ID); err != nil {
					t.Fatal(err)
				}
				storedDeploymentFixture(t, s.db, deploymentsql.EnsureStandingParams{ID: ids.New(time.Now()), ProjectID: r.ProjectID, Name: r.Name, Kind: Preview.String(), TargetID: r.TargetID, Feed: r.Feed, Audience: r.Audience, DataClass: r.DataClass})
				want = ErrPreviewNotDeployed
			case "retargeted":
				sources.before = func() {
					mutateStoredDeployment(t, s.db, in.ProjectID, in.DeploymentName, func(r *deploymentsql.Deployment) { r.TargetID = idstest.N(999999) })
				}
				want = ErrPlacementChanged
			case "attributes-changed":
				sources.before = func() {
					mutateStoredDeployment(t, s.db, in.ProjectID, in.DeploymentName, func(r *deploymentsql.Deployment) { r.Audience = "public" })
				}
				want = ErrPlacementChanged
			case "generation-changed":
				sources.before = func() {
					mutateStoredDeployment(t, s.db, in.ProjectID, in.DeploymentName, func(r *deploymentsql.Deployment) { r.DesiredGeneration++ })
				}
				want = ErrPlacementChanged
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			got, err := s.RequestRelease(ctx, in)
			if want == nil {
				if err != nil || got.ID.IsZero() || len(authority.seen) != 2 || authority.seen[0].Resource.ProjectID != in.ProjectID || authority.seen[1].Capability != access.CapReleasePromote || authority.seen[1].Resource.ID != in.DeploymentName {
					t.Fatalf("protected selection: %+v %+v %v", got, authority.seen, err)
				}
			} else {
				if !errors.Is(err, want) || !got.ID.IsZero() {
					t.Fatalf("got %+v %v; want %v", got, err, want)
				}
				// The queue double references releases, so none queued either.
				wantN(t, "releases", everyRelease(t, s.db), 0)
			}
		})
	}
}

// A landing-fed deployment accepts a release of the bundle it is known to
// run, and that release runs; another bundle at the same revision (whose
// images can differ), a bundle while a newer deploy is under way, and a
// bundle whose deployment's running bundle is not known are refused. A
// release made before attempts recorded their inputs names what runs.
//
// Kills: a landing-fed deployment refusing every deploy by hand (no way to
// record a legacy release's rebuild inputs); matching by revision alone
// (RD-01); matching a last-success revision while a landing runs (RD-02).
func TestALandingFedDeploymentTakesTheBundleItRuns(t *testing.T) {
	type world struct {
		s       *Service
		in      DeploySelection
		dep     *Deployment
		running *ReleaseBundle
	}
	setup := func(t *testing.T, pin bool) world {
		t.Helper()
		s, in, _, _ := requestFixture(t)
		running, err := s.Bundles.ByID(t.Context(), in.BundleID)
		if err != nil {
			t.Fatal(err)
		}
		dep, err := s.Deployments.ByName(t.Context(), in.ProjectID, in.DeploymentName)
		if err != nil {
			t.Fatal(err)
		}
		inputs := Inputs{Revision: running.ContractRevision}
		if pin {
			inputs.BundleID = running.ID
		}
		at, _, err := s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "ran", Inputs: inputs})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Outcomes.Complete(t.Context(), Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", Health: "healthy"}}); err != nil {
			t.Fatal(err)
		}
		mutateStoredDeployment(t, s.db, in.ProjectID, in.DeploymentName, func(r *deploymentsql.Deployment) { r.Feed = "landing" })
		dep, err = s.Deployments.ByName(t.Context(), in.ProjectID, in.DeploymentName)
		if err != nil {
			t.Fatal(err)
		}
		return world{s, in, dep, running}
	}
	t.Run("the bundle it runs, again", func(t *testing.T) {
		t.Parallel()
		w := setup(t, true)
		got, err := w.s.RequestRelease(t.Context(), w.in)
		if err != nil || got.ID.IsZero() {
			t.Fatalf("the bundle it runs, again: %+v %v", got, err)
		}
		if err := w.s.ExecuteRelease(t.Context(), got.ID); errors.Is(err, ErrFedByLanding) {
			t.Fatalf("the accepted release was refused when it ran: %v", err)
		}
	})
	t.Run("another bundle at the same revision", func(t *testing.T) {
		t.Parallel()
		w := setup(t, true)
		other := w.in
		other.BundleID = cutBundleFixture(t, w.s.db, w.in.ProjectID, w.running.ContractRevision, BundleOrigin{})
		if got, err := w.s.RequestRelease(t.Context(), other); !errors.Is(err, ErrFedByLanding) || !got.ID.IsZero() {
			t.Fatalf("a same-revision bundle can carry other images: %+v %v", got, err)
		}
	})
	t.Run("a newer deploy under way", func(t *testing.T) {
		t.Parallel()
		w := setup(t, true)
		if _, _, err := w.s.Outcomes.Admit(t.Context(), Start{DeploymentID: w.dep.ID, TargetID: w.dep.TargetID, Key: "landing", Inputs: Inputs{Revision: "newer"}}); err != nil {
			t.Fatal(err)
		}
		if got, err := w.s.RequestRelease(t.Context(), w.in); !errors.Is(err, ErrFedByLanding) || !got.ID.IsZero() {
			t.Fatalf("a landing under way decides what runs next: %+v %v", got, err)
		}
	})
	t.Run("what runs is not known", func(t *testing.T) {
		t.Parallel()
		w := setup(t, false) // the attempt names no bundle, release or run
		if got, err := w.s.RequestRelease(t.Context(), w.in); !errors.Is(err, ErrFedByLanding) || !got.ID.IsZero() {
			t.Fatalf("an unknown running bundle is not a match: %+v %v", got, err)
		}
	})
	t.Run("a release made before attempts recorded inputs", func(t *testing.T) {
		t.Parallel()
		w := setup(t, false)
		// sql-guarantee: a generation settled before 8 September 2026 has no attempt inputs and its release no generation; no operation writes that shape now, so the inputs row is removed and the legacy release record written directly.
		mustExec(t, w.s.db, `DELETE FROM deployment_attempt_inputs USING deployment_attempts a WHERE a.id=attempt_id AND a.deployment_id=$1`, w.dep.ID)
		// sql-guarantee: the legacy shape (no attempt inputs, a release without a generation) that no operation writes now.
		mustExec(t, w.s.db, `INSERT INTO releases(id,project_id,number,state,deployment,approved_by,created_at,released_at,notes,commit_sha,source_sha,bundle_id,deployment_id) VALUES ($5,$1,1,'released',$2,'bls','2026-09-08T00:00:00Z','2026-09-08T00:00:00Z','','','',$3,$4)`, w.in.ProjectID, w.in.DeploymentName, w.running.ID, w.dep.ID, ids.New(time.Now()))
		if got, err := w.s.RequestRelease(t.Context(), w.in); err != nil || got.ID.IsZero() {
			t.Fatalf("the legacy release names what runs: %+v %v", got, err)
		}
	})
}

// Concurrent admissions use independent stateless ports: the project lock must
// serialize the busy check and allocation, not an in-memory mutex.
type concurrentRequestPorts struct{}

func (concurrentRequestPorts) Require(context.Context, access.Permission) (access.Caller, error) {
	return access.Caller{Principal: access.Principal{Name: "alice"}, Verified: true}, nil
}
func (concurrentRequestPorts) Project(ctx context.Context, id ids.ID) (ReleaseProject, error) {
	return (&requestSources{}).Project(ctx, id)
}
func (concurrentRequestPorts) Contract(ctx context.Context, p ReleaseProject, rev string) (*appcontract.Contract, string, error) {
	return (&requestSources{}).Contract(ctx, p, rev)
}
func TestRequestReleaseConcurrentAdmission(t *testing.T) {
	t.Parallel()
	s, in, _, _ := requestFixture(t)
	s.release.Authority = concurrentRequestPorts{}
	s.release.Sources = concurrentRequestPorts{}
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.RequestRelease(context.Background(), in); results <- err }()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrReleaseInFlight) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d", accepted)
	}
	// sql-guarantee: test_release_queue is this test's own queue double; no owner reads it.
	dbtest.Count(t, s.db, 1, `SELECT COUNT(*) FROM test_release_queue`)
}
func TestExecuteReleaseClaimAndPlacement(t *testing.T) {
	for _, scenario := range []string{"retargeted", "generation-changed", "preparation-failed"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s, in, _, sources := requestFixture(t)
			got, err := s.RequestRelease(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "retargeted" {
				mutateStoredDeployment(t, s.db, in.ProjectID, in.DeploymentName, func(r *deploymentsql.Deployment) { r.TargetID = idstest.N(999999) })
			}
			if scenario == "generation-changed" {
				mutateStoredDeployment(t, s.db, in.ProjectID, in.DeploymentName, func(r *deploymentsql.Deployment) { r.DesiredGeneration++ })
			}
			err = s.ExecuteRelease(context.Background(), got.ID)
			if err == nil {
				t.Fatal("expected execution failure")
			}
			if scenario != "preparation-failed" && !errors.Is(err, ErrPlacementChanged) {
				t.Fatal(err)
			}
			calls := sources.calls
			if scenario != "preparation-failed" && calls != 1 {
				t.Fatal("prepared a changed placement")
			}
			if r := storedRelease(t, s.db, in.ProjectID, got.ID); r.State != ReleaseFailed {
				t.Fatalf("release: %s", r.State)
			}
			if q := releaseRequest(t, s.db, got.ID); !q.ClaimedAt.Valid {
				t.Fatal("failed execution left its request unclaimed")
			}
			if err := s.ExecuteRelease(context.Background(), got.ID); err != nil {
				t.Fatal(err)
			}
			if sources.calls != calls {
				t.Fatal("duplicate delivery repeated preparation")
			}
			wantN(t, "attempts", attemptsIn(t, s.db), 0)
		})
	}
}
func TestRequestReleasePinsLatestBundle(t *testing.T) {
	t.Parallel()
	s, in, _, _ := requestFixture(t)
	bundle := in.BundleID
	in.BundleID = ids.Nil
	got, err := s.RequestRelease(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	storedBundleFixture(t, s.db, deploymentsql.InsertBundleParams{ID: ids.New(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)), ProjectID: in.ProjectID, ContractRevision: "newer", CreatedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)})
	if r := storedRelease(t, s.db, in.ProjectID, got.ID); r.BundleID != bundle || r.CommitSHA != "selected" || r.ApprovedBy != "alice" {
		t.Fatalf("pinned release: %+v", r)
	}
	// sql-guarantee: test_release_queue is this test's own queue double; no owner reads it.
	dbtest.Count(t, s.db, 1, `SELECT COUNT(*) FROM test_release_queue WHERE release_id=?`, got.ID)
	if got.Deployment != in.DeploymentName {
		t.Fatal(fmt.Sprint(got))
	}
}

func TestExecuteReleaseDoesNotPrepareConcurrentDelivery(t *testing.T) {
	t.Parallel()
	s, in, _, sources := requestFixture(t)
	accepted, err := s.RequestRelease(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	sources.before = func() { close(entered); <-release }
	done := make(chan error, 1)
	go func() { done <- s.ExecuteRelease(context.Background(), accepted.ID) }()
	<-entered
	// The first worker has claimed and is preparing, but has no attempt yet.
	// A second delivery must return without reaching the same source port.
	duplicate := s.ExecuteRelease(context.Background(), accepted.ID)
	close(release)
	first := <-done
	if duplicate != nil || first == nil {
		t.Fatalf("duplicate %v, first %v", duplicate, first)
	}
	if sources.calls != 2 {
		t.Fatalf("prepared more than once: %d contract reads", sources.calls)
	}
	wantN(t, "attempts", attemptsIn(t, s.db), 0)
}

func TestExecuteReleaseClaimWriteMustExist(t *testing.T) {
	t.Parallel()
	s, in, _, sources := requestFixture(t)
	accepted, err := s.RequestRelease(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.db, `CREATE FUNCTION veto_claim() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	mustExec(t, s.db, `CREATE TRIGGER veto_claim BEFORE UPDATE ON release_requests FOR EACH ROW EXECUTE FUNCTION veto_claim()`)
	if err := s.ExecuteRelease(context.Background(), accepted.ID); err == nil {
		t.Fatal("suppressed claim was executed")
	}
	if sources.calls != 1 {
		t.Fatal("prepared without claim")
	}
	if q := releaseRequest(t, s.db, accepted.ID); q.ClaimedAt.Valid {
		t.Fatal("suppressed claim was recorded")
	}
	if r := storedRelease(t, s.db, in.ProjectID, accepted.ID); r.State != Releasing {
		t.Fatalf("release: %s", r.State)
	}
}

func TestRequestReleaseRefusesUnreadableOrIncompatibleContract(t *testing.T) {
	for _, scenario := range []string{"read-error", "missing-contract", "incompatible", "browser-values", "integration-need", "password", "same-variable"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s, in, _, sources := requestFixture(t)
			switch scenario {
			case "read-error":
				sources.err = errors.New("cannot read source")
			case "missing-contract":
				sources.noContract = true
			case "incompatible":
				sources.contract = &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true, DataDir: "/data"}}}
			case "browser-values":
				// Only a Docker host serves /_apptown/env.js: a static site whose
				// page is given values is refused elsewhere, never shipped
				// with nothing in it.
				sources.contract = &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true, Static: "."}}}
				kinds := dependency.NewCatalog(s.db, nil)
				if _, _, err := kinds.SyncStock(t.Context(), stock.FS, "dependencies"); err != nil {
					t.Fatal(err)
				}
				integrations := integration.NewService(s.db, integration.Dependencies{Kinds: kinds, Projects: project.New(s.db, nil), Secrets: secret.NewService(s.db, secret.Config{DataDir: t.TempDir()})})
				if err := integrations.Attachments.Remember(t.Context(), in.ProjectID, "errors", "glitchtip", `{"dsn":"https://key@errors.example/3"}`); err != nil {
					t.Fatal(err)
				}
				s.env.integrations = integrations
			case "integration-need", "password", "same-variable":
				// The catalogue refuses a contract's needs as declared, before
				// the release is accepted, on every target.
				kinds := dependency.NewCatalog(s.db, nil)
				if _, _, err := kinds.SyncStock(t.Context(), stock.FS, "dependencies"); err != nil {
					t.Fatal(err)
				}
				s.env.needs = kinds
				web := []appcontract.Part{{Name: "web", Public: true, Port: 8080}}
				switch scenario {
				case "integration-need":
					sources.contract = &appcontract.Contract{Parts: web, Needs: []string{"errors"}}
				case "password":
					sources.contract = &appcontract.Contract{Parts: web, Needs: []string{"postgres"}, NeedParams: map[string]map[string]string{"postgres": {"password": "sourdough"}}}
				case "same-variable":
					if _, err := kinds.Save(t.Context(), "capability: pg-alt\nlocal:\n  image: postgres:16-alpine\n  inject:\n    DATABASE_URL: postgres://{host}/app\n", "bls"); err != nil {
						t.Fatal(err)
					}
					sources.contract = &appcontract.Contract{Parts: web, Needs: []string{"pg-alt", "postgres"}}
				}
			}
			got, err := s.RequestRelease(context.Background(), in)
			var contract *ContractReadError
			refusals := map[string]string{"integration-need": "errors is an integration, not a need", "password": "AppTown sets the password", "same-variable": "DATABASE_URL is given by pg-alt and postgres"}
			if scenario == "incompatible" || scenario == "browser-values" || refusals[scenario] != "" {
				if !errors.Is(err, ErrRefusedContract) {
					t.Fatal(err)
				}
				if want := refusals[scenario]; want != "" && !strings.Contains(err.Error(), want) {
					t.Fatalf("the refusal says %v, not %q", err, want)
				}
				if scenario == "browser-values" && !strings.Contains(err.Error(), "SENTRY_DSN") {
					t.Fatalf("the refusal names what the page would lose: %v", err)
				}
			} else if !errors.As(err, &contract) {
				t.Fatal(err)
			}
			if !got.ID.IsZero() {
				t.Fatal("returned an unaccepted release")
			}
			// The queue double references releases, so none queued either.
			wantN(t, "releases", everyRelease(t, s.db), 0)
		})
	}
}

func TestExecuteReleaseInterruptedPreparationSettlesFailure(t *testing.T) {
	t.Parallel()
	s, in, _, sources := requestFixture(t)
	got, err := s.RequestRelease(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sources.before = func() { cancel(); sources.err = ctx.Err() }
	if err := s.ExecuteRelease(ctx, got.ID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if r := storedRelease(t, s.db, in.ProjectID, got.ID); r.State != ReleaseFailed {
		t.Fatalf("interrupted release: %s", r.State)
	}
	wantN(t, "attempts", attemptsIn(t, s.db), 0)
}
