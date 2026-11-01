package dataprotection

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/openrouter/openrouterfake"
	"github.com/google/go-containerregistry/pkg/name"
)

// claimedAttempt stands in for Work's claimed deploy step attempt: it
// fences a write in a transaction, as Work does, while it is current, and
// refuses once lost. failAfter makes the fence's own transaction fail after
// the write, as Work's commit can.
type claimedAttempt struct {
	db                 *database.DB
	project, run, step ids.ID
	at                 int64
	lost, failAfter    bool
	// lostAfter, when positive, loses the attempt after that many fenced
	// writes: a later attempt has superseded it.
	lostAfter, fenced int
}

func (c *claimedAttempt) ClaimedStep(kind string) (ids.ID, ids.ID, ids.ID, error) {
	if kind != "deploy" {
		return ids.Nil, ids.Nil, ids.Nil, errAttemptLost
	}
	return c.project, c.run, c.step, nil
}

func (c *claimedAttempt) InAttempt(kind string, write func(*database.Tx, int64) error) error {
	if c.lost || kind != "deploy" || (c.lostAfter > 0 && c.fenced >= c.lostAfter) {
		return errAttemptLost
	}
	c.fenced++
	return c.db.InTx(context.Background(), func(tx *database.Tx) error {
		if err := write(tx, c.at); err != nil {
			return err
		}
		if c.failAfter {
			return errAttemptLost
		}
		return nil
	})
}

