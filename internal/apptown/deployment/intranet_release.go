package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/portainer"
)

const IntranetTargetName = "intranet"

// ErrNoIntranet is a release to the intranet on an AppTown that records no
// intranet target: one server's AppTown has none, and its landings deploy
// main.
var ErrNoIntranet = fault.New(fault.Conflict, "this AppTown has no intranet target: each landing deploys main; to release to another deployment, name it: apptown release <project> <deployment>")

// relLog appends a timestamped line to a release's log — the permalink's
// "what happened" record (a release should read like a log, not a
// spinner). Lines are written as the work happens, so a watching page (or a
// failure) shows exactly how far it got.
func (s *Service) relLog(relID ids.ID, format string, args ...any) {
	if err := AppendReleaseEvent(s.records, s.db, relID, fmt.Sprintf(format, args...), s.now()); err != nil {
		log.Printf("release %d: writing its log line: %v", relID, err)
	}
}

// IntranetConfig is what an intranet release goes by: the intranet
// target's configuration through PortainerConfig, so it answers on the
// target's domain setting. The release plan shows the same, so what the
// page promises is where the release goes. Refused while the target is not
// recorded or names no domain.
func (s *Service) IntranetConfig(ctx context.Context) (*portainer.Target, error) {
	intranet, err := s.Targets.ByName(ctx, IntranetTargetName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoIntranet
	}
	if err != nil {
		return nil, fmt.Errorf("reading the intranet target: %w", err)
	}
	cfg, err := s.providers.PortainerConfig(ctx, intranet)
	if err != nil {
		return nil, err
	}
	if cfg.AppsDomain == "" {
		return nil, refusal(fmt.Sprintf("the %s target names no domain to answer on: set one in its settings", IntranetTargetName))
	}
	return cfg, nil
}

// LANReleaseRequest is what a person asks an intranet release for: an
// extra hostname for the project (kept for later releases, saved only once
// the release is admitted), and the choice to release over data AppTown
// cannot copy (DataCopyCheck).
type LANReleaseRequest struct {
	Domain             string
	AcceptUncopiedData bool
}

// ReleaseLAN starts the promotion. Cheap validation happens synchronously
// (a plainly-blocked release should refuse at the button, without minting a
// numbered attempt); then the row is created in state 'releasing' and the
// work runs in the background, logging events as it goes. The permalink is
// live from the first moment and the caller navigates straight to it.
//
// What ships is the project's latest bundle, published to the project's
// registry by the landing that made it: every part and init step runs its
// image by digest. A latest bundle that was not published is refused, as a
// release to a Portainer deployment by name is: nothing is built here.
func (s *Service) ReleaseLAN(ctx context.Context, p *project.Project, who string, in LANReleaseRequest) (*LANReleaseStarted, int, error) {
	bundle, err := s.Bundles.Latest(ctx, p.ID)
	if err != nil {
		return nil, 500, err
	}
	return s.releaseLANOf(ctx, p, who, bundle, in)
}

