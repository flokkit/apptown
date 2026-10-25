package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"gopkg.in/yaml.v3"
)

// portainerNeedsSpec generates a secret every part is given, so the stack
// carries the app's secrets as the intranet release delivers them.
const portainerNeedsSpec = `[project]
name = "p"
revision = "1.0"

[profiles.default]
AUTH_KEY = { description = "signs cookies", generate = true }
`

// newPortainerNeeds is the promotion world whose multipart app declares a
// postgres need and a generated secret, committed before its bundle is
// published, and which asks for a backup after each release (recorded).
func newPortainerNeeds(t *testing.T) (*promotionTargets, *ReleaseBundle, *backupsAsked) {
	t.Helper()
	p := newPromotionTargetsWith(t, false, strings.Replace(digestAppYAML, "init:\n", "needs:\n  postgres: {}\ninit:\n", 1))
	if err := os.WriteFile(filepath.Join(p.repo, "secretspec.toml"), []byte(portainerNeedsSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "secrets"}} {
		cmd := gittest.Command(args...)
		cmd.Dir = p.repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	head, err := releaseGit(t.Context(), p.repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	p.revision = strings.TrimSpace(head)
	asked := &backupsAsked{}
	p.s.providers.afterRelease = asked.ask
	// The release door resolves secrets as AppTown's wiring does
	// (release_wiring.go), from the secrets store the intranet door reads.
	p.s.release.Secrets = storeSecrets{p.s.env.secrets}
	return p, p.land(t), asked
}

// storeSecrets resolves a release's secrets from the secrets store, as the
// AppTown's own wiring of the release door does.
type storeSecrets struct{ store *secret.Service }

func (s storeSecrets) ResolveRelease(ctx context.Context, p Application, name, staged string, c *appcontract.Contract) (deploy.ConsumerSecrets, []string, error) {
	delivery, err := s.store.Resolve(ctx, p.ID, p.Slug, name, staged, c)
	if err != nil {
		return nil, nil, err
	}
	var names []string
	for _, r := range delivery.Receipts {
		names = append(names, r.Name)
	}
	return delivery, names, nil
}

// backupsAsked records each deployment a release asked a backup for.
type backupsAsked struct {
	mu    sync.Mutex
	asked []string
}

func (b *backupsAsked) ask(_ ids.ID, deployment string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.asked = append(b.asked, deployment)
}

func (b *backupsAsked) of() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.asked...)
}

// releaseByName releases bundle b to the deployment name, as a person does
// with apptown release <project> <deployment>, and answers the release.
func (p *promotionTargets) releaseByName(t *testing.T, name string, b *ReleaseBundle) (*Release, error) {
	t.Helper()
	accepted, err := p.s.RequestRelease(context.Background(), DeploySelection{ProjectID: p.project, DeploymentName: name, BundleID: b.ID})
	if err != nil {
		return nil, err
	}
	execErr := p.s.ExecuteRelease(context.Background(), accepted.ID)
	rel, err := p.s.Releases.Last(context.Background(), p.project, name)
	if err != nil || rel.ID != accepted.ID {
		t.Fatalf("the release: %+v %v", rel, err)
	}
	return rel, execErr
}

func (p *promotionTargets) lastStack(t *testing.T) (string, int) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.stacks) == 0 {
		return "", 0
	}
	return p.stacks[len(p.stacks)-1], len(p.stacks)
}

