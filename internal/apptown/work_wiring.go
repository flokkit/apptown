package apptown

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/console"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/dataprotection"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/changeadds"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/evals"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/remediation"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workcommands"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/workflowexec"
	"github.com/flokkit/apptown/internal/database"
)

// Work is built once (architecture.md §5.7). AppTown supplies authority, external
// participants and executors; Work connects its own lifecycle components.
func (a *App) workDependencies() workflow.Dependencies {
	commands := workflowexec.NewCommands(a.DB, workflowexec.CommandDependencies{
		Runner: projectProcesses(a), Progress: &a.progress,
		Publisher: repositoryPublisher{secrets: a.Secrets, db: a.DB, dataDir: a.cfg.dataDir, now: a.cfg.clock.Now}, Branches: a.branches, Tasks: a.tasks, Panics: jobReporter{}, Docker: &a.docker,
		Checks: workflowexec.CompileCheck{Run: checksFor(a).Run, Image: checksFor(a).AgentImage}, Catalog: a.Dependencies, Now: a.cfg.clock.Now,
	}, workflowexec.CommandConfig{DataDir: a.cfg.dataDir, Image: a.cfg.agentImage, HostScripts: a.cfg.hostScripts})
	// An AppTown with a worker builds every release there
	// (design/operations/workers.md, "Releases built on the worker").
	var builds *workflowexec.ReleaseBuilds
	if a.cfg.worker != nil {
		builds = &workflowexec.ReleaseBuilds{Tasks: a.tasks, PrivateDocker: a.cfg.privateDocker}
	}
	shipping := workflowexec.NewShipping(a.DB, workflowexec.ShippingDependencies{Deployment: a.Deployment, Previews: a.Preview, Docker: &a.docker, Checks: workflowexec.CompileCheck{Run: checksFor(a).Run, Image: checksFor(a).AgentImage}, Progress: &a.progress, Backups: backupRequests{db: a.DB, life: a.life}, ReleaseData: releaseData{a.DataProtection}, Builds: builds, Now: a.cfg.clock.Now, Screenshots: verification.NewScreenshots(checksFor(a), screenshotsAuthority(a.cfg.localTLS))}, workflowexec.ShippingConfig{DataDir: a.cfg.dataDir, Image: a.cfg.agentImage, Routing: a.cfg.routing()})
	repositories := workflowexec.NewRepositories(a.DB, repositoryProvider{secrets: a.Secrets, dataDir: a.cfg.dataDir}, a.branches, workflowexec.RepositoryConfig{DataDir: a.cfg.dataDir, PublicHost: a.cfg.publicHost, Now: a.cfg.clock.Now})
	runners := make(map[string]workflow.Runner)
	runners[workflowexec.CallKind] = workflowexec.NewCalls(a.DB, jobReporter{}).Run
	runners[workflowexec.MergeRequestKind] = repositories.RunSCMMR
	runners[workflowexec.ReconcileKind] = repositories.RunReconcile
	runners[workflowexec.DeployKind] = shipping.RunDeploy
	runners[workflowexec.PreviewKind] = shipping.RunPreview
	runners[workflowexec.ScriptKind] = commands.RunScript
	runners[workflowexec.TestsKind] = commands.RunTests
	runners[verification.GuidanceKind] = a.guidance.RunGuidanceConformance
	runners[verification.ScanKind] = a.scanSteps.RunScan
	runners[workflowexec.AgentKind] = a.agents.Run
	runners[workflowexec.ReviewKind] = a.agents.Review
	return workflow.Dependencies{
		StepContext:  projectCacheScope(a.DB),
		Masker:       a.Secrets.Masker,
		Authority:    access.Checked{Authority: identity.Scoped(a.Access, "work"), Projects: a.Projects},
		Scanners:     verification.StageScanners{},
		Laws:         workflowLawRules{},
		Abandonment:  abandonmentEffects{now: a.cfg.clock.Now},
		Completion:   workflowCompletionEffects{now: a.cfg.clock.Now},
		Merge:        mergeEffectsFor(a),
		Landings:     landingAdoptions{},
		LandingMeans: workcommands.LandingSentences{DB: a.DB, Lanes: a.lifecycle},
		InAdvance:    workcommands.LandingSentences{DB: a.DB, Lanes: a.lifecycle},
		ChangeAdds:   changeadds.Comparer{},
		Budget:       remediation.LandingRetryBudget{Now: a.cfg.clock.Now},
		EvalGate:     evalStepGate{a.evalGate},
		Narratives:   narrativeReleaseNotes{},
		Resolve:      a.preparation.Resolve,
		LandingWork:  a.preparation,
		Runners:      runners,
		Recoverers:   map[string]workflow.Runner{workflowexec.ScriptKind: commands.RecoverScript, workflowexec.AgentKind: a.agents.RecoverAgent, workflowexec.ReviewKind: a.agents.RecoverAgent},
		Chats:        holderPresenceFor(a),
		Report:       selfErrorsFor(a).Workflow,
		Slots:        a.capacity.Waiting,
		Git:          (workflow.GitWorkspace{DataDir: a.cfg.dataDir}),
		Repositories: &a.repositories,
		Automatic:    autoStepTypes(),
		ModelRefusal: workflowexec.ModelRefusal(a.modelSelection),
		DataDir:      a.cfg.dataDir,

		CheckPlacement: checkPlacement(a.cfg),
		TaskPlacement:  taskPlacement(a.cfg),
		// AppTown's clock writes every time Work's rules compare: step
		// finishes, Change closes, artifacts, votes and send-backs.
		Now: a.cfg.clock.Now,
	}
}

