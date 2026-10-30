package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/instanceupdate"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/evals"
	"github.com/flokkit/apptown/internal/wiregen"
)

// conformanceObservationWire is what conformance.Observation encodes: the
// verdict it carries or synthesizes, and its state — an outcome word, or
// "stale" / "not-scanned" when nothing current was assessed.
type conformanceObservationWire struct {
	conformance.VerdictView
	State string `json:"state"`
}

// The conformance, people, models and usage responses.
func init() {
	registerWireMapped(instanceupdate.Candidate{}, "InstanceUpdateCandidate")
	registerWireMapped(instanceupdate.Evidence{}, "InstanceUpdateEvidence")
	registerWireMapped(instanceupdate.Artifact{}, "InstanceUpdateArtifact")
	registerWireMapped(conformance.Verdict{}, "Verdict")
	registerWireMapped(conformance.Observation{}, "ConformanceObservation")
	registerWire(
		wiregen.Named{Name: "Verdict", Value: conformance.VerdictView{}},
		wiregen.Named{Name: "InstanceUpdateStatus", Value: instanceupdate.Status{}},
		wiregen.Named{Name: "InstanceUpdateCandidate", Value: instanceupdate.Candidate{}},
		wiregen.Named{Name: "InstanceUpdateEvidence", Value: instanceupdate.Evidence{}},
		wiregen.Named{Name: "InstanceUpdateArtifact", Value: instanceupdate.Artifact{}},
		wiregen.Named{Name: "ConformanceObservation", Value: conformanceObservationWire{}},
		wiregen.Named{Name: "DelegationList", Value: DelegationList{}},
		wiregen.Named{Name: "DelegationRevoked", Value: DelegationRevoked{}},
		wiregen.Named{Name: "AdministratorNetworksView", Value: AdministratorNetworksView{}},
		wiregen.Named{Name: "GrantCreated", Value: GrantCreated{}},
		wiregen.Named{Name: "AuthzExplanation", Value: AuthzExplanation{}},
		wiregen.Named{Name: "AuthzHoldings", Value: AuthzHoldings{}},
		wiregen.Named{Name: "BrowserErrorReporting", Value: BrowserErrorReporting{}},
		wiregen.Named{Name: "ComplaintFiled", Value: ComplaintFiled{}},
		wiregen.Named{Name: "ComplaintList", Value: ComplaintList{}},
		wiregen.Named{Name: "ComplaintsArchived", Value: ComplaintsArchived{}},
		wiregen.Named{Name: "ComplaintSteeringText", Value: ComplaintSteeringText{}},
		wiregen.Named{Name: "ConformanceReviewList", Value: ConformanceReviewList{}},
		wiregen.Named{Name: "ConformanceView", Value: conformance.View{}},
		wiregen.Named{Name: "ConformanceReviewDetail", Value: conformance.ReviewDetail{}},
		wiregen.Named{Name: "WhatIfStarted", Value: WhatIfStarted{}},
		wiregen.Named{Name: "EvalList", Value: EvalList{}},
		wiregen.Named{Name: "EvalDeleted", Value: EvalDeleted{}},
		wiregen.Named{Name: "EvalScorecard", Value: evals.Scorecard{}},
		wiregen.Named{Name: "EvalStartForm", Value: evals.StartForm{}},
		wiregen.Named{Name: "LatestWhatIf", Value: LatestWhatIf{}},
		wiregen.Named{Name: "RecentWhatIfs", Value: RecentWhatIfs{}},
		wiregen.Named{Name: "ConformanceHistory", Value: ConformanceHistory{}},
		wiregen.Named{Name: "ConformanceFixList", Value: ConformanceFixList{}},
		wiregen.Named{Name: "DispositionCreated", Value: DispositionCreated{}},
		wiregen.Named{Name: "DispositionList", Value: DispositionList{}},
		wiregen.Named{Name: "OpenVEXDocument", Value: OpenVEXDocument{}},
		wiregen.Named{Name: "RunFindingsView", Value: RunFindingsView{}},
		wiregen.Named{Name: "ProjectFindingsView", Value: ProjectFindingsView{}},
		wiregen.Named{Name: "FleetView", Value: FleetView{}},
		wiregen.Named{Name: "ProjectMemberList", Value: ProjectMemberList{}},
		wiregen.Named{Name: "ModelCatalogList", Value: ModelCatalogList{}},
		wiregen.Named{Name: "DiscoveredModels", Value: DiscoveredModels{}},
		wiregen.Named{Name: "ModelSlotMatrix", Value: ModelSlotMatrix{}},
		wiregen.Named{Name: "RemediationOpened", Value: RemediationOpened{}},
		wiregen.Named{Name: "RemediationList", Value: RemediationList{}},
		wiregen.Named{Name: "RemediationDetail", Value: RemediationDetail{}},
		wiregen.Named{Name: "RemediationPreview", Value: RemediationPreview{}},
		wiregen.Named{Name: "RemediationStarted", Value: RemediationStarted{}},
		wiregen.Named{Name: "WorkerPools", Value: WorkerPools{}},
		wiregen.Named{Name: "UsageReport", Value: UsageReport{}},
	)
}

// An optional list stays absent when unset; a required one is written empty.
// The usage report relies on it: byProject and byWorkflow appear only in the
// scope they belong to.
func TestOptionalListsStayAbsentInTypedResponses(t *testing.T) {
	w := httptest.NewRecorder()
	WriteJSON(w, 200, UsageReport{})
	body := w.Body.String()
	if strings.Contains(body, "byProject") || strings.Contains(body, "byWorkflow") || !strings.Contains(body, `"byKind":[]`) {
		t.Fatalf("body: %s", body)
	}
}
