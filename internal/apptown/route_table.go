package apptown

import (
	"context"
	"net/http"

	"github.com/flokkit/apptown/internal/gate"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/briefs"
	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/complaint"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	stockdocs "github.com/flokkit/apptown/stock/docs"
)

// appRoutes is the single declaration of HTTP patterns, handlers and route
// authorization. It returns instance-local values; building another Handler
// cannot mutate an existing router or its policy.
func appRoutes(a *App) []routeSpec {
	// Inventory callers may declare routes without a running App.
	var adrs httpapi.ADRs
	var retention httpapi.Retention
	var serviceKinds httpapi.ServiceKinds
	var remediations httpapi.Remediations
	var conformance httpapi.Conformance
	var usage httpapi.Usage
	var timing httpapi.Timing
	var dependencies httpapi.Dependencies
	var jobs httpapi.Jobs
	var projectPolicies httpapi.ProjectPolicies
	var policyLibrary httpapi.PolicyLibrary
	var packLibrary httpapi.PackLibrary
	var systemWorkflows httpapi.SystemWorkflows
	var instanceUpdates httpapi.InstanceUpdates
	docs := docsForRoutes("")
	var changes httpapi.Changes
	var traces httpapi.Traces
	var workspaces httpapi.Workspaces
	var auditLog httpapi.AuditLog
	var deployments httpapi.Deployments
	var health httpapi.Health
	var configuration httpapi.Configuration
	var secrets httpapi.Secrets
	var authorization httpapi.Authorization
	var accounts httpapi.Accounts
	var external httpapi.ExternalSignIn
	var invites httpapi.Invites
	var complaints httpapi.Complaints
	var workflowLibrary httpapi.WorkflowLibrary
	var adoptionCommands httpapi.AdoptionCommands
	var fleet httpapi.Fleet
	var packPull httpapi.PackPull
	var attachments httpapi.Attachments
	var standingApprovals httpapi.StandingApprovals
	var routinePublications httpapi.RoutinePublications
	var guidanceBlocks httpapi.GuidanceBlocks
	var guidance httpapi.Guidance
	var packs httpapi.Packs
	var changeAdmission httpapi.ChangeAdmission
	var catalogChanges httpapi.CatalogChanges
	var runProgress httpapi.RunProgress
	var projectWorkflows httpapi.ProjectWorkflows
	var projectSchedules httpapi.ProjectSchedules
	var projectManifest httpapi.ProjectManifest
	var workflowCatalog httpapi.WorkflowCatalog
	var projectServices httpapi.ProjectServices
	var consoleMessages httpapi.ConsoleMessages
	var chatHistory httpapi.ChatHistory
	var complaintIntake httpapi.ComplaintIntake
	var consoleSessions httpapi.ConsoleSessions
	var consoleEnd httpapi.ConsoleEnd
	var consoleCreation httpapi.ConsoleCreation
	var consoleInvocation httpapi.ConsoleInvocation
	var consoleChanges httpapi.ConsoleChanges
	var consolePreview httpapi.ConsolePreview
	var changePreviews httpapi.ChangePreviews
	var projectDeletion httpapi.ProjectDeletion
	var projectViews httpapi.ProjectViews
	var projectSettings httpapi.ProjectSettings
	var releaseHistory httpapi.ReleaseHistory
	var modelSlots httpapi.ModelSlots
	var modelDiscovery httpapi.ModelDiscovery
	var projectOverview httpapi.ProjectOverview
	var activity httpapi.Activity
	var dashboard httpapi.Dashboard
	var attention httpapi.Attention
	var changeDetail httpapi.ChangeDetail
	var administratorNetworks httpapi.AdministratorNetworks
	var stepInterruption httpapi.StepInterruption
	var integrationDocuments httpapi.IntegrationDocuments
	var apiDescription httpapi.APIDescription
	var projectAdoption httpapi.ProjectAdoption
	var changeNarratives httpapi.ChangeNarratives
	var changePreviewState httpapi.ChangePreviewState
	var landingRestart httpapi.LandingRestart
	var backups httpapi.Backups
	var dataProtection httpapi.DataProtection
	var ops httpapi.Operations
	var targets httpapi.Targets
	var deploymentListing httpapi.DeploymentListing
	var infrastructure httpapi.Infrastructure
	var disks httpapi.Disks
	var executor httpapi.Executor
	var setupStatus httpapi.SetupStatus
	var setupModel httpapi.SetupModel
	var browserErrors httpapi.BrowserErrors
	var approvalBriefs httpapi.ApprovalBriefs
	var packAssistance httpapi.PackAssistance
	var conformanceReviews httpapi.ConformanceReviews
	var whatIfs httpapi.WhatIfs
	var evalRoutes httpapi.Evals
	var remediationChanges httpapi.RemediationChanges
	var scmRemotes httpapi.SCMRemotes
	var connectors httpapi.Connectors
	var integrationListing httpapi.IntegrationListing
	var projectAttachments httpapi.ProjectAttachments
	var releaseOperations httpapi.ReleaseOperations
	var repositoryViews httpapi.RepositoryViews
	var tiers httpapi.Tiers
	var findings httpapi.Findings
	var runs httpapi.Runs
	var members httpapi.Members
	var creation httpapi.ProjectCreation
	var repositorySetup httpapi.RepositorySetup
	var packSetup httpapi.PackSetup
	var changeChecks httpapi.ChangeChecks
	var changeWorkerChats httpapi.ChangeWorkerChats
	var refusedStepWork httpapi.RefusedStepWork
	var features httpapi.Features
	if a != nil {
		features = httpapi.NewFeatures(a.Access)
		docs = docsForRoutes(a.cfg.browserSession().PublicOrigin)
		runs = httpapi.NewRuns(a.Work, a.operations)
		findings = httpapi.NewFindings(a.Projects, a.Work, a.conformance, a.DB, a.cfg.dataDir)
		tiers = httpapi.NewTiers(a.Projects, a.Deployment, a.Preview, a.Work, a.DB, a.cfg.dataDir)
		repositoryViews = httpapi.NewRepositoryViews(a.Projects, a.Access, a.DB, a.cfg.dataDir, repositoryedit.NewIntegrity(&a.repositories, a.processRunner, repositoryPublisher{attachments: a.repositoryAttachments, db: a.DB, now: a.cfg.clock.Now}, a.DB, a.cfg.dataDir, a.cfg.clock.Now), a.Deployment.Releases)
		releaseOperations = httpapi.NewReleaseOperations(a.Projects, a.Deployment, a.Preview, a.Work, a.DB, a.cfg.dataDir, a.cfg.clock.Now)
		projectAttachments = httpapi.NewProjectAttachments(a.Projects, a.Integrations, a.Access, a.changeActions, a.DB, a.cfg.publicHost, a.cfg.clock.Now)
		integrationListing = httpapi.NewIntegrationListing(a.Integrations, a.Deployment, connectorHealthFor(a), a.Access)
		connectors = httpapi.NewConnectors(a.Integrations, a.Access, a.Deployment, connectorHealthFor(a), a.DB, agentenv.RegistryFormats())
		scmRemotes = httpapi.NewSCMRemotes(a.Projects, repositoryProvider{secrets: a.Secrets}, a.repositoryAttachments)
		remediationChanges = httpapi.NewRemediationChanges(a.Projects, a.Access, a.conformance, a.Work, a.lifecycle, a.DB, a.authModeName(), a.cfg.clock.Now)
		conformanceReviews = httpapi.NewConformanceReviews(a.Projects, fleetReviewsFor(a))
		whatIfs = httpapi.NewWhatIfs(a.Projects, a.conformance, fleetReviewsFor(a), a.Ways)
		evalRoutes = httpapi.NewEvals(a.Evals)
		packAssistance = httpapi.NewPackAssistance(a.Ways, a.lifecycle, a.modelSelection, a.inference)
		approvalBriefs = httpapi.NewApprovalBriefs(a.Work, briefs.New(a.Work, a.modelSelection, a.inference, a.Console, a.cfg.clock.Now))
		browserErrors = httpapi.NewBrowserErrors(selfErrorsFor(a))
		setupModel = httpapi.NewSetupModel(a.Secrets, a.Integrations, a.Models, a.DB, a.cfg.clock.Now)
		setupStatus = httpapi.NewSetupStatus(&a.setup, a.modelSelection, a.authModeName() == "open" && a.cfg.listenAddr != "" && !loopbackAddr(a.cfg.listenAddr))
		infrastructure = httpapi.NewInfrastructure(a.Deployment, a.hosts, a.targetProviders, a.Projects, a.DB, a.Access, inventorySourcesFor(a), a.cfg.usingBuiltinDB, builtinDBFor(a.cfg.usingBuiltinDB, a.cfg.dataDir).Container, builtinDBFor(a.cfg.usingBuiltinDB, a.cfg.dataDir).Volume, a.cfg.clock.Now)
		disks = httpapi.NewDisks(a.diskObserver)
		workerAddress := ""
		if a.cfg.worker != nil {
			workerAddress = a.cfg.worker.URL
		}
		// Without a worker, work runs on AppTown's own Docker: the built-in
		// target's, whose CPU and memory its capacity reading holds.
		hostTarget := ""
		if a.cfg.worker == nil && !a.cfg.noLocalDocker {
			hostTarget = a.hosts.BuiltInTarget()
		}
		executor = httpapi.NewExecutor(a.workerObserver, workerAddress, hostTarget, a.Preview, a.Work)
		deploymentListing = httpapi.NewDeploymentListing(a.Projects, a.Deployment, a.cfg.dataDir, a.DataProtection.FillsOf)
		targets = httpapi.NewTargets(a.Deployment, a.targetProviders, a.Projects, a.Access, a.DB, a.cfg.clock.Now)
		backups = httpapi.NewBackups(a.Projects, a.DataProtection, a.Deployment.Releases)
		dataProtection = httpapi.NewDataProtection(a.Projects, a.DataProtection)
		ops = httpapi.NewOperations(a.operations, a.Projects)
		landingRestart = httpapi.NewLandingRestart(a.Projects, a.Work, a.lifecycle, a.DB, a.cfg.clock.Now)
		changePreviewState = httpapi.NewChangePreviewState(a.DB, changePreviewViewsFor(a))
		changeNarratives = httpapi.NewChangeNarratives(a.Work, a.Projects, a.Access, a.narratives, a.authModeName())
		projectAdoption = httpapi.NewProjectAdoption(a.Projects, a.Adoption, a.Ways, a.DB)
		integrationDocuments = httpapi.NewIntegrationDocuments(stockdocs.FS)
		apiDescription = httpapi.NewAPIDescription(stockdocs.FS)
		stepInterruption = httpapi.NewStepInterruption(a.Work, &a.docker, a.tasks)
		administratorNetworks = httpapi.NewAdministratorNetworks(a.Access, a.cfg.proxies)
		changeDetail = httpapi.NewChangeDetail(a.Work, a.ADRs, a.Deployment, a.DB, a.Usage.Ledger, a.cfg.dataDir)
		dashboard = httpapi.NewDashboard(a.ADRs, a.operations)
		attention = httpapi.NewAttention(a.attention)
		activity = httpapi.NewActivity(a.Work, a.Deployment)
		projectOverview = httpapi.NewProjectOverview(a.Projects, a.Work, a.cfg.dataDir, a.Access)
		modelDiscovery = httpapi.NewModelDiscovery(a.providers)
		modelSlots = httpapi.NewModelSlots(a.Models, a.Projects, a.Ways, a.Access, a.DB, a.cfg.clock.Now)
		releaseHistory = httpapi.NewReleaseHistory(a.Projects, a.Work, a.Deployment, a.backupRecords, a.DB)
		projectSettings = httpapi.NewProjectSettings(a.Projects, a.Access, a.lifecycle)
		projectViews = httpapi.NewProjectViews(a.Projects, a.Work, a.ADRs, a.Deployment, a.processRunner, a.branches, a.cfg.dataDir, a.consoleAdmission, a.Access)
		projectDeletion = httpapi.NewProjectDeletion(a.Projects, a.Access, a.deletion)
		changePreviews = httpapi.NewChangePreviews(a.Preview, a.DB, a.cfg.clock.Now)
		consolePreview = httpapi.NewConsolePreview(a.Console, a.Preview, a.DB, a.cfg.publicHost, a.cfg.clock.Now)
		consoleChanges = httpapi.NewConsoleChanges(a.Console, a.Work, a.changeActions, a.DB)
		consoleInvocation = httpapi.NewConsoleInvocation(a.Console, a.Projects, a.Ways, a.Work, a.changeActions, a.DB, a.cfg.publicHost, a.cfg.clock.Now)
		consoleCreation = httpapi.NewConsoleCreation(a.Projects, a.Access, a.consoleAdmission, httpapi.NewConsoleSessions(chatsFor(a), a.cli), a.authModeName())
		consoleEnd = httpapi.NewConsoleEnd(a.changeConsole)
		consoleSessions = httpapi.NewConsoleSessions(chatsFor(a), a.cli)
		complaintIntake = httpapi.NewComplaintIntake(a.Complaints, a.Console, a.DB)
		consoleMessages = httpapi.NewConsoleMessages(chatsFor(a), a.Work, a.DB, a.cfg.dataDir, a.Secrets.Masker)
		chatHistory = httpapi.NewChatHistory(a.Console, a.Projects, a.Access, a.cfg.dataDir, a.Secrets.Masker)
		projectServices = httpapi.NewProjectServices(a.Projects, a.Access, a.repositoryEdits, a.cfg.dataDir, a.authModeName())
		workflowCatalog = httpapi.NewWorkflowCatalog(a.DB, a.Ways)
		projectManifest = httpapi.NewProjectManifest(a.Projects, a.Adoption, a.DB, a.cfg.dataDir, a.Secrets)
		projectWorkflows = httpapi.NewProjectWorkflows(a.Projects, a.Adoption, a.Work, a.DB)
		projectSchedules = httpapi.NewProjectSchedules(a.Projects, changeactions.Schedules{Actions: a.changeActions, Adoption: a.Adoption, Projects: a.Projects, DB: a.DB}, a.cfg.clock.Now)
		runProgress = httpapi.NewRunProgress(a.Work, &a.progress)
		catalogChanges = httpapi.NewCatalogChanges(a.Ways, a.Adoption, a.Projects, a.Access, a.changeActions, a.authModeName())
		changeAdmission = httpapi.NewChangeAdmission(a.Projects, a.Access, a.Work, a.changeActions, a.DB, a.authModeName())
		packs = httpapi.NewPacks(a.Ways, a.DB, httpapi.NewSteeringInventory(a.steering, piHomeFor(a), httpapi.PlatformSteering{Complaints: complaint.AgentInstructions, Hygiene: agentenv.WorkspaceHygiene, Runtime: agentenv.RuntimeContractInstructions, Docker: agentenv.DockerInstructions}))
		guidance = httpapi.NewGuidance(a.Ways, a.Adoption, a.Projects, a.lifecycle, httpapi.NewSteeringInventory(a.steering, piHomeFor(a), httpapi.PlatformSteering{Complaints: complaint.AgentInstructions, Hygiene: agentenv.WorkspaceHygiene, Runtime: agentenv.RuntimeContractInstructions, Docker: agentenv.DockerInstructions}))
		guidanceBlocks = httpapi.NewGuidanceBlocks(a.Ways, a.Adoption, a.Access)
		attachments = httpapi.NewAttachments(a.attachments, a.cfg.dataDir)
		standingApprovals = httpapi.NewStandingApprovals(a.standingRules)
		routinePublications = httpapi.NewRoutinePublications(a.routinePublications)
		packPull = httpapi.NewPackPull(a.Adoption, a.lifecycle, a.Projects, a.Access, a.Ways, a.DB, a.cfg.clock.Now)
		fleet = httpapi.NewFleet(a.Adoption, a.Ways)
		adoptionCommands = httpapi.NewAdoptionCommands(a.Adoption, a.conformance, a.Projects, a.Access, a.Work, a.lifecycle)
		workflowLibrary = httpapi.NewWorkflowLibrary(a.Ways, a.Access, a.Projects)
		complaints = httpapi.NewComplaints(a.Complaints, a.Access, complaint.AgentInstructions, complaint.ConsoleRules)
		accounts = httpapi.NewAccounts(a.Access)
		external = httpapi.NewExternalSignIn(a.Access)
		invites = httpapi.NewInvites(a.Access, a.cfg.browserSession(), a.cfg.proxies, a.cfg.authMode == "internal")
		authorization = httpapi.NewAuthorization(a.Access, a.Projects)
		secrets = httpapi.NewSecrets(a.Secrets, a.Projects, a.Deployment, a.DB, a.cfg.dataDir, a.cfg.clock.Now)
		configuration = httpapi.NewConfiguration(a.Dependencies, a.cfg.stock.Tools)
		health = httpapi.NewHealth(a.DB.Pool().PingContext)
		deployments = httpapi.NewDeployments(a.Deployment, a.DataProtection, a.Projects, a.Access)
		auditLog = httpapi.NewAuditLog(a.DB, a.Projects)
		workspaces = httpapi.NewWorkspaces(a.DB, a.cfg.dataDir)
		traces = httpapi.NewTraces(a.Work, a.Ways, a.DB)
		changes = httpapi.NewChanges(a.Work, a.Projects, a.Access, a.DB, a.cfg.clock.Now)
		instanceUpdates = httpapi.NewInstanceUpdates(a.InstanceUpdates)
		systemWorkflows = httpapi.NewSystemWorkflows(a.Ways, a.Access)
		packLibrary = httpapi.NewPackLibrary(a.Ways, a.conformance, a.Access, a.DB, a.cfg.clock.Now)
		policyLibrary = httpapi.NewPolicyLibrary(a.Ways, a.Adoption, a.DB)
		projectPolicies = httpapi.NewProjectPolicies(a.Projects, a.Access, a.Ways, a.Adoption, a.DB)
		jobs = httpapi.NewJobs(a.DB)
		dependencies = httpapi.NewDependencies(a.Dependencies, a.Access)
		usage = httpapi.NewUsage(a.Usage, a.Projects)
		timing = httpapi.NewTiming(a.Work, a.Usage.Ledger)
		conformance = httpapi.NewConformance(a.conformance, a.Projects, a.cfg.clock.Now)
		remediations = httpapi.NewRemediations(a.Fixes)
		serviceKinds = httpapi.NewServiceKinds(a.Integrations, a.Access)
		retention = httpapi.NewRetention(a.Deployment, a.Projects)
		adrs = httpapi.NewADRs(a.ADRs, a.Projects)
		members = httpapi.NewMembers(a.Projects, a.Access)
		creation = httpapi.NewProjectCreation(a.lifecycle, a.Access)
		repositorySetup = httpapi.NewRepositorySetup(a.Projects, a.lifecycle, a.Access)
		packSetup = httpapi.NewPackSetup(a.Projects, a.lifecycle, a.Ways)
		changeWorkerChats = httpapi.NewChangeWorkerChats(a.DB, a.changeConsole)
		refusedStepWork = httpapi.NewRefusedStepWork(a.DB, a.cfg.dataDir)
		changeChecks = httpapi.NewChangeChecks(a.DB, a.verification)
	}
	return []routeSpec{
		publicRoute("GET /api/v1/features", features.List, "features.List: area names and current switches, no secrets").inArea("setup"),
		adminRoute("PUT /api/v1/features/{area}", features.Set).inArea("setup"),
		filteredRoute("GET /api/v1/dashboard", dashboard.Dashboard, "dashboard.Dashboard").inArea("setup"),
		filteredRoute("GET /api/v1/attention", attention.Read, "attention.Read").inArea("setup"),

		filteredRoute("GET /api/v1/projects", projectViews.ListProjects, "projectViews.ListProjects").inArea("projects"),
		ownerRoute("GET /api/v1/project-creation-targets", creation.CreationTargets, "creation.CreationTargets").inArea("setup"),
		ownerRoute("POST /api/v1/projects", creation.Create, "creation.Create").inArea("projects"), // decides inside: project:create at the owning group
		requireRoute("GET /api/v1/projects/{slug}", projectViews.GetProject, access.CapProjectRead, "project-slug").inArea("projects"),
		adminRoute("PATCH /api/v1/projects/{slug}", projectSettings.PatchProject).inArea("projects"),
		requireRoute("PUT /api/v1/projects/{slug}/approval-policy", projectPolicies.ProjectApprovalPolicy, access.CapProjectConfigure, "project-slug").inArea("projects"),
		ownerRoute("DELETE /api/v1/projects/{slug}", projectDeletion.DeleteProject, "projectlifecycle.Request").inArea("projects"),
		requireRoute("GET /api/v1/projects/{slug}/deletion", projectDeletion.ProjectDeletionStatus, access.CapProjectRead, "project-slug").inArea("projects"),
		ownerRoute("POST /api/v1/projects/{slug}/deletion/cancel", projectDeletion.CancelProjectDeletion, "projectlifecycle.Cancel").inArea("projects"),
		ownerRoute("POST /api/v1/projects/{slug}/restore", projectDeletion.RestoreProject, "projectlifecycle.Restore").inArea("projects"),
		ownerRoute("POST /api/v1/projects/{slug}/purge", projectDeletion.PurgeProject, "projectlifecycle.PurgeNow").inArea("projects"),
		adminRoute("GET /api/v1/project-cleanups", projectDeletion.ProjectCleanups).inArea("setup"),
		adminRoute("POST /api/v1/project-cleanups/{id}/retry", projectDeletion.RetryProjectCleanup).inArea("setup"),
		requireRoute("POST /api/v1/projects/{slug}/repo", repositorySetup.Initialize, access.CapProjectImport, "project-slug").inArea("projects"),
		requireRoute("POST /api/v1/projects/{slug}/repo/from-url", repositorySetup.SeedFromURL, access.CapProjectImport, "project-slug").inArea("projects"),
		requireRoute("POST /api/v1/projects/{slug}/repo/from-bundle", repositorySetup.SeedFromBundle, access.CapProjectImport, "project-slug").inArea("projects"),
		requireRoute("GET /api/v1/projects/{slug}/repo/bundle", repositorySetup.DownloadCode, access.CapProjectRead, "project-slug").inArea("projects"),
		adminRoute("POST /api/v1/projects/{slug}/repo/integrity", repositoryViews.RepoIntegrity).inArea("projects"),
		// Where the project's code is pushed: an integration, decided as one.
		requireRoute("GET /api/v1/projects/{slug}/repo/remote", scmRemotes.RemoteState, access.CapProjectIntegrations, "project-slug").inArea("projects"),
		requireRoute("POST /api/v1/projects/{slug}/repo/remote", scmRemotes.AttachRemote, access.CapProjectIntegrations, "project-slug").inArea("projects"),
		requireRoute("GET /api/v1/projects/{slug}/workflows", projectWorkflows.ListWorkflows, access.CapProjectRead, "project-slug").inArea("projects"),
		requireRoute("GET /api/v1/projects/{slug}/lanes", projectWorkflows.ProjectLanes, access.CapProjectRead, "project-slug").inArea("projects"),
		requireRoute("GET /api/v1/projects/{slug}/schedules", projectSchedules.List, access.CapProjectRead, "project-slug").inArea("projects"),
		requireRoute("GET /api/v1/instance-updates", instanceUpdates.Status, access.CapInstanceUpdates, "instance").inArea("updates"),
		requireRoute("POST /api/v1/instance-updates/check", instanceUpdates.Check, access.CapInstanceUpdates, "instance").inArea("updates"),
		publicRoute("GET /api/v1/system-workflows", systemWorkflows.SystemWorkflows, "systemWorkflows.SystemWorkflows: shared system workflow declarations").inArea("ways"),
		requireRoute("PUT /api/v1/system-workflows", systemWorkflows.PutSystemWorkflow, access.CapExecWorkflows, "instance").inArea("ways"),
		ownerRoute("GET /api/v1/conformance", conformance.Conformance, "conformance.Conformance").inArea("conformance-estate"),
		adminRoute("POST /api/v1/conformance/review", conformanceReviews.RunConformanceReview).inArea("conformance"),
		ownerRoute("POST /api/v1/conformance/what-if", whatIfs.StartWhatIf, "conformance.BeginWhatIf: the authority of a fleet review").inArea("what-ifs"),
		ownerRoute("GET /api/v1/conformance/what-if", whatIfs.LatestWhatIf, "conformance.LatestWhatIf: conformance read").inArea("what-ifs"),
		ownerRoute("GET /api/v1/conformance/what-if/{id}", whatIfs.WhatIf, "conformance.WhatIf: conformance read").inArea("what-ifs"),
		ownerRoute("GET /api/v1/conformance/what-ifs", whatIfs.RecentWhatIfs, "conformance.RecentWhatIfs: conformance read").inArea("what-ifs"),
		ownerRoute("POST /api/v1/evals", evalRoutes.StartEval, "evals.Start: standards:manage-packs and project:read on every source project").inArea("evals"),
		ownerRoute("GET /api/v1/evals", evalRoutes.ListEvals, "evals.List: standards:read-evals").inArea("evals"),
		ownerRoute("GET /api/v1/evals/start", evalRoutes.EvalStartForm, "evals.StartForm: standards:manage-packs").inArea("evals"),
		ownerRoute("GET /api/v1/evals/{id}", byNumber(func(ctx context.Context, n int64) (ids.ID, error) { return a.Evals.IDByNumber(ctx, n) }, evalRoutes.EvalScorecard), "evals.Scorecard: standards:read-evals").inArea("evals"),
		ownerRoute("GET /api/v1/evals/{id}/arms/{arm}/diff", byNumber(func(ctx context.Context, n int64) (ids.ID, error) { return a.Evals.IDByNumber(ctx, n) }, evalRoutes.EvalArmDiff), "evals.ArmDiff: standards:read-evals and project:read on the source project, the arm found within its eval").inArea("evals"),
		ownerRoute("POST /api/v1/evals/{id}/continue", byNumber(func(ctx context.Context, n int64) (ids.ID, error) { return a.Evals.IDByNumber(ctx, n) }, evalRoutes.ContinueEval), "evals.Continue: the starter, still a standards manager").inArea("evals"),
		ownerRoute("DELETE /api/v1/evals/{id}", byNumber(func(ctx context.Context, n int64) (ids.ID, error) { return a.Evals.IDByNumber(ctx, n) }, evalRoutes.DeleteEval), "evals.Delete: an administrator, or the starter still a standards manager").inArea("evals"),
		ownerRoute("GET /api/v1/conformance/reviews/{id}", byNumber(func(ctx context.Context, n int64) (ids.ID, error) { return a.conformance.ReviewIDByNumber(ctx, n) }, conformance.ReviewDetail), "conformance.ReviewDetail").inArea("conformance"),
		ownerRoute("GET /api/v1/conformance/reviews", conformance.ListReviews, "conformance.ListReviews").inArea("conformance"),
		publicRoute("GET /tls/ask", func(w http.ResponseWriter, r *http.Request) {
			certificateNames{publicHost: a.cfg.publicHost, tiers: a.cfg.tierDomains, rules: a.vhostRules, projects: a.Projects, deployment: a.Deployment}.ask(w, r)
		}, "certificateNames.ask: a TLS proxy on this machine asks whether a name may have a certificate; answers loopback callers only, names bounded by AppTown's records; outside /api/ so no sign-in mode stands in front of it, as /healthz").inArea("setup"),
		publicRoute("GET /healthz", health.Healthz, "httpapi.Health: up or down for a load balancer, nothing else").inArea("setup"),
		publicRoute("GET /api/v1/errors/browser", browserErrors.BrowserErrors, "browserErrors.BrowserErrors: shared browser diagnostic configuration").inArea("setup"),
		memberRoute("GET /api/v1/conformance/projects/{slug}/history", conformance.ProjectConformanceHistory, "project-slug").inArea("conformance"),
		ownerRoute("POST /api/v1/conformance/verdicts/{id}/confirm", conformance.ConfirmVerdict, "conformance.ConfirmVerdict").inArea("conformance"),
		ownerRoute("POST /api/v1/conformance/remediate", remediationChanges.OpenRemediation, "remediationChanges.OpenRemediation").inArea("conformance"),
		requireRoute("GET /api/v1/conformance/remediations", remediations.ListRemediations, access.CapConformanceRead, "instance").inArea("conformance"),
		requireRoute("POST /api/v1/conformance/remediations", remediations.StartRemediation, access.CapConformanceFix, "instance").inArea("conformance"),
		requireRoute("GET /api/v1/conformance/remediations/{id}", byNumber(func(ctx context.Context, n int64) (ids.ID, error) { return a.Fixes.IDByNumber(ctx, n) }, remediations.Remediation), access.CapConformanceRead, "instance").inArea("conformance"),
		requireRoute("POST /api/v1/conformance/remediations/{id}/{action}", byNumber(func(ctx context.Context, n int64) (ids.ID, error) { return a.Fixes.IDByNumber(ctx, n) }, remediations.RemediationAction), access.CapConformanceFix, "instance").inArea("conformance"),
		requireRoute("POST /api/v1/conformance/remediations/{id}/items/{item}/{action}", byNumber(func(ctx context.Context, n int64) (ids.ID, error) { return a.Fixes.IDByNumber(ctx, n) }, remediations.RemediationItemAction), access.CapConformanceFix, "instance").inArea("conformance"),
		memberRoute("GET /api/v1/conformance/projects/{slug}/fixes", conformance.ProjectFixes, "project-slug").inArea("conformance"),
		requireRoute("GET /api/v1/workers", remediations.Workers, access.CapConformanceRead, "instance").inArea("worker"),
		adminRoute("PUT /api/v1/workers", remediations.Workers).inArea("worker"),
		publicRoute("GET /api/v1/config/schema", configuration.ConfigSchema, "configuration.ConfigSchema: shared contract schema").inArea("setup"),
		publicRoute("POST /api/v1/config/validate", configuration.ConfigValidate, "configuration.ConfigValidate: validate caller-supplied configuration without persisting it").inArea("setup"),
		publicRoute("POST /api/v1/dev-compose", configuration.DevCompose, "configuration.DevCompose: render caller-supplied local compose configuration").inArea("setup"),
		requireRoute("POST /api/v1/changes/{id}/check", changeChecks.Check, access.CapChangeRunChecks, "change").inArea("landing"),
		publicRoute("GET /api/v1/workflows/library", workflowCatalog.LibraryList, "workflowCatalog.LibraryList: shared published workflow library").inArea("ways"),
		requireRoute("POST /api/v1/workflows/library", workflowLibrary.PutLibraryWorkflow, access.CapExecWorkflows, "instance").inArea("ways"),
		requireRoute("DELETE /api/v1/workflows/library/{name}", workflowLibrary.DeleteLibraryWorkflow, access.CapExecWorkflows, "instance").inArea("ways"),
		publicRoute("GET /api/v1/packs", packs.Packs, "packs.Packs: shared published pack library").inArea("ways"),
		publicRoute("POST /api/v1/packs/interview", packAssistance.PackInterview, "packAssistance.PackInterview: authenticated pack-selection assistance").inArea("ways"),
		requireRoute("POST /api/v1/packs", packLibrary.SavePack, access.CapStandardsPacks, "instance").inArea("ways"),
		requireRoute("DELETE /api/v1/packs/{name}", packLibrary.DeletePack, access.CapStandardsPacks, "instance").inArea("ways"),
		requireRoute("POST /api/v1/packs/{name}/enabled", packLibrary.SetPackEnabled, access.CapStandardsPacks, "instance").inArea("ways"),
		requireRoute("POST /api/v1/packs/{name}/recommended", packLibrary.RecommendPackVersion, access.CapStandardsPacks, "instance").inArea("ways"),
		publicRoute("GET /api/v1/packs/{name}", packs.PackHistory, "packs.PackHistory: a pack's declaration, versions and recommendation, like the shared pack library").inArea("ways"),
		requireRoute("POST /api/v1/projects/{slug}/pack", packSetup.ApplyPack, access.CapProjectWayOfWorking, "project-slug").inArea("projects"),
		requireRoute("GET /api/v1/projects/{slug}/pack/pull-plan", packPull.PackPullPlan, access.CapProjectRead, "project-slug").inArea("projects"),
		requireRoute("POST /api/v1/projects/{slug}/pack/pull", packPull.PackPull, access.CapProjectWayOfWorking, "project-slug").inArea("projects"),
		// Commits the toolchain declaration to the default branch: part of the way of working.
		requireRoute("POST /api/v1/projects/{slug}/toolchain", packSetup.SeedToolchain, access.CapProjectWayOfWorking, "project-slug").inArea("projects"),
		requireRoute("POST /api/v1/runs/{id}/abandon", runs.AbandonRun, access.CapRunOperate, "run").inArea("changes"),
		requireRoute("POST /api/v1/runs/{id}/restart-landing", landingRestart.RestartLanding, access.CapChangeLand, "run").inArea("landing"),
		requireRoute("GET /api/v1/model-slots", modelSlots.ModelSlots, access.CapProjectRead, "instance").inArea("models"),
		publicRoute("GET /api/v1/model-catalog", modelDiscovery.ModelCatalog, "modelDiscovery.ModelCatalog: shared credential-free model catalogue").inArea("models"),
		requireRoute("POST /api/v1/integrations/discover", modelDiscovery.DiscoverModels, access.CapModelsBindings, "instance").inArea("models"),
		requireRoute("PUT /api/v1/model-slots", modelSlots.PutModelSlot, access.CapModelsSlots, "instance").inArea("models"),
		requireRoute("GET /api/v1/projects/{slug}/model-profile", modelSlots.ProjectModels, access.CapProjectRead, "project-slug").inArea("models"),
		requireRoute("PUT /api/v1/projects/{slug}/model-profile", modelSlots.PutProjectModelProfile, access.CapProjectModelProfile, "project-slug").inArea("models"),
		requireRoute("GET /api/v1/projects/{slug}/manifest", projectManifest.ProjectManifest, access.CapProjectRead, "project-slug").inArea("projects"),
		publicRoute("GET /api/v1/workflows/schema", workflowCatalog.WorkflowSchema, "workflowCatalog.WorkflowSchema: shared workflow format schema").inArea("setup"),
		publicRoute("POST /api/v1/workflows/validate", workflowCatalog.ValidateWorkflow, "workflowCatalog.ValidateWorkflow: validate caller-supplied workflow without publication").inArea("setup"),
		requireRoute("GET /api/v1/projects/{slug}/board", changes.Board, access.CapProjectRead, "project-slug").inArea("projects"),
		requireRoute("GET /api/v1/projects/{slug}/deployments", deploymentListing.Deployments, access.CapDeploymentRead, "project-slug").inArea("deployments"),
		ownerRoute("GET /api/v1/projects/{slug}/release/default", deployments.ReleaseDefault, "deployments.ReleaseDefault").inArea("releases"),
		ownerRoute("PUT /api/v1/projects/{slug}/release/default", deployments.SetReleaseDefault, "deployments.SetReleaseDefault").inArea("releases"),
		ownerRoute("PUT /api/v1/projects/{slug}/deployments/{id}/label", deployments.EditDeploymentLabel, "deployments.EditDeploymentLabel").inArea("deployments"),
		ownerRoute("GET /api/v1/projects/{slug}/deployments/{id}/address", deployments.DeploymentAddress, "deployments.DeploymentAddress").inArea("deployments"),
		ownerRoute("PUT /api/v1/projects/{slug}/deployments/{id}/address", deployments.EditDeploymentAddress, "deployments.EditDeploymentAddress").inArea("deployments"),
		ownerRoute("PUT /api/v1/targets/{id}/label", targets.EditTargetLabel, "targets.EditTargetLabel").inArea("deployments"),
		requireRoute("PUT /api/v1/integrations/{id}/label", connectors.EditConnectionLabel, access.CapIntegrationConnector, "instance").inArea("setup"),
		ownerRoute("POST /api/v1/deployments/confirm-feed", deployments.ConfirmAllFeeds, "deployments.ConfirmAllFeeds").inArea("deployments"),
		ownerRoute("POST /api/v1/projects/{slug}/deployments/{name}/confirm-feed", deployments.ConfirmFeed, "deployments.ConfirmFeed").inArea("deployments"),
		ownerRoute("PUT /api/v1/projects/{slug}/deployments/{name}/feed", deployments.SetFeed, "deployments.SetFeed").inArea("deployments"),
		ownerRoute("POST /api/v1/projects/{slug}/deployments", deployments.CreateDeployment, "deployments.CreateDeployment").inArea("deployments"),
		ownerRoute("DELETE /api/v1/projects/{slug}/deployments/{name}", deployments.DeleteDeployment, "deployments.DeleteDeployment").inArea("deployments"),
		ownerRoute("POST /api/v1/projects/{slug}/deployments/{name}/deploy", deployments.DeployDeployment, "deployments.DeployDeployment").inArea("deployments"),
		requireRoute("POST /api/v1/projects/{slug}/deployments/{name}/start", deployments.StartRuntime, access.CapDeploymentOperate, "project-slug").inArea("deployments"),
		requireRoute("POST /api/v1/projects/{slug}/deployments/{name}/stop", deployments.StopRuntime, access.CapDeploymentOperate, "project-slug").inArea("deployments"),
		requireRoute("POST /api/v1/projects/{slug}/deployments/{name}/restart", deployments.RestartRuntime, access.CapDeploymentOperate, "project-slug").inArea("deployments"),
		requireRoute("POST /api/v1/projects/{slug}/deployments/{name}/check", deployments.CheckAgain, access.CapDeploymentOperate, "project-slug").inArea("deployments"),
		requireRoute("GET /api/v1/projects/{slug}/deployments/{name}/ai-models", deployments.AIModels, access.CapDeploymentRead, "project-slug").inArea("deployments"),
		ownerRoute("GET /api/v1/projects/{slug}/deployments/{name}/resources", deployments.DeploymentResources, "deployments.DeploymentResources").inArea("deployments"),
		ownerRoute("GET /api/v1/projects/{slug}/deployments/{name}/diagnostics", deployments.DeploymentDiagnostics, "deployments.DeploymentDiagnostics").inArea("deployments"),
		ownerRoute("GET /api/v1/projects/{slug}/deployments/{name}/logs", deployments.DeploymentLog, "deployments.DeploymentLog").inArea("deployments"),
		ownerRoute("PUT /api/v1/projects/{slug}/deployments/{name}/resources", deployments.SetResourceOverride, "deployments.SetResourceOverride").inArea("deployments"),
		ownerRoute("DELETE /api/v1/projects/{slug}/deployments/{name}/resources", deployments.ClearResourceOverride, "deployments.ClearResourceOverride").inArea("deployments"),
		requireRoute("GET /api/v1/projects/{slug}/deployments/{name}/backups", backups.ListBackups, access.CapDeploymentRead, "project-slug").inArea("backups"),
		requireRoute("POST /api/v1/projects/{slug}/deployments/{name}/backups", backups.TakeBackup, access.CapDeploymentDeploy, "project-slug").inArea("backups"),
		requireRoute("POST /api/v1/projects/{slug}/deployments/{name}/backups/{id}/restore", backups.RestoreBackup, access.CapDeploymentDeploy, "project-slug").inArea("backups"),
		requireRoute("GET /api/v1/backups", backups.AllBackups, access.CapDeploymentRead, "instance").inArea("backups"),
		requireRoute("GET /api/v1/backup-store", dataProtection.Store, access.CapDeploymentRead, "instance").inArea("backups"),
		requireRoute("PUT /api/v1/backup-store", dataProtection.AttachStore, access.CapPlatformTargets, "instance").inArea("backups"),
		requireRoute("GET /api/v1/projects/{slug}/deployments/{name}/protection", dataProtection.Protection, access.CapDeploymentRead, "project-slug").inArea("deployments"),
		requireRoute("POST /api/v1/projects/{slug}/deployments/{name}/copies", dataProtection.Copy, access.CapDeploymentDeploy, "project-slug").inArea("backups"),
		requireRoute("POST /api/v1/projects/{slug}/deployments/{name}/drills", dataProtection.Drill, access.CapDeploymentRestore, "project-slug").inArea("backups"),
		requireRoute("POST /api/v1/projects/{slug}/deployments/{name}/rehearsals", dataProtection.Rehearse, access.CapDeploymentRestore, "project-slug").inArea("restore-rehearsals"),
		requireRoute("POST /api/v1/projects/{slug}/deployments/{name}/restored-copies", dataProtection.StartRestoredCopy, access.CapDeploymentRestore, "project-slug").inArea("restore-rehearsals"),
		requireRoute("DELETE /api/v1/projects/{slug}/restored-copies/{id}", dataProtection.TakeDownRestoredCopy, access.CapDeploymentRestore, "project-slug").inArea("restore-rehearsals"),
		requireRoute("GET /api/v1/projects/{slug}/final-copies", dataProtection.FinalCopies, access.CapDeploymentRead, "project-slug").inArea("backups"),
		ownerRoute("GET /api/v1/operations/{id}", byNumber(func(ctx context.Context, n int64) (ids.ID, error) { return a.operations.IDByNumber(ctx, n) }, ops.Get), "operations.Get").inArea("record"),
		ownerRoute("POST /api/v1/operations/{id}/cancel", byNumber(func(ctx context.Context, n int64) (ids.ID, error) { return a.operations.IDByNumber(ctx, n) }, ops.Cancel), "operations.Cancel").inArea("record"),
		ownerRoute("POST /api/v1/operations/{id}/dismiss", byNumber(func(ctx context.Context, n int64) (ids.ID, error) { return a.operations.IDByNumber(ctx, n) }, ops.Dismiss), "operations.Dismiss").inArea("record"),
		requireRoute("POST /api/v1/infrastructure/{target}/remove", infrastructure.RemoveInfrastructure, access.CapPlatformTargets, "instance").inArea("deployments"),
		requireRoute("GET /api/v1/targets", targets.Targets, access.CapDeploymentRead, "instance").inArea("deployments"),
		requireRoute("POST /api/v1/targets", targets.AttachTarget, access.CapPlatformTargets, "instance").inArea("deployments"),
		requireRoute("GET /api/v1/targets/{name}", targets.TargetPage, access.CapDeploymentRead, "instance").inArea("deployments"),
		requireRoute("GET /api/v1/targets/{name}/capacity", targets.TargetCapacity, access.CapDeploymentRead, "instance").inArea("deployments"),
		requireRoute("PATCH /api/v1/targets/{name}", targets.EditTarget, access.CapPlatformTargets, "instance").inArea("deployments"),
		requireRoute("POST /api/v1/targets/{name}/check", targets.CheckTarget, access.CapPlatformTargets, "instance").inArea("deployments"),
		requireRoute("POST /api/v1/targets/{name}/retire", targets.RetireTarget, access.CapPlatformTargets, "instance").inArea("deployments"),
		requireRoute("POST /api/v1/targets/{name}/offers", targets.OfferTarget, access.CapPlatformTargets, "instance").inArea("deployments"),
		requireRoute("DELETE /api/v1/targets/{name}/offers/{id}", targets.RevokeOffer, access.CapPlatformTargets, "instance").inArea("deployments"),
		requireRoute("GET /api/v1/projects/{slug}/targets", targets.ProjectTargets, access.CapDeploymentRead, "project-slug").inArea("deployments"),
		requireRoute("GET /api/v1/retained", retention.Retained, access.CapDeploymentRead, "instance").inArea("setup"),
		requireRoute("GET /api/v1/infrastructure", infrastructure.Infrastructure, access.CapDeploymentRead, "instance").inArea("deployments"),
		requireRoute("GET /api/v1/executor", executor.Get, access.CapRunRead, "instance").inArea("worker"),
		requireRoute("GET /api/v1/infrastructure/disks", disks.Disks, access.CapDeploymentRead, "instance").inArea("usage"),
		requireRoute("GET /api/v1/jobs", jobs.ListJobs, access.CapDeploymentRead, "instance").inArea("record"),
		requireRoute("GET /api/v1/projects/{slug}/retained", retention.ProjectRetained, access.CapDeploymentRead, "project-slug").inArea("projects"),
		requireRoute("GET /api/v1/projects/{slug}/adoption", projectAdoption.ProjectAdoption, access.CapProjectRead, "project-slug").inArea("projects"),
		requireRoute("POST /api/v1/projects/{slug}/adoption/prepare", adoptionCommands.Prepare, access.CapProjectRead, "project-slug").inArea("projects"),
		requireRoute("POST /api/v1/projects/{slug}/adoption/adopt", adoptionCommands.Adopt, access.CapProjectWayOfWorking, "project-slug").inArea("projects"),
		requireRoute("GET /api/v1/projects/{slug}/adoption/in-advance", adoptionCommands.InAdvance, access.CapProjectRead, "project-slug").inArea("projects"),
		filteredRoute("GET /api/v1/wow", fleet.Fleet, "fleet.Fleet").inArea("setup"),
		ownerRoute("POST /api/v1/projects/{slug}/retained/{id}", retention.SettleRetained, "retention.SettleRetained").inArea("projects"),
		publicRoute("GET /api/v1/setup", setupStatus.Setup, "setupStatus.Setup: shared setup readiness projection").inArea("setup"),
		publicRoute("GET /api/v1/openapi.json", apiDescription.Serve, "apiDescription.Serve: the build's shipped API description, the same for every caller").inArea("setup"),
		requireRoute("POST /api/v1/setup/model", setupModel.SetupModel, access.CapModelsBindings, "instance").inArea("models"),
		filteredRoute("GET /api/v1/tiers", tiers.Tiers, "tiers.Tiers").inArea("deployments"),
		publicRoute("POST /api/v1/complaints", complaintIntake.Complain, "complaintIntake.Complain: complaint intake with its existing optional scope validation").inArea("complaints"),
		requireRoute("GET /api/v1/complaints", complaints.ListComplaints, access.CapProjectRead, "instance").inArea("complaints"),
		requireRoute("POST /api/v1/complaints/seen", complaints.ComplaintsSeen, access.CapProjectRead, "instance").inArea("complaints"),
		adminRoute("POST /api/v1/complaints/{id}/answer", byNumber(func(ctx context.Context, n int64) (ids.ID, error) { return a.Complaints.IDByNumber(ctx, n) }, complaints.AnswerComplaint)).inArea("complaints"),
		adminRoute("POST /api/v1/complaints/{id}/archive", byNumber(func(ctx context.Context, n int64) (ids.ID, error) { return a.Complaints.IDByNumber(ctx, n) }, complaints.ArchiveComplaint)).inArea("complaints"),
		adminRoute("POST /api/v1/complaints/archive-older", complaints.ArchiveOldComplaints).inArea("complaints"),
		adminRoute("DELETE /api/v1/complaints/{id}", byNumber(func(ctx context.Context, n int64) (ids.ID, error) { return a.Complaints.IDByNumber(ctx, n) }, complaints.DeleteComplaint)).inArea("complaints"),
		publicRoute("GET /api/v1/complaints/steering", complaints.ComplaintSteering, "complaints.ComplaintSteering: shared complaint submission instructions").inArea("complaints"),
		filteredRoute("GET /api/v1/tiers/departures", tiers.Departures, "tiers.Departures").inArea("deployments"),
		requireRoute("GET /api/v1/projects/{slug}/overview", projectOverview.ProjectOverview, access.CapProjectRead, "project-slug").inArea("projects"),
		filteredRoute("GET /api/v1/activity", activity.Activity, "activity.Activity").inArea("setup"),
		requireRoute("GET /api/v1/projects/{slug}/changes", changes.ListChanges, access.CapChangeList, "project-slug").inArea("changes"),
		requireRoute("GET /api/v1/projects/{slug}/members", members.ListMembers, access.CapProjectRead, "project-slug").inArea("projects"),
		requireRoute("POST /api/v1/projects/{slug}/members", members.Add, access.CapProjectManageMembers, "project-slug").inArea("projects"),
		requireRoute("POST /api/v1/projects/{slug}/changes", changeAdmission.CreateChange, access.CapChangeCreate, "project-slug").inArea("changes"),
		requireRoute("GET /api/v1/projects/{slug}/release-bundles", releaseHistory.ListReleaseBundles, access.CapReleaseRead, "project-slug").inArea("releases"),
		requireRoute("GET /api/v1/projects/{slug}/releases", releaseHistory.ListReleases, access.CapReleaseRead, "project-slug").inArea("releases"),
		ownerRoute("POST /api/v1/projects/{slug}/release", releaseOperations.LANRelease, "deployment.RequestDefaultRelease").inArea("releases"),
		requireRoute("GET /api/v1/projects/{slug}/release/plan", releaseOperations.ReleasePlan, access.CapReleaseRead, "project-slug").inArea("releases"),
		requireRoute("GET /api/v1/projects/{slug}/releases/{number}", releaseHistory.ReleaseDetail, access.CapReleaseRead, "project-slug").inArea("releases"),
		ownerRoute("POST /api/v1/projects/{slug}/releases/{number}/approve", releaseOperations.ApproveRelease, "deployment.ApproveRelease").inArea("releases"),
		ownerRoute("POST /api/v1/projects/{slug}/releases/{number}/cancel", releaseOperations.CancelRelease, "deployment.CancelRelease").inArea("releases"),
		requireRoute("POST /api/v1/projects/{slug}/releases/{number}/preview", releaseOperations.ReleasePreview, access.CapDeploymentOperate, "project-slug").inArea("previews"),
		requireRoute("GET /api/v1/projects/{slug}/adrs", adrs.ListProjectADRs, access.CapProjectRead, "project-slug").inArea("projects"),
		requireRoute("POST /api/v1/projects/{slug}/adrs", adrs.CreateADR, access.CapChangeAttach, "project-slug").inArea("projects"),

		requireRoute("GET /api/v1/changes/{id}", changeDetail.GetChange, access.CapChangeRead, "change").inArea("changes"),
		requireRoute("PATCH /api/v1/changes/{id}", changes.PatchChange, access.CapChangeUpdateBrief, "change").inArea("changes"),
		requireRoute("GET /api/v1/changes/{id}/diff", repositoryViews.ChangeDiff, access.CapChangeRead, "change").inArea("changes"),
		requireRoute("GET /api/v1/changes/{id}/transcript", chatHistory.ChangeTranscript, access.CapEvidenceRead, "change").inArea("changes"),
		requireRoute("GET /api/v1/projects/{slug}/transcript", chatHistory.ProjectTranscript, access.CapEvidenceRead, "project-slug").inArea("projects"),
		requireRoute("GET /api/v1/changes/{id}/transcript/{chat}/pictures/{name}", chatHistory.ChangePicture, access.CapEvidenceRead, "change").inArea("changes"),
		requireRoute("GET /api/v1/projects/{slug}/transcript/{chat}/pictures/{name}", chatHistory.ProjectPicture, access.CapEvidenceRead, "project-slug").inArea("projects"),
		requireRoute("POST /api/v1/changes/{id}/runs", changes.StartChangeRun, access.CapChangeInvokeLane, "change").inArea("changes"),
		requireRoute("POST /api/v1/changes/{id}/sendback", changes.SendBack, access.CapChangeSendBack, "change").inArea("changes"),
		ownerRoute("POST /api/v1/routine-publications/preview", routinePublications.Preview, "routinePublications.Preview").inArea("record"),
		ownerRoute("POST /api/v1/routine-publications", routinePublications.Classify, "routinePublications.Classify").inArea("setup"),
		ownerRoute("GET /api/v1/routine-publications/{kind}/{id}/history", routinePublications.History, "routinePublications.History").inArea("setup"),
		ownerRoute("POST /api/v1/routine-publications/{id}/revoke", routinePublications.Revoke, "routinePublications.Revoke").inArea("setup"),
		ownerRoute("GET /api/v1/standing-approvals", standingApprovals.List, "standingApprovals.List").inArea("setup"),
		ownerRoute("POST /api/v1/standing-approvals/preview", standingApprovals.Preview, "standingApprovals.Preview").inArea("landing"),
		ownerRoute("POST /api/v1/standing-approvals", standingApprovals.Enable, "standingApprovals.Enable").inArea("setup"),
		ownerRoute("GET /api/v1/standing-approvals/{id}/history", standingApprovals.History, "standingApprovals.History").inArea("setup"),
		ownerRoute("POST /api/v1/standing-approvals/{id}/assess", standingApprovals.Assess, "standingApprovals.Assess").inArea("setup"),
		ownerRoute("POST /api/v1/standing-approvals/{id}/disable", standingApprovals.Disable, "standingApprovals.Disable").inArea("setup"),
		requireRoute("POST /api/v1/changes/{id}/attachments", attachments.UploadAttachment, access.CapChangeAttach, "change").inArea("changes"),
		requireRoute("GET /api/v1/changes/{id}/attachments", attachments.ListAttachments, access.CapChangeRead, "change").inArea("changes"),
		requireRoute("GET /api/v1/changes/{id}/attachments/{name}", attachments.GetAttachment, access.CapChangeRead, "change").inArea("changes"),
		requireRoute("DELETE /api/v1/changes/{id}/attachments/{name}", attachments.DeleteAttachment, access.CapChangeAttach, "change").inArea("changes"),
		requireRoute("POST /api/v1/changes/{id}/land", changeAdmission.LandChange, access.CapChangeRequestLand, "change").inArea("landing"),
		requireRoute("POST /api/v1/changes/{id}/recheck", changeAdmission.RecheckLanding, access.CapChangeRequestLand, "change").inArea("changes"),
		requireRoute("POST /api/v1/changes/{id}/preview", changePreviews.ChangePreview, access.CapChangePreview, "change").inArea("previews"),
		requireRoute("GET /api/v1/changes/{id}/preview", changePreviewState.ChangePreviewState, access.CapChangeRead, "change").inArea("previews"),
		requireRoute("GET /api/v1/changes/{id}/worker-chats", changeWorkerChats.List, access.CapChangeRead, "change").inArea("record"),
		requireRoute("GET /api/v1/changes/{id}/worker-chats/{chat}/refused", changeWorkerChats.RefusedBundle, access.CapChangeRead, "change").inArea("record"),
		requireRoute("POST /api/v1/changes/{id}/preview/restart", changePreviews.ChangePreviewRestart, access.CapChangePreview, "change").inArea("previews"),
		requireRoute("POST /api/v1/changes/{id}/abandon", changes.AbandonChange, access.CapChangeAbandon, "change").inArea("changes"),
		filteredRoute("GET /api/v1/runs", runs.ListRuns, "runs.ListRuns").inArea("changes"),
		requireRoute("GET /api/v1/runs/executing", runs.ExecutingSteps, access.CapRunRead, "instance").inArea("changes"),
		requireRoute("GET /api/v1/runs/{id}", runs.GetRun, access.CapRunRead, "run").inArea("changes"),
		requireRoute("GET /api/v1/runs/{id}/trace", traces.Trace, access.CapEvidenceRead, "run").inArea("changes"),
		requireRoute("GET /api/v1/runs/{id}/diff", repositoryViews.RunDiff, access.CapRunRead, "run").inArea("changes"),
		requireRoute("GET /api/v1/runs/{id}/steps/{step}/attempts/{attempt}/refused", refusedStepWork.Bundle, access.CapRunRead, "run").inArea("changes"),
		requireRoute("GET /api/v1/runs/{id}/findings", findings.RunFindings, access.CapEvidenceRead, "run").inArea("changes"),
		requireRoute("POST /api/v1/projects/{slug}/dispositions", conformance.CreateDisposition, access.CapExceptionApprove, "project-slug").inArea("record"),
		requireRoute("GET /api/v1/projects/{slug}/dispositions", conformance.ListDispositions, access.CapConformanceRead, "project-slug").inArea("record"),
		requireRoute("GET /api/v1/projects/{slug}/dispositions/openvex", conformance.OpenVEX, access.CapConformanceRead, "project-slug").inArea("record"),
		requireRoute("DELETE /api/v1/dispositions/{id}", conformance.RevokeDisposition, access.CapExceptionRevoke, "disposition").inArea("record"),
		requireRoute("GET /api/v1/projects/{slug}/findings", findings.ProjectFindings, access.CapConformanceRead, "project-slug").inArea("projects"),
		requireRoute("GET /api/v1/projects/{slug}/policies", projectPolicies.ListPolicies, access.CapProjectRead, "project-slug").inArea("ways"),
		requireRoute("POST /api/v1/projects/{slug}/policies", projectPolicies.CreatePolicy, access.CapProjectWayOfWorking, "project-slug").inArea("ways"),
		requireRoute("DELETE /api/v1/policies/{id}", projectPolicies.DeletePolicy, access.CapStandardsPolicies, "instance").inArea("ways"),
		filteredRoute("GET /api/v1/usage", usage.Usage, "usage.Usage").inArea("usage"),
		filteredRoute("GET /api/v1/timing", timing.Timing, "timing.Timing").inArea("setup"),
		requireRoute("GET /api/v1/runs/{id}/timing", timing.RunTiming, access.CapRunRead, "run").inArea("changes"),
		filteredRoute("GET /api/v1/policy-library", policyLibrary.PolicyLibrary, "policyLibrary.PolicyLibrary").inArea("ways"),
		requireRoute("POST /api/v1/policy-library", policyLibrary.SaveLibraryPolicy, access.CapStandardsPolicies, "instance").inArea("ways"),
		requireRoute("DELETE /api/v1/policy-library/{name}", policyLibrary.DeleteLibraryPolicy, access.CapStandardsPolicies, "instance").inArea("ways"),
		filteredRoute("GET /api/v1/policy-library/{name}/usage", policyLibrary.PolicyUsage, "policyLibrary.PolicyUsage").inArea("ways"),
		requireRoute("GET /api/v1/runs/{id}/workspace/{path...}", workspaces.Workspace, access.CapEvidenceRead, "run").inArea("changes"),
		requireRoute("GET /api/v1/audit", auditLog.AuditLog, access.CapAuditRead, "instance").inArea("record"),
		requireRoute("POST /api/v1/runs/{id}/advance", runs.Advance, access.CapRunOperate, "run").inArea("changes"),
		requireRoute("POST /api/v1/runs/{id}/artifacts", runs.AddArtifact, access.CapEvidenceAttach, "run").inArea("changes"),
		ownerRoute("GET /api/v1/artifacts/{id}", runs.GetArtifact, "runs.GetArtifact").inArea("setup"),

		requireRoute("POST /api/v1/steps/{id}/run", runs.StepRun, access.CapRunOperate, "step").inArea("changes"),
		requireRoute("POST /api/v1/steps/{id}/interrupt", stepInterruption.StepInterrupt, access.CapChangeInvokeLane, "step").inArea("changes"),
		requireRoute("GET /api/v1/steps/{id}/live", runProgress.StepLive, access.CapRunRead, "step").inArea("changes"),
		requireRoute("GET /api/v1/steps/{id}/approvers", runProgress.StepApprovers, access.CapRunRead, "step").inArea("changes"),
		requireRoute("POST /api/v1/steps/{id}/approve", runs.Approve, access.CapChangeApprove, "step").inArea("changes"),
		requireRoute("GET /api/v1/steps/{id}/advice", approvalBriefs.FailureAdvice, access.CapRunRead, "step").inArea("changes"),
		requireRoute("GET /api/v1/steps/{id}/brief", approvalBriefs.ApprovalBrief, access.CapRunRead, "step").inArea("changes"),
		requireRoute("GET /api/v1/steps/{id}/question", approvalBriefs.ApprovalQuestion, access.CapRunRead, "step").inArea("changes"),
		requireRoute("POST /api/v1/steps/{id}/override", runs.Override, access.CapChangeOverride, "step").inArea("changes"),

		publicRoute("GET /api/v1/catalog", catalogChanges.CatalogList, "catalogChanges.CatalogList: shared published change templates").inArea("changes"),
		publicRoute("GET /api/v1/catalog/{name...}", catalogChanges.CatalogGet, "catalogChanges.CatalogGet: shared change-template definition").inArea("changes"),
		ownerRoute("POST /api/v1/catalog-apply/{name...}", catalogChanges.CatalogApply, "catalogChanges.CatalogApply").inArea("changes"),
		publicRoute("GET /api/v1/guidance/blocks", guidanceBlocks.ListBlocks, "guidanceBlocks.ListBlocks: shared guidance library").inArea("ways"),
		ownerRoute("GET /api/v1/guidance/generated", guidance.GeneratedGuidance, "guidance.GeneratedGuidance").inArea("ways"), // a named project needs project:read
		publicRoute("GET /api/v1/me", a.handleMe, "a.handleMe: current verified caller self-view").inArea("setup"),
		protocolRoute("POST /api/v1/auth/external/start", credentialDoor(a, a.handleExternalStart), "a.handleExternalStart: browser-bound external authentication").inArea("people"),
		protocolRoute("GET /api/v1/auth/external/callback", credentialDoor(a, a.handleExternalCallback), "a.handleExternalCallback: one-use external authentication evidence").inArea("people"),
		requireRoute("GET /api/v1/auth/external/provider", external.Provider, access.CapIdentityUsers, "instance").inArea("people"),
		ownerRoute("PUT /api/v1/auth/external/provider", external.Save, "external.Save").inArea("people"),
		ownerRoute("POST /api/v1/auth/external/enabled", external.Enabled, "external.Enabled").inArea("people"),
		ownerRoute("POST /api/v1/auth/external/test", external.Test, "external.Test").inArea("people"),
		ownerRoute("GET /api/v1/auth/external/bindings", external.Bindings, "external.Bindings").inArea("people"),
		ownerRoute("POST /api/v1/auth/external/bindings/{id}/unlink", credentialDoor(a, a.handleExternalUnlink), "a.handleExternalUnlink").inArea("people"),
		ownerRoute("POST /api/v1/auth/external/bindings", external.Bind, "external.Bind").inArea("people"),
		ownerRoute("DELETE /api/v1/auth/external/bindings/{id}", external.Revoke, "external.Revoke").inArea("people"),
		publicRoute("GET /api/v1/auth/me", a.handleAuthMe, "a.handleAuthMe: current authentication self-view").inArea("people"),
		protocolRoute("POST /api/v1/auth/login", credentialDoor(a, a.handleAuthLogin), "a.handleAuthLogin: credential verification").inArea("people"),
		protocolRoute("POST /api/v1/auth/logout", a.handleAuthLogout, "a.handleAuthLogout: credential verification").inArea("people"),
		protocolRoute("POST /api/v1/auth/bootstrap", credentialDoor(a, a.handleAuthBootstrap), "a.handleAuthBootstrap: credential verification").inArea("people"),
		requireRoute("GET /api/v1/users", accounts.ListUsers, access.CapInstitutionRead, "instance").inArea("people"),
		requireRoute("POST /api/v1/users", accounts.SaveUser, access.CapIdentityUsers, "instance").inArea("people"),
		requireRoute("DELETE /api/v1/users/{name}", accounts.DeleteUser, access.CapIdentityUsers, "instance").inArea("people"),
		requireRoute("POST /api/v1/invites", invites.CreateInvite, access.CapIdentityUsers, "instance").inArea("people"),
		requireRoute("POST /api/v1/password-links", invites.CreatePasswordLink, access.CapIdentityUsers, "instance").inArea("people"),
		requireRoute("GET /api/v1/invites", invites.ListInvites, access.CapIdentityUsers, "instance").inArea("people"),
		requireRoute("DELETE /api/v1/invites/{id}", invites.RevokeInvite, access.CapIdentityUsers, "instance").inArea("people"),
		protocolRoute("POST /api/v1/auth/invite", credentialDoor(a, invites.ShowInvite), "invites.ShowInvite: the invite token in the body is the credential").inArea("people"),
		protocolRoute("POST /api/v1/auth/invite/accept", credentialDoor(a, invites.AcceptInvite), "invites.AcceptInvite: the invite token in the body is the credential").inArea("people"),
		requireRoute("GET /api/v1/groups", accounts.ListGroups, access.CapInstitutionRead, "instance").inArea("people"),
		requireRoute("POST /api/v1/groups", accounts.SaveGroup, access.CapIdentityGroups, "instance").inArea("people"),
		requireRoute("POST /api/v1/groups/{name}/members/{user}", accounts.AddGroupMember, access.CapIdentityGroups, "instance").inArea("people"),
		requireRoute("DELETE /api/v1/groups/{name}/members/{user}", accounts.RemoveGroupMember, access.CapIdentityGroups, "instance").inArea("people"),
		requireRoute("DELETE /api/v1/groups/{name}", accounts.DeleteGroup, access.CapIdentityGroups, "instance").inArea("people"),
		requireRoute("GET /api/v1/auth/admin-networks", administratorNetworks.AdminNetworks, access.CapInstanceReadPublic, "instance").inArea("people"),
		requireRoute("PUT /api/v1/auth/admin-networks", administratorNetworks.AdminNetworks, access.CapIdentityAuth, "instance").inArea("people"), // decides inside
		filteredRoute("GET /api/v1/auth/delegations", accounts.ListDelegations, "accounts.ListDelegations").inArea("people"),
		ownerRoute("DELETE /api/v1/auth/delegations/{id}", accounts.RevokeDelegation, "accounts.RevokeDelegation").inArea("people"), // decides inside
		publicRoute("GET /api/v1/authz/roles", authorization.AuthzRoles, "authorization.AuthzRoles: compiled stock role and capability catalogue, no grants").inArea("people"),
		requireRoute("GET /api/v1/authz/grants", authorization.AuthzGrants, access.CapInstitutionRead, "instance").inArea("people"),
		ownerRoute("POST /api/v1/authz/grants", authorization.AuthzGrant, "authorization.AuthzGrant").inArea("people"), // decides inside (instance or project scope)
		ownerRoute("DELETE /api/v1/authz/grants/{id}", authorization.AuthzRevoke, "authorization.AuthzRevoke").inArea("people"),
		ownerRoute("GET /api/v1/authz/explain", authorization.AuthzExplain, "authorization.AuthzExplain").inArea("people"), // decides inside: self on a readable project, else institution:read-structure
		publicRoute("GET /api/v1/authz/me", authorization.AuthzMe, "authorization.AuthzMe: current caller capability self-view").inArea("people"),
		ownerRoute("POST /api/v1/account/tokens", accounts.CreateOwnToken, "accounts.CreateOwnToken").inArea("people"),
		ownerRoute("DELETE /api/v1/account/tokens/{name}", accounts.RevokeOwnToken, "accounts.RevokeOwnToken").inArea("people"),
		ownerRoute("POST /api/v1/tokens", accounts.CreateToken, "accounts.CreateToken").inArea("people"),
		ownerRoute("GET /api/v1/tokens", accounts.ListTokens, "accounts.ListTokens").inArea("people"),
		ownerRoute("DELETE /api/v1/tokens/{name}", accounts.RevokeToken, "accounts.RevokeToken").inArea("people"),
		publicRoute("GET /api/v1/integrations/docs/{capability}/{provider}", integrationDocuments.IntegrationDocs, "integrationDocuments.IntegrationDocs: shared connector usage documentation").inArea("setup"),
		publicRoute("GET /api/v1/docs", docs.ListDocs, "docs.ListDocs: shared AppTown documentation").inArea("setup"),
		publicRoute("GET /api/v1/docs/guides/{slug}", docs.GuideDoc, "docs.GuideDoc: shared AppTown documentation").inArea("setup"),
		requireRoute("GET /api/v1/projects/{slug}/integrations", projectAttachments.ListProjectIntegrations, access.CapProjectRead, "project-slug").inArea("projects"),
		requireRoute("POST /api/v1/projects/{slug}/integrations", projectAttachments.AttachIntegration, access.CapProjectIntegrations, "project-slug").inArea("projects"),
		requireRoute("DELETE /api/v1/projects/{slug}/integrations/{capability}", projectAttachments.DetachIntegration, access.CapProjectIntegrations, "project-slug").inArea("projects"),
		requireRoute("POST /api/v1/projects/{slug}/integrations/{capability}/wire", projectAttachments.WireIntegration, access.CapProjectIntegrations, "project-slug").inArea("projects"),
		requireRoute("POST /api/v1/projects/{slug}/integrations/{capability}/retire", projectAttachments.RetireIntegration, access.CapProjectIntegrations, "project-slug").inArea("projects"),
		publicRoute("GET /api/v1/attachables", projectAttachments.ListAttachables, "projectAttachments.ListAttachables: shared integration capability catalogue").inArea("setup"),
		requireRoute("POST /api/v1/changes/{id}/distill", changeNarratives.DistillChange, access.CapChangeNarrative, "change").inArea("changes"),
		requireRoute("POST /api/v1/projects/{slug}/import", changeNarratives.ImportBranch, access.CapProjectImport, "project-slug").inArea("projects"),
		publicRoute("GET /api/v1/dependencies", dependencies.ListDependencies, "dependencies.ListDependencies: shared dependency catalogue").inArea("setup"),
		requireRoute("POST /api/v1/dependencies", dependencies.SaveDependency, access.CapIntegrationDeps, "instance").inArea("setup"),
		publicRoute("GET /api/v1/services", serviceKinds.ListServiceKinds, "serviceKinds.ListServiceKinds: shared service-kind catalogue").inArea("setup"),
		requireRoute("POST /api/v1/services", serviceKinds.SaveServiceKind, access.CapIntegrationServices, "instance").inArea("setup"),
		requireRoute("DELETE /api/v1/services/{capability}", serviceKinds.DeleteServiceKind, access.CapIntegrationServices, "instance").inArea("setup"),
		requireRoute("POST /api/v1/guidance/blocks", guidanceBlocks.CreateBlock, access.CapStandardsGuidance, "instance").inArea("ways"),
		publicRoute("GET /api/v1/guidance/blocks/{block}", guidanceBlocks.GetBlock, "guidanceBlocks.GetBlock: shared guidance definition, read by the block's name").inArea("ways"),
		requireRoute("PATCH /api/v1/guidance/blocks/{block}", guidanceBlocks.PatchBlock, access.CapStandardsGuidance, "instance").inArea("ways"),
		requireRoute("GET /api/v1/projects/{slug}/services", projectServices.ListProjectServices, access.CapProjectRead, "project-slug").inArea("projects"),
		requireRoute("POST /api/v1/projects/{slug}/services", projectServices.SetProjectServices, access.CapProjectServices, "project-slug").inArea("projects"),
		requireRoute("GET /api/v1/projects/{slug}/guidance", guidance.GetProjectGuidance, access.CapProjectRead, "project-slug").inArea("ways"),
		// Secrets: declarations and bindings, never values.
		requireRoute("GET /api/v1/projects/{slug}/secrets", secrets.ProjectSecrets, access.CapSecretRead, "project-slug").inArea("projects"),
		requireRoute("PUT /api/v1/projects/{slug}/secrets/{deployment}", secrets.SetSecretProfile, access.CapSecretBind, "project-slug").inArea("projects"),
		requireRoute("PUT /api/v1/projects/{slug}/secrets/{deployment}/{name}/agent-visibility", secrets.SetSecretAgentVisibility, access.CapSecretBind, "project-slug").inArea("projects"),
		requireRoute("POST /api/v1/projects/{slug}/secrets/{deployment}/{name}/separate-preview", secrets.SeparatePreviewSecret, access.CapSecretBind, "project-slug").inArea("projects"),
		requireRoute("PUT /api/v1/projects/{slug}/secrets/{deployment}/{name}", secrets.BindSecret, access.CapSecretBind, "project-slug").inArea("projects"),
		requireRoute("DELETE /api/v1/projects/{slug}/secrets/{deployment}/{name}", secrets.UnbindSecret, access.CapSecretBind, "project-slug").inArea("projects"),
		adminRoute("GET /api/v1/projects/{slug}/secrets/{deployment}/{name}/reveal", secrets.RevealSecret).inArea("projects"),
		requireRoute("PUT /api/v1/projects/{slug}/guidance", guidance.PutProjectGuidance, access.CapProjectGuidance, "project-slug").inArea("ways"),

		requireRoute("GET /api/v1/integrations", integrationListing.ListIntegrations, access.CapProjectRead, "instance").inArea("setup"),
		publicRoute("GET /api/v1/integrations/catalog", connectors.ConnectorCatalog, "connectors.ConnectorCatalog: shared connector catalogue").inArea("changes"),
		requireRoute("GET /api/v1/secrets/names", secrets.SecretNames, access.CapProjectRead, "instance").inArea("setup"),
		adminRoute("PUT /api/v1/secrets/{name}", secrets.SetInstanceSecret).inArea("setup"),
		adminRoute("DELETE /api/v1/secrets/{name}", secrets.RemoveInstanceSecret).inArea("setup"),
		requireRoute("POST /api/v1/integrations", connectors.SaveIntegration, access.CapIntegrationConnector, "instance").inArea("setup"),
		requireRoute("POST /api/v1/integrations/test", connectors.TestIntegration, access.CapIntegrationConnector, "instance").inArea("setup"),
		requireRoute("POST /api/v1/integrations/{name}/test", connectors.TestSavedIntegration, access.CapIntegrationConnector, "instance").inArea("setup"),
		requireRoute("DELETE /api/v1/integrations/{name}", connectors.DeleteIntegration, access.CapIntegrationConnector, "instance").inArea("setup"),
		publicRoute("GET /api/v1/scm/namespaces", scmRemotes.SCMNamespaces, "scmRemotes.SCMNamespaces: shared SCM namespace discovery").inArea("setup"),

		// A chat is a model with write access to a Change branch: decided before the session is held.
		chatRoute("POST /api/v1/projects/{slug}/console", consoleCreation.ConsoleCreate, access.CapChangeInvokeLane, "project-slug").inArea("chats"),
		// Anyone who can read the project may look at its chats (project:read,
		// however it is held); acting on one (say, paste, stop, end, type) is
		// working on changes, which conversation.Chats decides and keeps a
		// receipt of, whichever door asks, and refuses a viewer in the chat's
		// own words.
		requireRoute("GET /api/v1/projects/{slug}/console", consoleSessions.ConsoleList, access.CapProjectRead, "project-slug").inArea("chats"),
		requireRoute("GET /api/v1/console/{sid}/pty", a.handleConsolePTY, access.CapProjectRead, "console-session").inArea("chats"),
		chatRoute("POST /api/v1/console/{sid}/invoke", consoleInvocation.ConsoleInvoke, access.CapChangeInvokeLane, "console-session").inArea("chats"),
		chatRoute("POST /api/v1/console/{sid}/preview", consolePreview.ConsolePreview, access.CapChangePreview, "console-session").inArea("chats"),
		chatActRoute("DELETE /api/v1/console/{sid}", consoleEnd.ConsoleEnd, "console-session", "conversation.Chats.Act: change:invoke-lane on the chat's project, by a person or this chat only, refused in the chat's own words").inArea("chats"),
		requireRoute("GET /api/v1/console/{sid}/changes", consoleChanges.ConsoleChanges, access.CapProjectRead, "console-session").inArea("chats"),
		requireRoute("GET /api/v1/console/{sid}/messages", consoleMessages.ConsoleMessages, access.CapProjectRead, "console-session").inArea("chats"),
		chatActRoute("POST /api/v1/console/{sid}/say", consoleMessages.ConsoleSay, "console-session", "conversation.Chats.Act: change:invoke-lane on the chat's project, by a person or this chat only, refused in the chat's own words").inArea("chats"),
		chatActRoute("POST /api/v1/console/{sid}/interrupt", consoleMessages.ConsoleInterrupt, "console-session", "conversation.Chats.Act: change:invoke-lane on the chat's project, by a person or this chat only, refused in the chat's own words").inArea("chats"),
		chatRoute("POST /api/v1/console/{sid}/land", consoleChanges.ConsoleLand, access.CapChangeRequestLand, "console-session").inArea("chats"),
		chatRoute("POST /api/v1/console/{sid}/recheck", consoleChanges.ConsoleRecheck, access.CapChangeRequestLand, "console-session").inArea("chats"),
		chatActRoute("POST /api/v1/console/{sid}/upload", consoleMessages.ConsoleUpload, "console-session", "conversation.Chats.Act: change:invoke-lane on the chat's project, by a person or this chat only, refused in the chat's own words").inArea("chats"),
		requireRoute("GET /api/v1/console/{sid}/uploads/{name}", consoleMessages.ConsoleUploadGet, access.CapProjectRead, "console-session").inArea("chats"),

		filteredRoute("GET /api/v1/adrs", adrs.ADRLibrary, "adrs.ADRLibrary").inArea("record"),
		ownerRoute("GET /api/v1/adrs/{id}", adrs.GetADR, "adrs.GetADR").inArea("record"),
		requireRoute("PATCH /api/v1/adrs/{id}", adrs.PatchADR, access.CapChangeAttach, "adr").inArea("record"),
		requireRoute("POST /api/v1/adrs/{id}/supersede", adrs.SupersedeADR, access.CapChangeAttach, "adr").inArea("record"),

		// These protocols authenticate inside their handlers. AuthMiddleware's
		// path rules and browser-security/VHost wrapping still apply unchanged.
		protocolRoute("/git/{slug}/{gitpath...}", a.handleGitHTTP, "a.handleGitHTTP: credential verification").inArea("setup"),
		protocolRoute(mcpPath, a.serveMCP, "a.serveMCP: credential verification").inArea("mcp"),
		publicRoute("GET /.well-known/oauth-protected-resource/mcp/user", a.handleOAuthProtectedResource, "a.handleOAuthProtectedResource: OAuth public resource discovery").inArea("mcp"),
		publicRoute("GET /.well-known/oauth-authorization-server", a.handleOAuthServerMetadata, "a.handleOAuthServerMetadata: OAuth public authorization-server discovery").inArea("mcp"),
		publicRoute("GET /oauth/login.js", handleOAuthLoginScript, "handleOAuthLoginScript: fixed login page script").inArea("mcp"),
		publicRoute("POST /oauth/register", a.handleOAuthRegister, "a.handleOAuthRegister: OAuth dynamic public-client registration").inArea("mcp"),
		protocolRoute("GET /oauth/authorize", a.handleOAuthAuthorize, "a.handleOAuthAuthorize: credential verification").inArea("mcp"),
		protocolRoute("POST /oauth/authorize", a.handleOAuthAuthorize, "a.handleOAuthAuthorize: credential verification").inArea("mcp"),
		protocolRoute("POST /oauth/token", credentialDoor(a, a.handleOAuthToken), "a.handleOAuthToken: credential verification").inArea("mcp"),
		protocolRoute("POST /oauth/revoke", a.handleOAuthRevoke, "a.handleOAuthRevoke: credential verification").inArea("mcp"),
		protocolRoute("POST "+gate.RedeemPath, dataProtection.RedeemGateTicket, "dataProtection.RedeemGateTicket: credential verification (a restored copy's gate secret)").inArea("setup"),
		publicRoute("/api/", missingAPI, "missingAPI: unknown API path response").inArea("setup"),
		publicRoute("/", webHandler(), "webHandler(): SPA assets and fallback; API authentication is independent").inArea("setup"),
	}
}

func docsForRoutes(origin string) httpapi.Docs {
	var docs []httpapi.DocIntegration
	for _, c := range integration.ConnectorCatalog() {
		docs = append(docs, httpapi.DocIntegration{Capability: c.Capability, Provider: c.Provider, Title: c.Title})
	}
	return httpapi.NewDocs(docs, origin)
}