// releaseLANOf is an intranet release of bundle: ReleaseLAN's latest, or
// the bundle the intranet already runs when an operator's override
// redeploys it. A bundle that is missing or not published is refused.
func (s *Service) releaseLANOf(ctx context.Context, p *project.Project, who string, bundle *ReleaseBundle, in LANReleaseRequest) (*LANReleaseStarted, int, error) {
	cfg, err := s.IntranetConfig(ctx)
	if err != nil {
		return nil, 422, err
	}
	if bundle == nil || !bundle.ByDigest() {
		return nil, 422, notPublished(IntranetTargetName)
	}
	if p.DefaultBranch == "" {
		return nil, 422, fmt.Errorf("project is not repo-backed: nothing has landed to release")
	}
	repo := filepath.Join(s.hosts.settings.DataDir, "workspaces", "project-"+p.Slug)
	head := bundle.ContractRevision
	// Validate the selected commit before minting a release. Uncommitted
	// changes must not change what this release admits.
	selected, cleanup, err := docker.PrepareSource(ctx, repo, head, p.Slug, nil)
	defer cleanup()
	if errors.Is(err, docker.ErrNoContract) {
		return nil, 422, fmt.Errorf("nothing runnable has landed — the app has no runtime contract on %s", p.DefaultBranch)
	}
	if err != nil {
		return nil, 422, err
	}

	c := selected.Source.Contract
	// An intranet release is a stack Portainer keeps, with no record of
	// what AppTown made for it: nothing would revoke a key given to it.
	if needs, err := s.env.registeredNeeds(ctx, c); err != nil {
		return nil, 422, err
	} else if len(needs) > 0 {
		return nil, 422, refusal(fmt.Sprintf("this app needs %s, which AppTown gives only to a deployment released by name, where it can revoke it at take-down: release it with apptown release %s %s", needs[0].name, p.Slug, cfg.ReleaseDeployment))
	}
	if _, err := s.env.ResolveDockerStack(ctx, c, nil, nil); err != nil {
		return nil, 422, err // an unknown need fails at the door, before a number is minted
	}
	var vanity string
	rec, err := project.ByIDIn(ctx, s.db, p.ID)
	vanity = rec.ReleaseDomain
	if _, err := database.Lookup(err); err != nil {
		return nil, 0, err
	}
	domain := strings.TrimSpace(in.Domain)
	if domain != "" {
		vanity = domain
	}
	_, url := portainerAddress(p.Slug, cfg.AppsDomain, vanity)
	lanEnv, err := s.intranetDeployment(ctx, p.ID, cfg)
	if err != nil {
		return nil, 500, fmt.Errorf("naming the intranet deployment: %w", err)
	}
	intranet, err := s.Targets.ByID(ctx, lanEnv.TargetID)
	if err != nil {
		return nil, 500, fmt.Errorf("reading the intranet target: %w", err)
	}
	refused, accepted, err := s.uncopiedData(ctx, lanEnv, intranet, bundle.ID, in.AcceptUncopiedData, who)
	if err != nil {
		return nil, 500, err
	}
	if refused != "" {
		return nil, 422, refusal(refused)
	}
	draft := ReleaseDraft{Source: ReleaseFromBundle, BundleID: bundle.ID, ProjectID: p.ID, DeploymentID: lanEnv.ID,
		Deployment: cfg.ReleaseDeployment, Revision: head, Actor: who, Notes: url}
	description := fmt.Sprintf("started by %s — shipping bundle %d (%s as of %s)", who, bundle.ID, p.DefaultBranch, releaseShort(head))
	r, err := s.Releases.Start(ctx, draft, description+accepted)
	if err != nil {
		if errors.Is(err, ErrReleaseInFlight) {
			return nil, 409, err
		}
		return nil, 500, err
	}
	relID, number := r.ID, r.Number
	// A new hostname is this release's (it is given to the run, which
	// labels the stack with it), and the project's for later releases once
	// saved: only after the release is admitted, never by one refused. A
	// save that fails is said, and no receipt claims it.
	if domain != "" {
		if err := s.projects.SetReleaseDomain(ctx, p.ID, domain); err != nil {
			s.relLog(relID, "the hostname %s is this release's but was not saved for later releases: %v", domain, err)
		} else {
			audit.At(s.now()).BestEffort(ctx, s.db, who, p.ID, "project.release-domain", domain, nil)
		}
	}
	s.projects.Contracts().RecordCommit(ctx, s.hosts.settings.DataDir, p.ID, head) // record the contract as of the release commit

	// The release runs on in the background after the request answers.
	go s.runLANRelease(context.WithoutCancel(ctx), relID, p, cfg, who, head, number, url, vanity, bundle)
	return &LANReleaseStarted{OK: true, Number: number, URL: url, State: Releasing}, 202, nil
}

// LANReleaseStarted is an intranet release AppTown accepted: it runs on
// after the request answers, under its number.
type LANReleaseStarted struct {
	OK     bool         `json:"ok"`
	Number int64        `json:"number"`
	URL    string       `json:"url"`
	State  ReleaseState `json:"state"`
}

