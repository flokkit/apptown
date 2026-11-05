package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/conformance/internal/conformancesql"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/stamp"
)

// LandingReceipt is one guidance check's saved judgement. The landing adapter
// translates Work's receipt; conformance owns interpreting it as standing.
type LandingReceipt struct {
	AgentEvidence                    *check.AgentEvidence
	Block                            string
	Version                          int
	Check, Verdict                   string
	EvaluatorVer, EvidenceHash       string
	Detail, Output, Note, ExceptedBy string
}

// RecordLandingResults records witnessed guidance receipts after SCM has recorded
// a successful landing. Its caller supplies that settled result's commit, run and
// checked candidate; this operation never evaluates or merges. Missing receipts
// record nothing. All conformance records commit together in our own transaction.
func (s *Service) RecordLandingResults(ctx context.Context, projectID ids.ID, landed string, runID ids.ID, candidate string, receipts []LandingReceipt) error {
	if projectID.IsZero() || runID.IsZero() || landed == "" || candidate == "" {
		return fmt.Errorf("landing results require a project, landed commit, run and candidate")
	}
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		if err := lockConformanceProject(ctx, tx, projectID); err != nil {
			return err
		}
		recorded, err := queries(tx).LandingResultsRecorded(ctx, runID)
		if err != nil || recorded {
			return err
		}
		projects, err := readPreparedConformanceProjects(ctx, tx, s.records.checks, []ids.ID{projectID}, false)
		if err != nil {
			return err
		}
		if len(projects) != 1 {
			return fmt.Errorf("landing project is unavailable")
		}
		p := projects[0]
		byCheck := map[string]LandingReceipt{}
		for _, r := range receipts {
			key := CheckKey(r.Block, r.Version, r.Check)
			if _, exists := byCheck[key]; exists {
				return fmt.Errorf("duplicate landing receipt for %s", key)
			}
			byCheck[key] = r
		}
		// Check completeness before creating even a review: partial evidence must
		// not make the landed tree look assessed.
		for _, c := range p.Checks {
			if _, ok := byCheck[CheckKey(c.Block, c.Version, c.Check.Name)]; !ok {
				return nil
			}
		}
		if len(byCheck) != len(p.Checks) {
			return fmt.Errorf("landing receipts differ from adopted checks")
		}
		review, err := s.records.create(ctx, tx, ReviewRequest{Trigger: "landing", Projects: []ids.ID{projectID}}, "apptown-system", ReviewRunning)
		if err != nil {
			return err
		}
		exceptions, err := s.records.snapshotExceptionsIn(ctx, tx, review.ID, projectID)
		if err != nil {
			return err
		}
		at := stamp.Of(s.records.now())
		for _, c := range p.Checks {
			r := byCheck[CheckKey(c.Block, c.Version, c.Check.Name)]
			state, err := landingOutcome(r.Verdict)
			if err != nil {
				return err
			}
			ref := r.ExceptedBy
			if state == DoesNotConform && ref == "" {
				if matched, ok := ExceptionFor(c, exceptions); ok {
					encoded, err := json.Marshal(matched)
					if err != nil {
						return err
					}
					state, ref = Excepted, string(encoded)
				}
			}
			detail := strings.TrimSpace(strings.Join([]string{r.Detail, r.Output, r.Note}, "\n"))
			severity := c.Check.Severity
			if severity == "" {
				severity = "note"
			}
			if _, err := queries(tx).InsertVerdict(ctx, conformancesql.InsertVerdictParams{
				ID: ids.New(at.Time), ReviewID: review.ID, ProjectID: projectID, ProjectSlug: p.Slug, Sha: landed,
				Block: c.Block, Version: int64(c.Version), CheckName: c.Check.Name, Evaluator: check.Evaluator(c.Check),
				EvaluatorVer: r.EvaluatorVer, Mode: "gate", State: state.String(), Severity: severity,
				Detail: detail, CreatedAt: at.Time, ExceptedBy: ref, EvidenceHash: r.EvidenceHash, AgentEvidence: agentEvidenceJSON(r.AgentEvidence),
			}); err != nil {
				return err
			}
		}
		n, err := queries(tx).CompleteLandingReview(ctx, conformancesql.CompleteLandingReviewParams{
			ID: review.ID, SourceRun: runID, Candidate: candidate, At: at.NullTime, Verdicts: int64(len(p.Checks)),
		})
		return reviewRowsWritten(n, err)
	})
}

func landingOutcome(v string) (Outcome, error) {
	switch v {
	case "pass":
		return Conforms, nil
	case "fail":
		return DoesNotConform, nil
	case "excepted":
		return Excepted, nil
	case "not-applicable":
		return NotApplicable, nil
	case "error":
		return EvaluationError, nil
	case "skipped", "cannot-assess", "unclear":
		return CannotAssess, nil
	default:
		return 0, fmt.Errorf("invalid landing check verdict: %s", v)
	}
}