// A deployment on Portainer, released by name, runs the app with its needs
// as the intranet release runs it: the same stack file, byte for byte —
// postgres with the stock start script that makes the app's user an
// ordinary owner, AppTown's password, its data on a volume the deploy
// made and the deployment now owns, the app's secret, its parts and init
// steps — at the project's release domain, and a backup is asked for once
// it is live. The deployment had no handle and no address before.
//
// Kills: no renderer (every named Portainer release fails); a stack named
// from the handle only; the placement's empty address kept; no backup asked.
func TestANamedReleaseToPortainerCarriesTheAppsNeeds(t *testing.T) {
	t.Parallel()
	p, b, asked := newPortainerNeeds(t)
	lan, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, "lan", p.target(t, IntranetTargetName).ID, Attributes{Feed: "promotion", Audience: "organisation", DataClass: "non-production"})
	if err != nil || lan.URL != "" || lan.Placement().Handle["stack"] != nil {
		t.Fatalf("the new deployment: %+v %v", lan, err)
	}
	address, err := p.s.EditAddress(t.Context(), p.project, lan.ID, 0, "feedback.example")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := p.releaseByName(t, "lan", b)
	if err != nil || rel.State != Released {
		events, _ := p.s.Releases.Events(context.Background(), rel.ID)
		t.Fatalf("the named release: %v %v %+v", rel.State, err, events)
	}
	named, n := p.lastStack(t)
	// The stack carries the deployment's own password, never the
	// catalogue's.
	password, err := p.s.env.NeedCredentials(NeedSecretsScope(lan.ID))(t.Context(), "postgres", "password")
	if err != nil || password == "" || password == "app" {
		t.Fatalf("the deployment's Postgres password: %q %v", password, err)
	}
	if strings.Contains(named, `POSTGRES_PASSWORD: "app"`) || strings.Contains(named, "postgres://app:app@") {
		t.Fatalf("the stack carries the catalogue's password:\n%s", named)
	}
	for _, want := range []string{
		"  postgres:\n    image: postgres:16-alpine\n",
		`echo \"apptown: making $$POSTGRES_USER an ordinary owner of $$POSTGRES_DB\"`,
		`POSTGRES_PASSWORD: "` + password + `"`,
		// Its need volume is named by the deployment's key.
		"- apptown-need-dep-" + deploy.HostKey(lan.ID) + "-postgres:/var/lib/postgresql/data",
		`DATABASE_URL: "postgres://app:` + password + `@postgres:5432/app?sslmode=disable"`,
		"AUTH_KEY: ",
		"  init-migrate:\n", "  worker:\n",
		"Host(`" + address.Ingress.Hosts[0] + "`) || Host(`feedback.example`)",
	} {
		if !strings.Contains(named, want) {
			t.Fatalf("the stack lacks %q:\n%s", want, named)
		}
	}
	events, err := p.s.Releases.Events(context.Background(), rel.ID)
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	for _, e := range events {
		said = append(said, e.Text)
	}
	if strings.Contains(strings.Join(said, "\n"), password) {
		t.Fatalf("the release's log holds the deployment's password: %v", said)
	}
	for _, want := range []string{"carrying the app's needs: postgres", "the needs' credentials, made for this deployment, go as stack environment", "Portainer keeps a copy with the stack definition", "also answering on feedback.example"} {
		if !strings.Contains(strings.Join(said, "\n"), want) {
			t.Fatalf("the release's log does not say %q: %v", want, said)
		}
	}
	got, err := p.s.Deployments.ByName(t.Context(), p.project, "lan")
	if err != nil || got.URL != "https://feedback.example" {
		t.Fatalf("the deployment's address: %q %v", got.URL, err)
	}
	owned, err := p.s.Deployments.Owned(t.Context(), got.ID)
	if err != nil {
		t.Fatal(err)
	}
	var volume bool
	key := deploy.HostKey(got.ID)
	for _, o := range owned {
		volume = volume || (o.Kind == "volume" && o.Name == "apptown-dep-"+key+"_apptown-need-dep-"+key+"-postgres" && o.Need == "postgres")
	}
	if !volume {
		t.Fatalf("the postgres volume the deploy made is not the deployment's: %+v", owned)
	}
	if a := asked.of(); len(a) != 1 || a[0] != "lan" {
		t.Fatalf("backups asked: %v", a)
	}
	// The intranet door gives the same stack for the same bundle.
	p.releaseLAN(t)
	intranet, m := p.lastStack(t)
	if m != n+1 || intranet != named {
		t.Fatalf("the doors' stacks differ:\nnamed:\n%s\nintranet:\n%s", named, intranet)
	}
}