// runLANRelease is the background half: stage, check the platform, deploy —
// each step logged with its timestamp. Any failure marks the release failed
// with the log showing exactly where it died; a failed release is a numbered
// record, not a vanished toast.
func (s *Service) runLANRelease(ctx context.Context, relID ids.ID, p *project.Project, cfg *portainer.Target, who, head string, number int64, url, vanity string, bundle *ReleaseBundle) {
	store := s.Outcomes
	fail := func(format string, args ...any) {
		cause := fmt.Errorf(format, args...)
		if _, err := store.FailUnstartedRelease(ctx, relID, cause); err != nil {
			s.relLog(relID, "could not record the preparation failure: %v (preparation: %v)", err, cause)
		}
	}
	repo := filepath.Join(s.hosts.settings.DataDir, "workspaces", "project-"+p.Slug)

	// Stage the COMMITTED tree — a release ships what landed, never the
	// worktree (the webalizer lesson).
	tree, cleanup, err := (deploy.SelectedSource{Repository: repo, Revision: head}).Stage(ctx, "")
	staged := tree.Directory
	if err != nil {
		fail("staging the committed tree: %v", err)
		return
	}
	defer cleanup()
	s.relLog(relID, "staged the committed tree (%s)", head[:10])

	committed, err := docker.ReadSelectedContract(staged)
	if err != nil {
		fail("reading the runtime contract from the committed tree: %v", err)
		return
	}
	// The intranet deployment's profile and overrides over the committed
	// contract, recorded with the generation (profiles.go).
	dep, err := s.intranetDeployment(ctx, p.ID, cfg)
	if err != nil {
		fail("naming the deployment: %v", err)
		return
	}
	c, applied, err := s.effectiveContract(ctx, dep, staged, committed)
	if err != nil {
		fail("%v", err)
		return
	}
	s.relLog(relID, "%s", applied.Words())

	// The endpoint's platform decides what ships: the first live release
	// crash-looped in Restarting(255) because an arm64 Mac pushed an arm64
	// image at an amd64 CT. Ask, don't assume; an endpoint that names no
	// platform is refused.
	endpointID, err := portainer.EndpointID(ctx, cfg)
	if err != nil {
		fail("%v", err)
		return
	}
	platform, asked := portainer.EndpointPlatform(ctx, cfg, endpointID)
	if err := refusesPlatform(bundle, "the intranet", platform, asked); err != nil {
		fail("%v", err)
		return
	}
	s.relLog(relID, "the intranet's machines run %s", platform)
	// A published release: every image by the digest its registry
	// answered, built for the endpoint's platform.
	imageRef := bundle.Parts[c.PublicPart().Name]
	s.relLog(relID, "shipping bundle %d by digest: %s", bundle.ID, imageRef)

	// The pull credential is PLATFORM-OWNED: a group deploy token behind a
	// Portainer registry entry, both managed by the platform's operators.
	// AppTown never touches /api/registries (admin-only, and AppTown does not
	// hold Portainer admin). The credential just exists.
	stackPrefix, stackBase := portainer.StackNames(dep.ID, dep.HostPrefix, p.Slug)
	stackName := portainer.StackName(stackPrefix, stackBase)
	hosts, _ := portainerAddress(p.Slug, cfg.AppsDomain, vanity)
	if vanity != "" {
		s.relLog(relID, "also answering on %s (DNS for it is platform-owned — needs its record applied)", vanity)
	}
	// Secrets for the intranet tier: delivered as
	// stack environment, which Portainer stores with the stack definition
	// — a copy in the target's own keeping, said out loud in the log.
	secrets, err := s.env.secrets.Resolve(ctx, p.ID, p.Slug, cfg.ReleaseDeployment, staged, c)
	if err != nil {
		fail("%v", err)
		return
	}
	if len(secrets.Receipts) > 0 {
		names := make([]string, len(secrets.Receipts))
		for i, r := range secrets.Receipts {
			names[i] = r.Name
		}
		s.relLog(relID, "delivering the app's secrets (%s) as stack environment — Portainer keeps a copy with the stack definition", strings.Join(names, ", "))
	}
	logf := func(format string, args ...any) { s.relLog(relID, format, args...) }
	compose, err := s.env.portainerStack(ctx, portainerStackOf{Base: stackBase, Prefix: stackPrefix, Hosts: hosts, Domain: cfg.AppsDomain, Contract: c, Revision: head,
		Images: stackImages{Parts: bundle.Parts, Init: bundle.Init}, Secrets: secrets, Project: p.ID, Deployment: dep.ID}, logf)
	if err != nil {
		fail("%v", err)
		return
	}
	inputs := Inputs{ReleaseID: relID, Revision: strings.TrimSpace(head), ImageDigest: imageRef, ArtifactHash: deploy.ConfigHash(strings.TrimSpace(compose)), Applied: applied, BundleID: bundle.ID}
	result, err := (Runner{Store: store, Applied: s.applied}).Run(ctx,
		Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: fmt.Sprintf("release-%d", relID), Inputs: inputs},
		func(ctx context.Context, at Attempt, checkpoint deploy.Checkpoint) (deploy.Observation, error) {
			return (portainer.Execution{Config: *cfg, EndpointID: endpointID, Stack: stackName, Prefix: stackPrefix, Compose: compose, URL: url, Owner: s.Deployments.OwnerOf(dep),
				Log: func(format string, args ...any) { s.relLog(relID, format, args...) }}).Execute(ctx, at.Generation, checkpoint)
		})
	if err != nil {
		var failure *RunError
		if errors.As(err, &failure) && failure.Stage == Admission {
			fail("minting the deployment generation: %v", failure.Cause)
		} else {
			s.relLog(relID, "deployment outcome %s: %v", result.Outcome, err)
		}
		return
	}
	if !result.Created {
		s.relLog(relID, "attempt %d was already admitted; observation owns its unfinished outcome", result.Attempt.ID)
		return
	}
	if result.Outcome != Applied {
		s.relLog(relID, "deployment outcome %s", result.Outcome)
		return
	}
	s.relLog(relID, "live — %s now serves this release to every device on the intranet", url)
	// A release is the moment its data is worth keeping: a set is asked
	// for, as after a Docker host's release.
	if s.providers.afterRelease != nil {
		s.providers.afterRelease(dep.ProjectID, dep.Name)
	}
	s.tagRelease(ctx, number, bundle, logf)
	s.clearCommittedOverrides(ctx, dep, committed, applied, strings.TrimSpace(head), logf)
}

