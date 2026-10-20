# Finding your way through Forge

Start with `app.go` to see construction and `route_table.go` to find an HTTP
operation. Follow its concrete handler in `httpapi`, or its MCP tool in `mcpapi`,
to the command owner. Avoid adding another App forwarding method to connect them.

| Operation | Calls to follow | Transaction / effect boundary |
| --- | --- | --- |
| Begin work | `workflow.Service.StartRun` → `workcommands.Preparation.Resolve` → Workflow admission → dispatch | Preparation pins adoption/definition/acceptance; admission commits before dispatch |
| Run a step | `workflow.Execution` → bound `workflowexec` runner → external owner → `Execution.Finish` | Claim and settlement remain in Workflow; process/container work stays outside SQL transactions |
| Complete or land | Workflow settlement → `workflowCompletionEffects` → River → `workcommands.FollowUps.Complete` | Release/intent writes join settlement; landing, adoption, cleanup and prose run after commit |
| Remediate | Remediation observation → `workcommands.RemediationStarts.Resolve` → shared preparation → attempt admission | Exact observation and frozen adoption are rechecked; Remediation owns item/attempt transitions |
| Open a console | `conversation.Admission` → Console → Conversation environment → Agentenv / Pi | Session identity outlives a stopped agent process; preparation does not receive private model credentials |
| Save or end a change's chat | `changeconsole.Service` (`SaveAfterTurn`, `End`) → `repositoryedit.SaveChat` → `scm.Branches` publication; landings, checks and previews save first through `changeconsole.Saves` | One chat's saves run one at a time; a worker chat ends only after its last save settles, and a chat holds its change until then |
| Push a step's work for `apptown check` | `handleGitHTTP` → `inference.Identity.AuthenticateStep` → `workflow.Service.StepPush` (the one scratch ref the attempt may write) → `git http-backend` with that ref advertised → hub pre-receive hook | Workflow decides the ref from the run and its change; the handler keeps only the Git protocol, and the hook refuses any other ref again. A scratch commit is feedback, never evidence |
| Delete a project | `httpapi.ProjectDeletion.DeleteProject` → `projectlifecycle.Deletion.Request` (console block, `project.MarkDeletingIn`, `identity.GrantBindingIn`, the pass queued through the root `cleanupQueue`) → River `project-deletion-pass` → `Deletion.Pass` → `dataprotection.Service.TakeDownForDeletion` (main last) → `project.MarkDeletedIn` | The project is kept, read-only, until `kept_until`; `project.RefuseUnlessActiveIn` refuses new work at every owner door meanwhile |
| Purge a deleted project | River `project-purge-pass` → `Deletion.PurgePass`, or `POST …/purge` → `Deletion.PurgeNow` → `operations.AdmitIn` (`project.purge`) → `Deletion.runPurge` (backup sets, hub, GitLab archive, records with the `CleanupPlan`) → River → `projectlifecycle.Cleanup.Run` | Runs under the deletion's binding (or the administrator's authority), asking again before each step; the cleanup tries `CleanupAttempts` times, then waits for a person |
| Drain into the no-local-Docker mode | `RecoverInterruptedWork` (under `-drain-local-docker`) → `drainLocalDocker` → `maintenance.LocalWorkLeft` → `maintenance.ExecutionContainers` → `identity.RecordLocalDockerDrainedIn` | The root keeps the mode record and startup; maintenance answers what local work the records and the forge's Docker still hold. Nothing is stopped or removed, and the drain is recorded only when both are clear |
| Start the forge | `cmd/apptownd` → `RecoverInterruptedWork` (`workflow.InterruptStepsIn` ends `workflowexec.NotContinued` kinds; `jobs.RepairInterruptedIn`, then `operations.Service.RepairInterruptedIn` settles running operations unknown and requeues queued ones) → `ReapPreviewOrphans` (`docker.ReapPreviewOrphans`, `preview.Service.RemoveWorkerPreviews`) → `VerifyAdoptions` (`adoption.VerifyRevision` per project) → `SyncSeedIntegrations` (`integration.Seeds` through `wow.Sync`, then `integration.SeedModelBinding`) → `SeedTargets` (`deployment.SeedTargets`) | The root sequences boot and logs; each rule is its owner's. Seeds are idempotent, and a drifted project keeps its receipt |
| Refresh a change's narrative | `narratives.Request` → the `narrative` job (`narratives.RunJob`, as the engine) → `narrativeBuilder.Refresh` → `workflow.Service.NarrativeSource` → evidence (`narrativeDiff`, `changeconsole.ChatReports`) → model call with `workflow.NarrativePrompt` and `workflow.NarrativeEvidence` → `workflow.ParseNarrative` → `workflow.Service.PublishNarrative` | The model call and its usage record are root assembly; usage is recorded even when publication fails |
| See or reach a change's preview | `httpapi.ChangePreviewState` → `changePreviewViews.State` → `preview.Service.ChangeStatus` (a boot in progress, else where it is served and whether it runs); a visitor: `VHost` → `preview.Service.PreviewServedOnWorker` → worker forward, else this forge's Docker | Preview answers where a preview is served and whether it runs; the root passes the change's state and keeps the HTTP protocol |
| Fire a schedule | `RunScheduler` (every 30 s) → `changeactions.Schedules.FireDueSchedules` → per project: adopted workflow version's cron → `workflow.ArmScheduleIn` / `RunningWorkflowCountIn` → `changeactions.Service.CreateAndStart` → `workflow.RecordScheduleFiringIn` | A new schedule is armed, never replayed; a firing is recorded even when its run cannot start, so it retries at its next due time |
| Read a connector's health | `httpapi` integration listing or Test connection → `connectorHealth.Health` / `Test` (the dispatch) → the capability's owner (`deployment.Service.AttachedDockerHostsHealth`, `verification.TestScannerImage`, `integration.Service.CheckModelCredentials` / `TestModelBinding`, the forge's own Docker and GitLab) | The root stamps each reading with when it ran; a kind with no check reads as not checked, never working |
| Start and follow a long operation (a data copy, a take-down) | owner door (`httpapi.DataProtection.Copy` → `dataprotection.Service.RequestCopy` → `dataprotection.Service.admit` → `operations.Service.AdmitIn`) answers 202; River's `operations` queue → `operations.Service.JobHandler` → `Run` (claim, the kind's `Execute`, settle); reads `httpapi.Operations.Get` → `operations.Service.Get`, `apptown wait operation`, MCP `get_operation` | The owner decides eligibility and holds; the ledger owns the row, the job, claiming, settlement and the read and cancel checks |
| Infer | dedicated listener → `inference.Gateway` → Identity / Catalog → Usage admission → upstream → final receipt | Current identity is checked before admission and during streaming; credentials stay server-side |
| The forge's own model call | narrative, brief, simple change, pack assistance, check or eval judge → `modelcall.Call` → `inference.Gateway.Ask` → Catalog / eval choice → `usage.Requests.OpenCall` (receipt and pending spend line) → upstream → `SettleCall` | One path for every provider; callers never record spend. Unsettled lines count as unknown once their time runs out |
| Shut down | daemon → `ShutdownBackground` → Workflow, Remediation and narrative `BeginStop` → bounded waits → telemetry close | Producers drain under one shared budget; telemetry gets the remainder. Timeout is not proof that an operation has finished |

See [Workflow](workflow/README.md), [execution](workflowexec/README.md),
[work commands](workcommands/README.md) and [inference](inference/README.md).

For package-owned contracts, read [model selection and capacity](model/doc.go),
[usage records](usage/doc.go), [complaints](complaint/doc.go),
[secrets](secret/doc.go) and [integrations](integration/doc.go), or run
`go doc ./internal/forge/<package>` from the repository root. Method comments
state caller obligations and failure behaviour. Their former standalone subsystem
guides have been removed to keep one current explanation.

The root is a composition and protocol owner, not a directory to empty. It
retains application composition, small transaction adapters, known exclusions
and the remaining test-only Workflow stage-construction dependency. Pure owner
behavior is tested in the owner; root tests cover actual assembly and cross-owner
guarantees with independent persistence assertions where representation matters.
