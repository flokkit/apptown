package httpapi

import (
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/wiregen"
)

// The ways-of-working and project routes' responses.
func init() {
	registerWire(
		wiregen.Named{Name: "ProjectDraft", Value: ProjectDraft{}},
		wiregen.Named{Name: "ProjectCreationSetup", Value: projectlifecycle.CreationSetup{}},
		wiregen.Named{Name: "ProjectCreationService", Value: projectlifecycle.CreationService{}},
		wiregen.Named{Name: "ProjectCreationUpstream", Value: projectlifecycle.CreationUpstream{}},
		wiregen.Named{Name: "AdoptionUpliftOpened", Value: AdoptionUpliftOpened{}},
		wiregen.Named{Name: "InAdvanceQuestion", Value: workflow.InAdvanceQuestion{}},
		wiregen.Named{Name: "AdoptionApplied", Value: AdoptionApplied{}},
		wiregen.Named{Name: "CatalogList", Value: CatalogList{}},
		wiregen.Named{Name: "CatalogApplied", Value: CatalogApplied{}},
		wiregen.Named{Name: "CatalogChangeDetail", Value: CatalogChangeDetail{}},
		wiregen.Named{Name: "DocsIndex", Value: DocsIndex{}},
		wiregen.Named{Name: "DocGuide", Value: DocGuide{}},
		wiregen.Named{Name: "GuidanceBlockDetail", Value: GuidanceBlockDetail{}},
		wiregen.Named{Name: "ProjectGuidance", Value: ProjectGuidance{}},
		wiregen.Named{Name: "GeneratedGuidance", Value: GeneratedGuidance{}},
		wiregen.Named{Name: "PackInterviewPick", Value: PackInterviewPick{}},
		wiregen.Named{Name: "PackInterviewQuestions", Value: PackInterviewQuestions{}},
		wiregen.Named{Name: "PackOffered", Value: PackOffered{}},
		wiregen.Named{Name: "PackSaved", Value: PackSaved{}},
		wiregen.Named{Name: "PackPulled", Value: PackPulled{}},
		wiregen.Named{Name: "PullPlan", Value: adoption.PullPlan{}},
		wiregen.Named{Name: "DeploymentView", Value: DeploymentView{}},
		wiregen.Named{Name: "AuthMe", Value: AuthMe{}},
		wiregen.Named{Name: "ToolchainWritten", Value: ToolchainWritten{}},
		wiregen.Named{Name: "PolicyLibraryList", Value: PolicyLibraryList{}},
		wiregen.Named{Name: "PolicyUsageList", Value: PolicyUsageList{}},
		wiregen.Named{Name: "ProjectAdoptionRecord", Value: ProjectAdoptionRecord{}},
		wiregen.Named{Name: "ProjectDeletionState", Value: projectDeletionState{}},
		wiregen.Named{Name: "ProjectCleanupList", Value: ProjectCleanupList{}},
		wiregen.Named{Name: "ProjectManifestView", Value: ProjectManifestView{}},
		wiregen.Named{Name: "ProjectOverviewView", Value: ProjectOverviewView{}},
		wiregen.Named{Name: "ProjectPolicyList", Value: ProjectPolicyList{}},
		wiregen.Named{Name: "ProjectPage", Value: ProjectPage{}},
		wiregen.Named{Name: "ProjectRow", Value: ProjectRow{}},
		wiregen.Named{Name: "ProjectDetail", Value: ProjectDetail{}},
		wiregen.Named{Name: "Project", Value: project.Project{}},
		wiregen.Named{Name: "ProjectLanes", Value: ProjectLanes{}},
		wiregen.Named{Name: "ModelConfigured", Value: ModelConfigured{}},
		wiregen.Named{Name: "SetupState", Value: SetupState{}},
		wiregen.Named{Name: "SystemWorkflowBindings", Value: SystemWorkflowBindings{}},
		wiregen.Named{Name: "LibraryWorkflow", Value: LibraryWorkflow{}},
		wiregen.Named{Name: "ProjectWorkflowRow", Value: ProjectWorkflowRow{}},
		wiregen.Named{Name: "PackView", Value: PackView{}},
		wiregen.Named{Name: "PackHistory", Value: wow.PackHistory{}},
		wiregen.Named{Name: "WorkflowInvalid", Value: WorkflowInvalid{}},
		wiregen.Named{Name: "WorkflowPreview", Value: WorkflowPreview{}},
		wiregen.Named{Name: "WorkflowPublished", Value: WorkflowPublished{}},
	)
}
