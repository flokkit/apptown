package adoption

import (
	"context"
	"strings"
	"testing"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/wow"
)

// A candidate with problems is refused as unprocessable, not as a failure of
// AppTown: two packs pinning one workflow at different versions answered
// 500 from the pack route until the refusal was classified.
func TestCandidateValidationPreservesPreparationProblems(t *testing.T) {
	t.Parallel()
	db, service, candidate := validationCandidate(t)
	candidate.Problems = []string{"the selected packs disagree", "a required pin is missing"}
	const want = "cannot be adopted as asked: the selected packs disagree; a required pin is missing"
	err := service.validateCandidateIn(context.Background(), db, candidate.ProjectID, candidate)
	if err == nil || err.Error() != want || fault.KindOf(err) != fault.Unprocessable {
		t.Fatalf("preparation problems were hidden, admitted or unclassified: %v (%s)", err, fault.KindOf(err))
	}
	_, err = Adoption{ProjectID: candidate.ProjectID}.Prepare(candidate, nil)
	if err == nil || err.Error() != want || fault.KindOf(err) != fault.Unprocessable {
		t.Fatalf("Prepare hid, admitted or left unclassified the problems: %v (%s)", err, fault.KindOf(err))
	}
}

func TestCandidateValidationRechecksEnabledDeclaredConflicts(t *testing.T) {
	t.Parallel()
	db, service, candidate := validationCandidate(t)
	library := wow.NewLibrary(db)
	if err := library.StockInsert(t.Context(), wow.StockBlock{Name: "other-guide", Conflicts: "held-guide", Body: "another pinned body"}); err != nil {
		t.Fatal(err)
	}
	otherBlock, err := library.ByName(t.Context(), "other-guide")
	if err != nil {
		t.Fatal(err)
	}
	other := otherBlock.ID
	candidate.Receipt.Guidance = append(candidate.Receipt.Guidance, ReceiptBlock{Position: 1, BlockID: other, Name: "other-guide", Version: 1})
	candidate.Additions = []string{"other-guide"}
	candidate.Digest = Digest(candidate.Receipt)
	candidate.Problems = nil // Removing the presentation summary does not remove the rule.
	if err := service.validateCandidateIn(context.Background(), db, candidate.ProjectID, candidate); err == nil || !strings.Contains(err.Error(), "held-guide contradicts other-guide") {
		t.Fatalf("enabled contradiction accepted: %v", err)
	}
	candidate.Receipt.Guidance[1].Disabled = true
	candidate.Digest = Digest(candidate.Receipt)
	if err := service.validateCandidateIn(context.Background(), db, candidate.ProjectID, candidate); err != nil {
		t.Fatalf("disabled guidance started imposing its contradiction: %v", err)
	}
}