// portainerAddress is the hosts a project's Portainer stack routes and the
// address a release there is reached at: <slug>.<apps domain> always, and
// the project's release domain beside it, which is then the address.
func portainerAddress(slug, appsDomain, releaseDomain string) (hosts []string, url string) {
	hosts = []string{slug + "." + appsDomain}
	url = "https://" + hosts[0]
	if releaseDomain != "" {
		hosts = append(hosts, releaseDomain)
		url = "https://" + releaseDomain
	}
	return hosts, url
}

// stackImages is what a Portainer stack runs: a published bundle's part
// and init images by digest.
type stackImages struct {
	Parts, Init map[string]string
}

// portainerStackOf is everything a Portainer deployment's stack is made of.
type portainerStackOf struct {
	// Prefix and Base begin the stack's name and its volume keys
	// (portainer.StackNames).
	Base     string
	Prefix   string
	Hosts    []string
	Domain   string
	Contract *appcontract.Contract // the effective contract
	Revision string
	Images   stackImages
	Secrets  deploy.ConsumerSecrets
	// Project is whose attached integrations' values the app is given.
	Project ids.ID
	// Deployment is whose generated need credentials the stack carries.
	Deployment ids.ID
}

// portainerStack is the stack file a Portainer deployment runs, whichever
// door released it: what main runs, the intranet runs — the needs as
// emulators with persistent volumes, the init steps before the app, the
// other parts beside it, and the front door the app should trust. The
// needs are resolved by the code a Docker host boots from (boot.go); the
// stack renders that, never resolving needs again.
func (e *Environment) portainerStack(ctx context.Context, in portainerStackOf, logf func(string, ...any)) (string, error) {
	// The stack's names are its deployment's: a stack rendered without
	// them would name its volumes anew, away from its data.
	if _, err := docker.ParsePrefix(in.Prefix); err != nil || in.Base == "" {
		return "", fmt.Errorf("the deployment's names on Portainer: prefix %q, base %q", in.Prefix, in.Base)
	}
	c := in.Contract
	rs, err := e.ResolveDockerStack(ctx, c, nil, e.NeedCredentials(NeedSecretsScope(in.Deployment)))
	if err != nil {
		return "", err
	}
	plan, err := bundleApp(rs.Plan, in.Images)
	if err != nil {
		return "", err
	}
	given, browser, err := e.integrationValues(ctx, in.Project, c)
	if err != nil {
		return "", err
	}
	st := deploy.IntranetStack{Base: in.Base, Prefix: in.Prefix, Hosts: in.Hosts, Domain: in.Domain, Image: in.Images.Parts[c.PublicPart().Name], Command: plan.PublicPart().Command, Resources: plan.PublicPart().Resources, Port: c.PublicPart().Port,
		Env: plan.PublicPart().Env, Integrations: given, BrowserEnv: browser, DataDir: plan.PublicPart().DataDir, Origins: e.PublicOrigins(ctx), Init: plan.Init, Needs: docker.IntranetNeeds(rs.Needs),
		Secrets: in.Secrets, PublicPart: c.PublicPart().Name, Release: c.PublicRelease(strings.TrimSpace(in.Revision))}
	if len(given) > 0 {
		logf("giving the app the project's integration values (%s) as stack environment — Portainer keeps a copy with the stack definition", strings.Join(slices.Sorted(maps.Keys(given)), ", "))
	}
	for _, part := range plan.Parts {
		if !part.Public {
			st.Parts = append(st.Parts, part)
		}
	}
	if len(st.Needs) > 0 {
		names := make([]string, len(st.Needs))
		for i, n := range st.Needs {
			names[i] = n.Name
		}
		logf("carrying the app's needs: %s (their data lives in intranet volumes and survives releases)", strings.Join(names, ", "))
		for _, n := range rs.Needs {
			if len(n.Credentials) > 0 {
				logf("the needs' credentials, made for this deployment, go as stack environment — Portainer keeps a copy with the stack definition")
				break
			}
		}
	}
	for _, it := range c.Init {
		logf("init step %q runs to completion before the app starts", it.Name)
	}
	return deploy.IntranetCompose(st), nil
}

