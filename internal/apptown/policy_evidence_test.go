package apptown

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// A declaration requires a running scenario, not just a documentation link.
// Scenarios may cover several contracts; each runs once within this suite,
// either as its own top-level test or, for a helper, through
// TestRegisteredPolicyEvidence.
var policyEvidence = map[string]func(*testing.T){
	"attention.Read":               TestAttentionHTTPFiltersSourcesAndCountsBeforeCurrentGrantReads,
	"routinePublications.Preview":  TestRoutinePublicationHTTPPreservesHumanIdentityAndRevokedRetry,
	"routinePublications.Classify": TestRoutinePublicationHTTPPreservesHumanIdentityAndRevokedRetry,
	"routinePublications.History":  TestRoutinePublicationHTTPPreservesHumanIdentityAndRevokedRetry,
	"routinePublications.Revoke":   TestRoutinePublicationHTTPPreservesHumanIdentityAndRevokedRetry,

	"standingApprovals.Assess": TestStandingConsentHTTPUsesCurrentDeclaredMeaningAndVerifiedIssuer,
	"external.Save":            TestExternalAdministrationHTTPUsesCurrentAuthorityAndSelfOwnership,
	"external.Enabled":         TestExternalAdministrationHTTPUsesCurrentAuthorityAndSelfOwnership,
	"external.Test":            TestExternalAdministrationHTTPUsesCurrentAuthorityAndSelfOwnership,
	"external.Bindings":        TestExternalAdministrationHTTPUsesCurrentAuthorityAndSelfOwnership,
	"a.handleExternalUnlink":   TestExternalAdministrationHTTPUsesCurrentAuthorityAndSelfOwnership,
	"external.Bind":            TestExternalAdministrationHTTPUsesCurrentAuthorityAndSelfOwnership,
	"external.Revoke":          TestExternalAdministrationHTTPUsesCurrentAuthorityAndSelfOwnership,

	"standingApprovals.List":            TestStandingConsentHTTPUsesCurrentDeclaredMeaningAndVerifiedIssuer,
	"standingApprovals.Preview":         TestStandingConsentHTTPUsesCurrentDeclaredMeaningAndVerifiedIssuer,
	"standingApprovals.Enable":          TestStandingConsentHTTPUsesCurrentDeclaredMeaningAndVerifiedIssuer,
	"standingApprovals.History":         TestStandingConsentHTTPUsesCurrentDeclaredMeaningAndVerifiedIssuer,
	"standingApprovals.Disable":         TestStandingConsentHTTPUsesCurrentDeclaredMeaningAndVerifiedIssuer,
	"deployment.RequestDefaultRelease":  TestReleasePlanHTTPProjectsActualHeadPublicationAndConfiguration,
	"deployments.ReleaseDefault":        TestEnvironmentLabelsAndDefaultHTTPUseCurrentAuthorityAndStableIdentity,
	"deployments.SetReleaseDefault":     TestEnvironmentLabelsAndDefaultHTTPUseCurrentAuthorityAndStableIdentity,
	"deployments.EditDeploymentLabel":   TestEnvironmentLabelsAndDefaultHTTPUseCurrentAuthorityAndStableIdentity,
	"deployments.DeploymentAddress":     TestDeploymentAddressHTTPChecksCurrentAuthorityAndRevision,
	"deployments.EditDeploymentAddress": TestDeploymentAddressHTTPChecksCurrentAuthorityAndRevision,
	"targets.EditTargetLabel":           TestEnvironmentLabelsAndDefaultHTTPUseCurrentAuthorityAndStableIdentity,

	"dashboard.Dashboard":       checkFilteredHTTPPolicies,
	"projectViews.ListProjects": checkFilteredHTTPPolicies,
	"adrs.ADRLibrary":           checkFilteredHTTPPolicies,
	"fleet.Fleet":               checkFilteredHTTPPolicies,
	"runs.ListRuns":             checkFilteredHTTPPolicies,
	"timing.Timing":             checkFilteredHTTPPolicies,

	"usage.Usage":                 checkMemberScopedReads,
	"tiers.Tiers":                 checkMemberScopedReads,
	"tiers.Departures":            checkMemberScopedReads,
	"activity.Activity":           checkMemberScopedReads,
	"policyLibrary.PolicyLibrary": checkMemberScopedReads,
	"policyLibrary.PolicyUsage":   checkMemberScopedReads,
	"guidance.GeneratedGuidance":  checkMemberScopedReads,
	"authorization.AuthzExplain":  checkMemberScopedReads,

	"adrs.GetADR":                                              checkFilteredHTTPPolicies,
	"creation.Create":                                          checkCreationPolicy,
	"conformance.Conformance":                                  checkConformancePolicies,
	"conformance.ReviewDetail":                                 checkConformancePolicies,
	"conformance.ListReviews":                                  checkConformancePolicies,
	"conformance.ConfirmVerdict":                               checkConformancePolicies,
	"remediationChanges.OpenRemediation":                       TestRemediationOpenHTTPUsesCurrentAdoptedCheckAndRetainsManualWork,
	"retention.SettleRetained":                                 TestRetainedSettlementChecksAuthorityAndHTTPOutcome,
	"runs.GetArtifact":                                         TestWorkHTTPRefusesIdentitySpoofAndPrivateEvidence,
	"accounts.ListDelegations":                                 checkDelegationPolicies,
	"accounts.RevokeDelegation":                                checkDelegationPolicies,
	"authorization.AuthzGrant":                                 TestGrantRevocationHTTPRemovesOnlySelectedAuthority,
	"authorization.AuthzRevoke":                                TestGrantRevocationHTTPRemovesOnlySelectedAuthority,
	"project_reads.listProjects":                               checkMCPReadPolicies,
	"project_reads.getProject":                                 checkMCPReadPolicies,
	"project_reads.getProjectGuidance":                         checkMCPReadPolicies,
	"project_reads.getApplicationContract":                     checkMCPReadPolicies,
	"work_reads.listChanges":                                   checkMCPReadPolicies,
	"work_reads.getChange":                                     checkMCPReadPolicies,
	"work_reads.getChangeDiff":                                 checkMCPReadPolicies,
	"work_reads.getChangeEvidence":                             checkMCPReadPolicies,
	"work_reads.getRun":                                        checkMCPReadPolicies,
	"work_reads.getWaitingOnYou":                               checkMCPReadPolicies,
	"lane_commands.createChange":                               checkMCPWritePolicies,
	"lane_commands.startChangeLane":                            checkMCPWritePolicies,
	"work_commands.requestChangeLanding":                       checkMCPWritePolicies,
	"work_commands.sendChangeBack":                             checkMCPWritePolicies,
	"work_commands.abandonChange":                              checkMCPWritePolicies,
	"checks.runChangeChecks":                                   checkMCPWritePolicies,
	"previews.previewChange":                                   TestPreviewCommandChecksCurrentAuthorityAndTransports,
	"contributions.checkoutChange":                             TestMCPCheckoutPushSync,
	"contributions.renewChangeGrant":                           TestMCPCheckoutPushSync,
	"contributions.syncChange":                                 TestMCPCheckoutPushSync,
	"project_creation.createProject":                           TestMCPCreateProject,
	"what_if.getWhatIf":                                        TestWhatIfAuthority,
	"evals.getEval":                                            TestEvalReadsTakeTheScorecardsAuthority,
	"operations.getOperation":                                  TestOperationRoutesAskTheOperationsOwner,
	"operations.Get":                                           TestOperationRoutesAskTheOperationsOwner,
	"operations.Cancel":                                        TestOperationRoutesAskTheOperationsOwner,
	"operations.Dismiss":                                       TestOperationRoutesAskTheOperationsOwner,
	"projectlifecycle.Request":                                 TestProjectDeletionRoutesAskTheDeletionOwner,
	"projectlifecycle.Cancel":                                  TestProjectDeletionRoutesAskTheDeletionOwner,
	"projectlifecycle.Restore":                                 TestProjectDeletionRoutesAskTheDeletionOwner,
	"projectlifecycle.PurgeNow":                                TestProjectDeletionRoutesAskTheDeletionOwner,
	"conformance.BeginWhatIf: the authority of a fleet review": TestWhatIfAuthority,
	"conformance.LatestWhatIf: conformance read":               TestWhatIfAuthority,
	"conformance.WhatIf: conformance read":                     TestWhatIfAuthority,
	"conformance.RecentWhatIfs: conformance read":              TestWhatIfAuthority,
	"evals.Start: standards:manage-packs and project:read on every source project":                              TestEvalRoutesAskTheEvalsOwner,
	"evals.List: standards:read-evals":                                                                          TestEvalRoutesAskTheEvalsOwner,
	"evals.StartForm: standards:manage-packs":                                                                   TestEvalRoutesAskTheEvalsOwner,
	"evals.Scorecard: standards:read-evals":                                                                     TestEvalRoutesAskTheEvalsOwner,
	"evals.ArmDiff: standards:read-evals and project:read on the source project, the arm found within its eval": TestEvalRoutesAskTheEvalsOwner,
	"evals.Continue: the starter, still a standards manager":                                                    TestEvalRoutesAskTheEvalsOwner,
	"evals.Delete: an administrator, or the starter still a standards manager":                                  TestEvalRoutesAskTheEvalsOwner,
}