// A later target-domain edit cannot change the deployment's admitted address.
func TestANamedPortainerReleaseRecordsTheAddressItRoutes(t *testing.T) {
	t.Parallel()
	p, b, _ := newPortainerNeeds(t)
	intranet := p.target(t, IntranetTargetName)
	if _, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, "lan", intranet.ID, Attributes{Feed: "promotion", Audience: "organisation", DataClass: "non-production"}); err != nil {
		t.Fatal(err)
	}
	if rel, err := p.releaseByName(t, "lan", b); err != nil || rel.State != Released {
		t.Fatalf("the first release: %v", err)
	}
	intranet.Ingress["domain"] = "intra.example"
	if _, err := p.s.Targets.Put(t.Context(), intranet); err != nil {
		t.Fatal(err)
	}
	if rel, err := p.releaseByName(t, "lan", b); err != nil || rel.State != Released {
		t.Fatalf("the second release: %v", err)
	}
	if got, err := p.s.Deployments.ByName(t.Context(), p.project, "lan"); err != nil || got.URL != "https://app-"+strings.ReplaceAll(got.ID.String(), "-", "")+".apps.example" {
		t.Fatalf("the address after the domain changed: %q %v", got.URL, err)
	}
	dep, err := p.s.Deployments.ByName(t.Context(), p.project, "lan")
	if err != nil {
		t.Fatal(err)
	}
	if stack, _ := p.lastStack(t); !strings.Contains(stack, ".apps.example`)") || strings.Contains(stack, ".intra.example") || !strings.Contains(stack, `APPTOWN_ORIGINS: "`+dep.URL+`"`) {
		t.Fatalf("the stack routes:\n%s", stack)
	}
}

// A project has one deployment on a Portainer target, the target's tier
// (lan for one attached with none), since its stack and volumes are named
// for the project: another name is refused before a release is numbered.
//
// Kills: no refusal (two deployments share one stack); the attached
// target's missing tier read as no name allowed.
func TestAProjectHasOneDeploymentOnAPortainerTarget(t *testing.T) {
	t.Parallel()
	p, b, _ := newPortainerNeeds(t)
	seeded := p.target(t, IntranetTargetName)
	attached, err := p.s.AttachPortainerTarget(t.Context(), PortainerAttachment{Name: "office", EndpointID: 3, Environment: DefaultPortainerEnvironment, URLFrom: "PORTAINER_URL", APIKeyFrom: "PORTAINER_API_KEY", Domain: "office.example"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ target *Target }{{seeded}, {attached}} {
		other := "other-" + c.target.Name
		if _, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, other, c.target.ID, Attributes{Feed: "promotion", Audience: "organisation", DataClass: "non-production"}); err != nil {
			t.Fatal(err)
		}
		before, _ := p.s.Releases.ForProject(context.Background(), p.project, 50)
		_, err := p.releaseByName(t, other, b)
		if !errors.Is(err, ErrReleaseRefused) || !strings.Contains(err.Error(), "a project has one deployment on "+c.target.Name+", lan") {
			t.Fatalf("%s on %s: %v", other, c.target.Name, err)
		}
		if after, _ := p.s.Releases.ForProject(context.Background(), p.project, 50); len(after) != len(before) {
			t.Fatalf("a refused release was numbered on %s", c.target.Name)
		}
	}
	if _, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, "lan", attached.ID, Attributes{Feed: "promotion", Audience: "organisation", DataClass: "non-production"}); err != nil {
		t.Fatal(err)
	}
	if rel, err := p.releaseByName(t, "lan", b); err != nil || rel.State != Released {
		t.Fatalf("lan on the attached target: %v", err)
	}
}