// changePages links a change to its page on this AppTown's public host.
type changePages struct{ host string }

func (p changePages) URL(id ids.ID) string { return changeactions.URL(p.host, id) }

// mergeEffectsFor assembles effects without retaining the composition root.
func mergeEffectsFor(a *App) workcommands.LandingEffects {
	return workcommands.LandingEffects{
		DB: a.DB, Contracts: a.Projects.Contracts(), DataDir: a.cfg.dataDir,
		Drivers: scm.GitDrivers{Runner: a.processRunner, DataDir: a.cfg.dataDir, Provider: (repositoryProvider{secrets: a.Secrets, dataDir: a.cfg.dataDir}).Client,
			ChangeURL: changePages{host: a.cfg.publicHost}.URL},
		Publisher: repositoryPublisher{secrets: a.Secrets, db: a.DB, dataDir: a.cfg.dataDir, now: a.cfg.clock.Now},
		Now:       a.cfg.clock.Now,
	}
}

// --- the caller of a Work operation -------------------------------------------
// Transport composition supplies the verified identity; it travels in the
// operation's context to the authority. Receipt actor text never substitutes
// for policy.

// asAppTown: AppTown acting for itself in one project through one of its
// own doors (remediation, the engine; the scheduler and webhooks build the
// same principal in their owners). The receipt names the door;
// the inference owner stays "system".
func (a *App) asAppTown(ctx context.Context, projectID ids.ID, via string) context.Context {
	return verifiedContext(ctx, access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: via, ProjectID: projectID})
}

// asEngine: AppTown's engine acting for itself in any project: recording a
// step's attempt, re-checking, redispatching, writing release notes.
func (a *App) asEngine(ctx context.Context) context.Context {
	return a.asAppTown(ctx, ids.Nil, "engine")
}

// evalsDependencies binds the evals owner to the owners it drives: Work for its arms' Changes and runs, the
// lifecycle for copies and their deletion, the model owner for what an arm
// runs, the ledger and complaint box for what it cost and what was said.
func evalsDependencies(a *App) evals.Dependencies {
	return evals.Dependencies{
		Authority: access.Checked{Authority: identity.Scoped(a.Access, "evals"), Projects: a.Projects},
		Projects:  a.Projects, Work: a.Work, Copier: a.lifecycle,
		Models:       evalModels{selection: a.modelSelection, slots: a.Models},
		Spend:        a.Usage.Ledger,
		Complaints:   a.Complaints,
		Deployments:  evalDeployments{deployment: a.Deployment, data: a.DataProtection},
		Deleter:      a.deletion,
		InCopy:       evalInCopy,
		Adoption:     a.Adoption,
		AdoptionJobs: evalAdoptionJobs{db: a.DB},
		Comparer:     fleetReviewsFor(a),
		Packs:        a.Ways,
		Origins:      evalOrigins{dataDir: a.cfg.dataDir},
		DataDir:      a.cfg.dataDir,
		Now:          a.cfg.clock.Now,
		RequestPass:  passRequests{db: a.DB, kind: evals.PassJob}.Request,
	}
}