// integrationValues is what a Docker host gives c's public part from the
// project's attached integrations (docker.Host.StartAppContainer): their
// values, and for a static public part its browser values as JSON ("{}"
// for none; "" for a server app, which is not given them). The browser
// values are read strictly: a failed read fails the release, as it fails a
// Docker host's boot.
func (e *Environment) integrationValues(ctx context.Context, projectID ids.ID, c *appcontract.Contract) (map[string]string, string, error) {
	static := c.PublicPart().IsStatic()
	if e.integrations == nil {
		if static {
			return nil, "{}", nil
		}
		return nil, "", nil
	}
	given := map[string]string{}
	for _, kv := range e.integrations.Environment(ctx, projectID) {
		if k, v, ok := strings.Cut(kv, "="); ok {
			given[k] = v
		}
	}
	if !static {
		return given, "", nil
	}
	values, err := e.integrations.BrowserEnvironment(ctx, projectID)
	if err != nil {
		return nil, "", fmt.Errorf("reading the values a browser may be given: %w", err)
	}
	raw, err := json.Marshal(values)
	return given, string(raw), err
}

// bundleApp is plan with every part and init step running its image from a
// published bundle, by digest. A static part's image carries the command
// that serves it (docker.StaticSiteDockerfile); it is still a static site.
func bundleApp(plan *appcontract.Plan, b stackImages) (*appcontract.Plan, error) {
	cc := *plan
	cc.Parts = append([]appcontract.Part(nil), plan.Parts...)
	cc.Init = append([]appcontract.InitContainer(nil), plan.Init...)
	for i := range cc.Parts {
		p := &cc.Parts[i]
		ref := b.Parts[p.Name]
		if ref == "" {
			return nil, fmt.Errorf("the release has no image for part %s", p.Name)
		}
		p.ServesStatic = p.IsStatic()
		p.Image, p.Build, p.Static, p.StaticBuild = ref, "", "", nil
	}
	for i := range cc.Init {
		it := &cc.Init[i]
		ref := b.Init[it.Name]
		if ref == "" {
			return nil, fmt.Errorf("the release has no image for init step %s", it.Name)
		}
		it.Image, it.Build = ref, ""
	}
	return &cc, nil
}

// intranetDeployment is the standing deployment the intranet tier is
// (§8.2): bound to the Portainer target, fed by promotion, the
// organisation's audience, non-production data.
func (s *Service) intranetDeployment(ctx context.Context, projectID ids.ID, cfg *portainer.Target) (*Deployment, error) {
	t, err := s.Targets.ByName(ctx, IntranetTargetName)
	if err != nil {
		return nil, err
	}
	dep, err := s.Deployments.EnsureStanding(ctx, projectID, cfg.ReleaseDeployment, t.ID, Attributes{Feed: "promotion", Audience: "organisation", DataClass: "non-production", PromoteAfter: "main"})
	if err != nil {
		return nil, err
	}
	if dep.URL == "" {
		url := "https://" + releaseProject(ctx, s.db, projectID).Slug + "." + cfg.AppsDomain
		if err := s.Deployments.RecordAddress(ctx, dep.ID, url); err != nil {
			log.Printf("recording the intranet deployment's address: %v", err)
		}
		dep.URL = url
	}
	return dep, nil
}

func releaseShort(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "(none)"
	}
	return s
}
func releaseProject(ctx context.Context, db database.Queryer, id ids.ID) project.Project {
	p, err := project.ByIDIn(ctx, db, id)
	if _, err := database.Lookup(err); err != nil {
		log.Printf("read failed: %v", err)
	}
	return p
}