// Portainer pulls every image from the registry, built for its machines: a
// release of images that live only on a host, one for an environment whose
// platform is unknown, and one built for another platform are refused with
// their reasons, and Portainer is given no stack.
func TestANamedPortainerReleaseRefusesImagesItCannotRun(t *testing.T) {
	t.Parallel()
	p, b, _ := newPortainerNeeds(t)
	if _, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, "lan", p.target(t, IntranetTargetName).ID, Attributes{Feed: "promotion", Audience: "organisation", DataClass: "non-production"}); err != nil {
		t.Fatal(err)
	}
	arm := `{"Os":"linux","Arch":"arm64"}`
	unknown := `{}`
	for _, c := range []struct {
		name, platform, want string
	}{
		{"another platform", arm, "this release was built for linux/amd64, and intranet runs linux/arm64"},
		{"an unknown platform", unknown, "intranet's platform is unknown"},
	} {
		p.platform.Store(&c.platform)
		_, err := p.releaseByName(t, "lan", b)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: %v", c.name, err)
		}
		if _, n := p.lastStack(t); n != 0 {
			t.Fatalf("%s: Portainer was given a stack", c.name)
		}
	}
	amd := `{"Os":"linux","Arch":"amd64"}`
	p.platform.Store(&amd)
	byID := *b
	byID.BuiltFrom.Registry = ""
	d, err := p.s.providers.Driver(t.Context(), p.target(t, IntranetTargetName), mustDeployment(t, p.digestWorld, "lan"))
	if err != nil {
		t.Fatal(err)
	}
	r := d.(*portainerRelease)
	if _, err := r.Render(t.Context(), mustDeployment(t, p.digestWorld, "lan").Placement(), sourceOf(t, p, &byID), nil, nil, func(string, ...any) {}); !errors.Is(err, ErrReleaseRefused) || !strings.Contains(err.Error(), "this release was not published there. Attach a container registry to the project") {
		t.Fatalf("images by ID: %v", err)
	}
}

// sourceOf is the source ExecuteRelease gives a driver for bundle b of
// the world's app.
func sourceOf(t *testing.T, p *promotionTargets, b *ReleaseBundle) deploy.Source {
	t.Helper()
	c, err := appcontract.Parse(p.contract)
	if err != nil {
		t.Fatal(err)
	}
	return deploy.Source{Slug: "p", Revision: b.ContractRevision, Contract: c, Images: b.Parts, Init: b.Init, Published: b.ByDigest(), Platform: b.BuiltFrom.Platform}
}

// The intranet door cannot revoke a key AppTown registers for an app (an
// AI models key), so it refuses such an app and says the door that can:
// releasing the deployment by name.
func TestTheIntranetDoorNamesTheDoorForARegisteredNeed(t *testing.T) {
	t.Parallel()
	p := newPromotionTargetsWith(t, false, strings.Replace(digestAppYAML, "init:\n", "needs:\n  llm: {}\ninit:\n", 1))
	p.publish(t)
	proj, err := p.projects.ByID(context.Background(), p.project)
	if err != nil {
		t.Fatal(err)
	}
	if _, code, err := p.s.ReleaseLAN(t.Context(), &proj, "boss", LANReleaseRequest{}); code != 422 || err == nil || !strings.Contains(err.Error(), "this app needs llm") || !strings.Contains(err.Error(), "release it with apptown release p lan") {
		t.Fatalf("the intranet door: %d %v", code, err)
	}
}