// evalOrigins reads a source Change's starting commit in its project's
// repository.
type evalOrigins struct{ dataDir string }

func (o evalOrigins) PredatesRename(ctx context.Context, slug, commit string) (bool, error) {
	return scm.PredatesRename(ctx, project.HubPath(o.dataDir, slug), commit)
}

// evalInCopy is an eval's driver acting in one of its copies: the eval
// instrument, bound to that copy, receipts naming the eval.
func evalInCopy(ctx context.Context, evalID, projectID ids.ID) context.Context {
	return access.WithCaller(ctx, access.Caller{Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: "eval", Via: fmt.Sprintf("eval/%d", evalID), ProjectID: projectID},
		Verified: true, Receipt: fmt.Sprintf("eval/%d", evalID)})
}

// evalModels is the model owner's part of an eval.
type evalModels struct {
	selection model.Selection
	slots     *model.Slots
}

func (m evalModels) ArmSlots(ctx context.Context, projectID ids.ID, profile string) (map[string]string, error) {
	return m.selection.ArmSlots(ctx, projectID, profile)
}
func (m evalModels) OfferedProfile(ctx context.Context, profile string) (bool, error) {
	return m.slots.OfferedProfile(ctx, profile)
}
func (m evalModels) Profiles(ctx context.Context) ([]string, error) { return m.slots.Profiles(ctx) }
func (m evalModels) CopyModels(ctx context.Context, from, to ids.ID, profile string, withOverrides bool, who string) error {
	return m.slots.CopyModels(ctx, from, to, profile, withOverrides, who)
}

// evalDeployments takes an eval copy's standing deployments down.
// evalDeployments are an eval copy's deployments: Deployment's records,
// and data protection's take-down.
type evalDeployments struct {
	deployment *deployment.Service
	data       *dataprotection.Service
}

func (d evalDeployments) StandingNames(ctx context.Context, projectID ids.ID) ([]string, error) {
	placed, err := d.deployment.Deployments.ForProject(ctx, projectID)
	var names []string
	for _, p := range placed {
		if p.Kind == deployment.Standing {
			names = append(names, p.Name)
		}
	}
	return names, err
}
func (d evalDeployments) TakeDownWithProject(ctx context.Context, projectID ids.ID, name string) error {
	_, err := d.data.TakeDownWithProject(ctx, projectID, name)
	return err
}

// evalStepGate is the eval gate as Work asks it at each step: why the step
// may not start, "" when it may.
type evalStepGate struct{ gate *evals.Gate }

func (g evalStepGate) RefusalIn(ctx context.Context, q database.Queryer, projectID ids.ID) (string, error) {
	err := g.gate.AdmitIn(ctx, q, projectID)
	if errors.Is(err, evals.ErrRefused) {
		return err.Error(), nil
	}
	return "", err
}

// projectCacheScope scopes a step's work to its project's cache volume
// (agentenv.WithCacheScope); a project that cannot be read leaves the step
// unscoped, which mounts no cache: cold, never another project's.
func projectCacheScope(db *database.DB) func(context.Context, ids.ID) context.Context {
	return func(ctx context.Context, projectID ids.ID) context.Context {
		p, err := project.ByIDIn(ctx, db, projectID)
		if err != nil {
			return ctx
		}
		return agentenv.WithCacheScope(ctx, p.Slug)
	}
}

// screenshotsAuthority is the local authority a preview's screenshots
// trust, as a change chat does: nil on an AppTown without one.
func screenshotsAuthority(t *console.LocalTLS) *verification.LocalAuthority {
	if t == nil {
		return nil
	}
	return &verification.LocalAuthority{CACert: t.CACert, Prelude: console.LocalTLSPreludeAt(verification.LocalCAPath, *t)}
}

// releaseData hands a landing data protection's copy and witness around its
// release (dataprotection.BeforeRelease).
type releaseData struct{ data *dataprotection.Service }

func (r releaseData) BeforeRelease(ctx context.Context, projectID ids.ID, deployment string, synthetic bool) (workflowexec.ReleaseWitness, error) {
	w, err := r.data.BeforeRelease(ctx, projectID, deployment, synthetic)
	if err != nil {
		return nil, err
	}
	return w, nil
}
