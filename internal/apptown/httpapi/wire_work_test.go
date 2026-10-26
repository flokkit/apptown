package httpapi

import (
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/wiregen"
)

// The Work routes' responses: Changes, runs, chats and the activity pages.
func init() {
	registerWireMapped(workflow.ScheduleOutcome(""), `"opened" | "skipped" | "failed" | "unknown"`)
	registerWire(
		wiregen.Named{Name: "StandingCategories", Value: workflow.StandingCategories{}},
		wiregen.Named{Name: "StandingEnvironment", Value: workflow.StandingEnvironment{}},
		wiregen.Named{Name: "StandingProject", Value: workflow.StandingProject{}},
		wiregen.Named{Name: "StandingDefinition", Value: workflow.StandingDefinition{}},
		wiregen.Named{Name: "StandingPurpose", Value: workflow.StandingPurpose{}},
		wiregen.Named{Name: "StandingProcedure", Value: workflow.StandingProcedure{}},
		wiregen.Named{Name: "StandingProjectMeaning", Value: workflow.StandingProjectMeaning{}},
		wiregen.Named{Name: "StandingQuestion", Value: workflow.StandingQuestion{}},
		wiregen.Named{Name: "StandingCommand", Value: workflow.StandingCommand{}},
		wiregen.Named{Name: "StandingRevision", Value: workflow.StandingRevision{}},
		wiregen.Named{Name: "StandingAssessment", Value: workflow.StandingAssessment{}},
		wiregen.Named{Name: "StandingInspection", Value: workflow.StandingInspection{}},
		wiregen.Named{Name: "StandingValidation", Value: workflow.StandingValidation{}},
		wiregen.Named{Name: "StandingStatus", Value: workflow.StandingStatus{}},
		wiregen.Named{Name: "StandingActor", Value: access.Actor{}},
		wiregen.Named{Name: "StandingDisable", Value: StandingDisable{}},
		wiregen.Named{Name: "ChangeRun", Value: ChangeRun{}},
		wiregen.Named{Name: "ScheduleList", Value: ScheduleList{}},
		wiregen.Named{Name: "ChangeEnvelope", Value: ChangeEnvelope{}},
		wiregen.Named{Name: "SendBackAccepted", Value: SendBackAccepted{}},
		wiregen.Named{Name: "ChangeBoard", Value: ChangeBoard{}},
		wiregen.Named{Name: "CreatedChange", Value: CreatedChange{}},
		wiregen.Named{Name: "InvokedChange", Value: InvokedChange{}},
		wiregen.Named{Name: "NarrativeRefreshed", Value: NarrativeRefreshed{}},
		wiregen.Named{Name: "AttachmentAdded", Value: AttachmentAdded{}},
		wiregen.Named{Name: "ChangeAttachment", Value: ChangeAttachment{}},
		wiregen.Named{Name: "ChangeCockpit", Value: ChangeCockpit{}},
		wiregen.Named{Name: "ApprovalQuestion", Value: workflow.ApprovalQuestion{}},
		wiregen.Named{Name: "AdvanceResult", Value: workflow.AdvanceResult{}},
		wiregen.Named{Name: "ConsoleSessionView", Value: ConsoleSessionView{}},
		wiregen.Named{Name: "ConsoleSessionList", Value: ConsoleSessionList{}},
		wiregen.Named{Name: "ConsoleOpened", Value: ConsoleOpened{}},
		wiregen.Named{Name: "ConsoleMessagePage", Value: ConsoleMessagePage{}},
		wiregen.Named{Name: "PastedPicture", Value: PastedPicture{}},
		wiregen.Named{Name: "ChatHistoryView", Value: ChatHistoryView{}},
		wiregen.Named{Name: "ConsoleChangeList", Value: ConsoleChangeList{}},
		wiregen.Named{Name: "LandingStarted", Value: LandingStarted{}},
		wiregen.Named{Name: "RecheckStarted", Value: RecheckStarted{}},
		wiregen.Named{Name: "ConsolePreviewAddress", Value: ConsolePreviewAddress{}},
		wiregen.Named{Name: "DashboardView", Value: DashboardView{}},
		wiregen.Named{Name: "JobList", Value: JobList{}},
		wiregen.Named{Name: "RestartedLanding", Value: RestartedLanding{}},
		wiregen.Named{Name: "StepProgress", Value: StepProgress{}},
		wiregen.Named{Name: "RunsAbandoned", Value: RunsAbandoned{}},
		wiregen.Named{Name: "ArtifactAttached", Value: ArtifactAttached{}},
		wiregen.Named{Name: "StepInterrupted", Value: StepInterrupted{}},
		wiregen.Named{Name: "InstanceActivity", Value: InstanceActivity{}},
		wiregen.Named{Name: "TimingReport", Value: TimingReport{}},
		wiregen.Named{Name: "RunTimeline", Value: RunTimeline{}},
		wiregen.Named{Name: "RunTrace", Value: RunTrace{}},
		wiregen.Named{Name: "ChangePreview", Value: changePreviewResponse{}},
		// Unexported Go types get the names a reader expects.
		wiregen.Named{Name: "WorkflowTiming", Value: workflowTiming{}},
		wiregen.Named{Name: "StepTiming", Value: stepTiming{}},
		wiregen.Named{Name: "TimelineEntry", Value: timelineEntry{}},
		wiregen.Named{Name: "RunRow", Value: runRowView{}},
		wiregen.Named{Name: "CockpitEvidence", Value: changeEvidence{}},
		wiregen.Named{Name: "ChangeRelease", Value: changeRelease{}},
		// Two packages call their summary ChangeSummary; the ADR one is named here.
		wiregen.Named{Name: "ADRChangeSummary", Value: adrs.ChangeSummary{}},
		// "Record" would shadow TypeScript's own Record<K, V>.
		wiregen.Named{Name: "ADRRecord", Value: adrs.Record{}},
		wiregen.Named{Name: "ADRState", Value: adrs.State(0)},
		// A workflow definition's parts share names with a run's records.
		wiregen.Named{Name: "SpecStage", Value: schema.Stage{}},
		wiregen.Named{Name: "SpecStep", Value: schema.Step{}},
		wiregen.Named{Name: "SpecGate", Value: schema.Gate{}},
	)
}