const (
	releaseRepo = "registry.example.com/team/game"
	digestA     = "@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB     = "@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func digestOrigin(by deployment.AttemptClaim) deployment.BundleOrigin {
	return deployment.BundleOrigin{Registry: releaseRepo, Platform: "linux/amd64", Build: deployment.BundleBuild{Worker: "http://worker:7070", Role: "agent"}, By: by}
}

type configurationIdentity struct{ Name, ProjectSlug, OwnerGroup string }

type configurationWorld struct {
	actor       configurationIdentity
	denial      error
	contract    *appcontract.Contract
	contractErr error
	before      func()
}

func (w *configurationWorld) Require(context.Context, access.Permission) (access.Caller, error) {
	return access.Caller{Principal: access.Principal{Name: w.actor.Name}, Verified: true, Receipt: w.actor.Name}, w.denial
}

func (w *configurationWorld) Project(_ context.Context, id ids.ID) (deployment.ReleaseProject, error) {
	return deployment.ReleaseProject{Application: deployment.Application{ID: id, Slug: w.actor.ProjectSlug}, OwnerGroup: w.actor.OwnerGroup}, nil
}

func (w *configurationWorld) CurrentContract(context.Context, string) (*appcontract.Contract, error) {
	if w.before != nil {
		w.before()
	}
	return w.contract, w.contractErr
}

// configurationFixtureWith is configurationFixture over rt's providers.
func configurationFixtureWith(t *testing.T, rt testRuntime) (*both, *configurationWorld, deployment.ConfigureDeployment, *deployment.Target) {
	t.Helper()
	db := testDB(t)
	// Project is an external prerequisite; this suite owns only Deployment setup.
	project := createTestProject(t, db, "config", "Configuration")
	w := &configurationWorld{actor: configurationIdentity{Name: "alice", ProjectSlug: "config", OwnerGroup: "team"}, contract: &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true}}}}
	s := assembled(db, rt, deployment.Dependencies{Authority: w, Configuration: deployment.ConfigurationDependencies{Authority: w, Projects: w, Contracts: w}})
	target, err := s.Targets.Put(context.Background(), &deployment.Target{Name: "cloud", Kind: deployment.KindDOApp, Origin: deployment.OriginAttached, Status: deployment.TargetReady, NeedsMode: "managed", Capabilities: deployment.TargetCapabilities{Needs: []string{"postgres"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Targets.Offer(context.Background(), target, "*", deployment.OfferCaps{}, "setup"); err != nil {
		t.Fatal(err)
	}
	return s, w, deployment.ConfigureDeployment{ProjectID: project, Name: "prod", Target: target.Name}, target
}

func configureForTest(t *testing.T, s *both, in deployment.ConfigureDeployment) *deployment.Deployment {
	t.Helper()
	result, err := s.Configure(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return result.Deployment
}

type doReleaseQueue struct{}

func (doReleaseQueue) EnqueueReleaseIn(context.Context, *database.Tx, ids.ID) error { return nil }

type doReleaseSecrets struct{}

func (doReleaseSecrets) ResolveRelease(context.Context, deployment.Application, string, string, *appcontract.Contract) (deploy.ConsumerSecrets, []string, error) {
	return secret.Delivery{}, nil, nil
}

// markCopy turns an ordinary project whose deployment already stands on
// App Platform into an eval copy. No operation produces that state: a
// purpose is fixed when the project is made, and a copy is never placed on
// a provider's platform. The test asks that the take-down judge by the
// purpose alone, not by where the deployment happens to stand.
func markCopy(t *testing.T, db *database.DB, projectID ids.ID) {
	t.Helper()
	// sql-guarantee: no owner operation changes a project's purpose (see above).
	if _, err := db.SQL().ExecContext(context.Background(), `UPDATE projects SET purpose='eval-copy' WHERE id=$1`, projectID); err != nil {
		t.Fatal(err)
	}
}

func mustDigest(t *testing.T, ref string) name.Digest {
	t.Helper()
	d, err := name.NewDigest(ref)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func releaseGit(ctx context.Context, repo string, args ...string) (string, error) {
	return gitexec.Run(ctx, repo, args...)
}

// deploymentExists reports whether the owner still has the deployment.
func deploymentExists(t *testing.T, db *database.DB, id ids.ID) bool {
	t.Helper()
	_, err := deployment.NewDeployments(db).ByID(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("deployment %d: %v", id, err)
	}
	return true
}

// receipts is every audit receipt of exactly this kind across the instance
// (every kind when empty), newest first, that also satisfies each filter.
func receipts(t *testing.T, db *database.DB, kind string, filters ...func(audit.Entry) bool) []audit.Entry {
	t.Helper()
	rows, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: kind, Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) >= audit.MaxLimit {
		t.Fatalf("%d %q receipts: the ledger page may be incomplete", len(rows), kind)
	}
	var out []audit.Entry
next:
	for _, r := range rows {
		if kind != "" && r.Kind != kind {
			continue
		}
		for _, keep := range filters {
			if !keep(r) {
				continue next
			}
		}
		out = append(out, r)
	}
	return out
}

func byActor(actor string) func(audit.Entry) bool {
	return func(e audit.Entry) bool { return e.Actor == actor }
}

// wantN fails when a read did not find exactly n records.
func wantN[T any](t *testing.T, what string, got []T, n int) {
	t.Helper()
	if len(got) != n {
		t.Fatalf("%s: %d, want %d", what, len(got), n)
	}
}

// everyRetained is every retained resource on the instance.
func everyRetained(t *testing.T, db *database.DB) []deployment.RetainedResource {
	t.Helper()
	all, err := deployment.NewRetained(db).All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// needInstances is a deployment's recorded need instances, every mode.
func needInstances(t *testing.T, db *database.DB, deploymentID ids.ID) []deployment.NeedInstance {
	t.Helper()
	needs, err := deployment.NewNeedStore(db, nil).ForDeployment(context.Background(), deploymentID)
	if err != nil {
		t.Fatal(err)
	}
	return needs
}

func bySubject(subject string) func(audit.Entry) bool {
	return func(e audit.Entry) bool { return e.Subject == subject }
}

type recoveryJobFollowups struct{}

func (recoveryJobFollowups) EnqueueIn(ctx context.Context, tx *database.Tx, r jobs.Request) error {
	_, err := jobs.InsertOnTx(ctx, tx.Native(), "", r, 3, time.Time{})
	return err
}

const llmProvisioningSentinel = "sk-or-v1-PROVISIONING-SENTINEL-d41c"

func itoa(n ids.ID) string { return n.String() }

type (
	noFollowups struct{}
	fakeDriver  struct{}
)

func (d fakeDriver) Reconcile(context.Context, deploy.Placement, int64, deploy.Artifact, string, deploy.Logf, deploy.Checkpoint) (deploy.Observation, error) {
	return deploy.Observation{}, nil
}

func (d fakeDriver) Resume(context.Context, deploy.Placement, int64, map[string]any, deploy.Logf) (deploy.Observation, error) {
	return deploy.Observation{}, nil
}

func (d fakeDriver) Observe(context.Context, deploy.Placement) (deploy.Observation, error) {
	return deploy.Observation{}, nil
}

func (d fakeDriver) Teardown(context.Context, deploy.Placement, int64, deploy.TakeDown) (deploy.TakenDown, error) {
	return deploy.TakenDown{}, nil
}

func (d fakeDriver) Controls() deploy.Controls { return deploy.Controls{} }

// withLLM gives s's data protection an OpenRouter connector on a fake
// OpenRouter, with AppTown's provisioning key, and attaches AI models to
// the project when attached: what revoking a deployment's key talks to.
func withLLM(t *testing.T, s *Service, projectID ids.ID, attached bool) *openrouterfake.Server {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("OPENROUTER_PROVISIONING_KEY="+llmProvisioningSentinel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secrets := secret.NewService(s.db, secret.Config{DataDir: dir})
	s.secrets = secrets
	s.integrations = integration.NewService(s.db, integration.Dependencies{Projects: project.New(s.db, nil), Secrets: secrets})
	f := openrouterfake.New(llmProvisioningSentinel)
	t.Cleanup(f.Close)
	if _, err := s.integrations.Bindings.Save(context.Background(), "openrouter", integration.CapLLM, "openrouter", `{"url":"`+f.Base+`","provisioningKeyFrom":["OPENROUTER_PROVISIONING_KEY"],"maxLimit":5,"previewLimit":1}`, "admin"); err != nil {
		t.Fatal(err)
	}
	if attached {
		if err := s.integrations.Attachments.Attach(t.Context(), projectID, integration.CapLLM, "openrouter", "{}", "owner", nil); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// ownedKeys records a deployment's AI models key on its owned list, as
// Deployment's recorder does when a boot makes one.
type ownedKeys struct {
	owner deploy.Owner
	name  string
}

func (k ownedKeys) Record(ctx context.Context, h integration.HeldKey) error {
	return k.owner.Created(ctx, deploy.Owned{Kind: deployment.KindOpenRouterKey, ID: h.Ident(), Name: k.name, Need: integration.CapLLM})
}
func (ownedKeys) Forget(context.Context, integration.HeldKey) error { return nil }