// A Portainer stack gives the app the project's integration values as a
// Docker host does, through either door: the public part gets them in its
// environment, and a static public part also gets them as its browser
// values; other parts and init steps get neither. The release log names
// the values, never their contents, and no longer says Portainer does not
// deliver them.
//
// Kills: a stack without the integration values (each door); values given
// to every service; a static site's page given nothing; a refusal of a
// static site whose page is given values.
func TestAPortainerStackGivesTheAppItsIntegrationValues(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	const url, key = "https://feedback.example/in", "feedback-key-canary"
	if err := p.s.env.integrations.Attachments.Attach(t.Context(), p.project, "feedback", "", `{"appKey":"`+key+`","url":"`+url+`"}`, integration.Entered, "test", nil); err != nil {
		t.Fatal(err)
	}
	b := p.land(t)
	type service struct {
		Environment map[string]string `yaml:"environment"`
	}
	check := func(door string, rel *Release, err error) {
		t.Helper()
		events, _ := p.s.Releases.Events(context.Background(), rel.ID)
		if err != nil || rel.State != Released {
			t.Fatalf("%s: %v %v %+v", door, rel.State, err, events)
		}
		compose, _ := p.lastStack(t)
		var stack struct {
			Services map[string]service `yaml:"services"`
		}
		if err := yaml.Unmarshal([]byte(compose), &stack); err != nil {
			t.Fatal(err)
		}
		app := stack.Services["app"].Environment
		if app["FEEDBACK_URL"] != url || app["FEEDBACK_APP_KEY"] != key {
			t.Errorf("%s: the app is not given the feedback values: %v", door, app)
		}
		var browser map[string]string
		if err := json.Unmarshal([]byte(app[appcontract.BrowserEnvVar]), &browser); err != nil || browser["FEEDBACK_URL"] != url || browser["FEEDBACK_APP_KEY"] != key {
			t.Errorf("%s: the static site's page is not given its values: %q %v", door, app[appcontract.BrowserEnvVar], err)
		}
		for name, s := range stack.Services {
			if name == "app" {
				continue
			}
			for k := range s.Environment {
				if k == "FEEDBACK_URL" || k == "FEEDBACK_APP_KEY" || k == appcontract.BrowserEnvVar {
					t.Errorf("%s: %s is given %s", door, name, k)
				}
			}
		}
		named := false
		for _, e := range events {
			if strings.Contains(e.Text, key) {
				t.Errorf("%s: the log carries a value: %s", door, e.Text)
			}
			if strings.Contains(e.Text, "does not deliver") {
				t.Errorf("%s: the log says the values are not delivered: %s", door, e.Text)
			}
			named = named || strings.Contains(e.Text, "integration values (FEEDBACK_APP_KEY, FEEDBACK_URL)")
		}
		if !named {
			t.Errorf("%s: the log does not name the values given: %+v", door, events)
		}
	}
	rel := p.releaseLAN(t)
	check("the intranet release", rel, nil)
	rel, err := p.releaseByName(t, "lan", b)
	check("the release by name", rel, err)
}

// A release by name to Portainer of a bundle that was not published is
// refused when it is asked for, on an AppTown with local Docker too, with
// the intranet door's next action: no numbered release is made, so nothing
// is prepared for it (no secret generated, no need registered).
//
// Kills: the refusal left to the driver's render, after the release was
// numbered and prepared.
func TestANamedPortainerReleaseOfAnUnpublishedBundleIsRefusedWhenAsked(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	if _, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, "lan", p.target(t, IntranetTargetName).ID, Attributes{Feed: "promotion", Audience: "organisation", DataClass: "non-production"}); err != nil {
		t.Fatal(err)
	}
	byID := cutBundleFixture(t, p.db, p.project, p.revision, BundleOrigin{})
	before, err := p.s.Releases.ForProject(context.Background(), p.project, 50)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.s.RequestRelease(context.Background(), DeploySelection{ProjectID: p.project, DeploymentName: "lan", BundleID: byID})
	if !errors.Is(err, ErrReleaseRefused) || !strings.Contains(err.Error(), "this release was not published there. Attach a container registry to the project, then land a change") {
		t.Fatalf("an unpublished bundle released by name: %v", err)
	}
	if after, err := p.s.Releases.ForProject(context.Background(), p.project, 50); err != nil || len(after) != len(before) {
		t.Fatalf("a refused release was numbered: %d before, %d after (%v)", len(before), len(after), err)
	}
}