func TestPolicyExceptionInventory(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	evidence := map[string]bool{}
	for name, check := range policyEvidence {
		evidence[name] = check != nil
	}
	// Evidence that naturally belongs to an adapter stays there. This is a Go
	// map of function values, compiled and executed in that package (as tests
	// or by its TestRegisteredPolicyEvidence), not a manifest of test-name
	// strings. Full validation runs both packages.
	f, err := parser.ParseFile(token.NewFileSet(), "httpapi/policy_evidence_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		v, ok := n.(*ast.ValueSpec)
		if !ok || len(v.Names) != 1 || v.Names[0].Name != "policyEvidence" {
			return true
		}
		entries := v.Values[0].(*ast.CompositeLit)
		for _, entry := range entries.Elts {
			pair := entry.(*ast.KeyValueExpr)
			name, err := strconv.Unquote(pair.Key.(*ast.BasicLit).Value)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := pair.Value.(*ast.Ident); !ok {
				t.Fatalf("%s needs a named executable scenario", name)
			}
			if evidence[name] {
				t.Fatalf("duplicate evidence ownership for %s", name)
			}
			evidence[name] = true
		}
		return false
	})
	required := map[string]bool{}
	for _, route := range appRoutes(a) {
		if p, ok := route.policy.(exceptionalPolicy); ok && (p.kind == "owner" || p.kind == "filtered") {
			required[p.contract] = true
			if !evidence[p.contract] {
				t.Errorf("%s (%s) has no executable evidence", route.pattern, p.contract)
			}
		}
	}
	for _, tool := range mcpToolsFor(a) {
		kind, contract := tool.Policy()
		if kind == "owner" || kind == "filtered" {
			required[contract] = true
			if !evidence[contract] {
				t.Errorf("MCP %s (%s) has no executable evidence", tool.Name, contract)
			}
		}
	}
	for contract := range evidence {
		if !required[contract] {
			t.Errorf("stale evidence binding: %s", contract)
		}
	}
}

