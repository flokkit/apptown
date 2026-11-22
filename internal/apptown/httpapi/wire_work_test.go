package httpapi

import (
	"reflect"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/wiregen"
)

// The statistics' types are Workflow's own; they are named here from the
// method's result, so Workflow need not export them.
var janitorStatistics = reflect.Zero(reflect.TypeOf((*workflow.Service).JanitorStatistics).Out(0))

func janitorStatisticsPart(field string) reflect.Value {
	return reflect.Zero(janitorStatistics.FieldByName(field).Type().Elem())
}

// The Work routes' responses: Changes, runs, chats and the activity pages.
func init() {
	registerWireMapped(workflow.ScheduleOutcome(""), `"opened" | "skipped" | "failed" | "unknown"`)
	registerWire(
		wiregen.Named{Name: "AddsLine", Value: reflect.Zero(reflect.TypeOf(workflow.ChangeAdds{}.Lines).Elem()).Interface()},
		wiregen.Named{Name: "StandingVote", Value: reflect.Zero(reflect.TypeOf(workflow.ApprovalVote{}.Standing).Elem()).Interface()},
		wiregen.Named{Name: "JanitorAttribution", Value: reflect.Zero(reflect.TypeOf(workflow.Change{}.Janitor).Elem()).Interface()},
		wiregen.Named{Name: "JanitorChoice", Value: reflect.Zero(reflect.TypeOf(workflow.JanitorProject{}.Choice).Elem()).Interface()},
		wiregen.Named{Name: "JanitorLandedChange", Value: reflect.Zero(reflect.TypeOf(workflow.JanitorHistory{}.Changes).Elem()).Interface()},
		wiregen.Named{Name: "JanitorRunView", Value: JanitorRunView{}},
		wiregen.Named{Name: "JanitorRun", Value: changeactions.JanitorRun{}},
		wiregen.Named{Name: "JanitorStart", Value: changeactions.JanitorStart{}},
		wiregen.Named{Name: "JanitorView", Value: JanitorView{}},
		wiregen.Named{Name: "JanitorHistoryView", Value: JanitorHistoryView{}},
		wiregen.Named{Name: "JanitorStatistics", Value: janitorStatistics.Interface()},
		wiregen.Named{Name: "JanitorKindStatistics", Value: janitorStatisticsPart("Kinds").Interface()},
		wiregen.Named{Name: "JanitorCounts", Value: reflect.Zero(janitorStatisticsPart("Kinds").FieldByName("Counts").Type()).Interface()},
		wiregen.Named{Name: "JanitorReason", Value: janitorStatisticsPart("Reasons").Interface()},
		wiregen.Named{Name: "JanitorReasonExample", Value: reflect.Zero(janitorStatisticsPart("Reasons").FieldByName("Examples").Type().Elem()).Interface()},
		wiregen.Named{Name: "JanitorPatchTimes", Value: janitorStatistics.FieldByName("Patch").Interface()},
		wiregen.Named{Name: "JanitorProjectStatistics", Value: janitorStatisticsPart("Projects").Interface()},
		wiregen.Named{Name: "StandingProjectState", Value: projectStandingState{}},
		wiregen.Named{Name: "StandingProjectAct", Value: projectStandingChoice{}},
		wiregen.Named{Name: "RoutineAdoptionTarget", Value: adoption.RoutineTarget{}},
		wiregen.Named{Name: "StandingPurpose", Value: workflow.StandingPurpose{}},
		wiregen.Named{Name: "StandingProcedure", Value: workflow.StandingProcedure{}},
		wiregen.Named{Name: "JanitorChoices", Value: workflow.JanitorChoices{}},
		wiregen.Named{Name: "JanitorChoicesCommand", Value: workflow.JanitorChoicesCommand{}},
		wiregen.Named{Name: "StandingRevision", Value: workflow.StandingRevision{}},
		wiregen.Named{Name: "StandingAssessment", Value: workflow.StandingAssessment{}},
		wiregen.Named{Name: "StandingInspection", Value: workflow.StandingInspection{}},
		wiregen.Named{Name: "StandingValidation", Value: workflow.StandingValidation{}},
		wiregen.Named{Name: "StandingActor", Value: access.Actor{}},
		wiregen.Named{Name: "ChangeRun", Value: ChangeRun{}},
		wiregen.Named{Name: "ScheduleList", Value: changeactions.ProjectSchedules{}},
		wiregen.Named{Name: "ScheduleView", Value: changeactions.ProjectSchedule{}},
		wiregen.Named{Name: "ScheduleSource", Value: changeactions.ProjectSchedule{}.Source},
		wiregen.Named{Name: "ScheduleFiringView", Value: workflow.ScheduleRecord{}},
		wiregen.Named{Name: "RunningScan", Value: reflect.Zero(reflect.TypeOf(workflow.MainScans{}.Running).Elem()).Interface()},
		wiregen.Named{Name: "ScheduleRunView", Value: ScheduleRunView{}},
		wiregen.Named{Name: "ProjectScanView", Value: ProjectScanView{}},
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
		wiregen.Named{Name: "ConsoleStreamUpdate", Value: ConsoleStreamUpdate{}},
		wiregen.Named{Name: "ChatUpload", Value: console.Attachment{}},
		wiregen.Named{Name: "ChatHistoryView", Value: ChatHistoryView{}},
		wiregen.Named{Name: "ConsoleChangeList", Value: ConsoleChangeList{}},
		wiregen.Named{Name: "LandingStarted", Value: LandingStarted{}},
		wiregen.Named{Name: "RecheckStarted", Value: RecheckStarted{}},
		wiregen.Named{Name: "ConsolePreviewAddress", Value: ConsolePreviewAddress{}},
		wiregen.Named{Name: "DashboardView", Value: DashboardView{}},
		wiregen.Named{Name: "JobList", Value: JobList{}},
		wiregen.Named{Name: "RestartedLanding", Value: RestartedLanding{}},
		wiregen.Named{Name: "StepProgress", Value: StepProgress{}},
		wiregen.Named{Name: "StepApprovers", Value: workflow.StepApprovers{}},
		wiregen.Named{Name: "Approver", Value: identity.Approver{}},
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