func TestRegisteredPolicyEvidence(t *testing.T) {
	t.Parallel()
	if !runsAsATopLevelTest(TestRegisteredPolicyEvidence) || runsAsATopLevelTest(func(*testing.T) {}) || runsAsATopLevelTest(checkMCPReadPolicies) {
		t.Fatal("a skipped scenario would not run at all: the top-level test rule is wrong")
	}
	seen := map[uintptr]bool{}
	for _, contract := range slices.Sorted(maps.Keys(policyEvidence)) {
		check := policyEvidence[contract]
		if check == nil {
			t.Fatalf("%s has no executable evidence", contract)
		}
		id := reflect.ValueOf(check).Pointer()
		if !seen[id] && !runsAsATopLevelTest(check) {
			t.Run(contract, check)
		}
		seen[id] = true
	}
}

// runsAsATopLevelTest says whether go test already runs f as a test of its
// own: a package-level function in a _test.go file whose name follows go
// test's rule (Test, then anything but a lower-case letter). Such a scenario
// runs once in the suite already; running it here too only repeated it. A
// helper or closure is run by nobody else, so it runs here.
func runsAsATopLevelTest(f func(*testing.T)) bool {
	fn := runtime.FuncForPC(reflect.ValueOf(f).Pointer())
	if file, _ := fn.FileLine(fn.Entry()); !strings.HasSuffix(file, "_test.go") {
		return false
	}
	name := fn.Name()
	_, name, _ = strings.Cut(name[strings.LastIndex(name, "/")+1:], ".")
	rest, ok := strings.CutPrefix(name, "Test")
	if !ok || strings.Contains(rest, ".") {
		return false
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return rest == "" || !unicode.IsLower(r)
}
