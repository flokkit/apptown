package conformance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
// leave the previous verdict unchanged. All records commit together in our transaction.
func (s *Service) RecordLandingResults(ctx context.Context, projectID, changeID ids.ID, landed string, runID ids.ID, candidate string, receipts []LandingReceipt) error {
	if projectID.IsZero() || changeID.IsZero() || runID.IsZero() || landed == "" || candidate == "" {
		return fmt.Errorf("landing results require a project, change, landed commit, run and candidate")
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
		adopted := map[string]bool{}
		for _, c := range p.Checks {
			adopted[CheckKey(c.Block, c.Version, c.Check.Name)] = true
		}
		for key := range byCheck {
			if !adopted[key] {
				return fmt.Errorf("landing receipt is not an adopted check: %s", key)
			}
		}
		if len(byCheck) == 0 {
			return nil
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
			r, ran := byCheck[CheckKey(c.Block, c.Version, c.Check.Name)]
			if !ran {
				continue
			}
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
			v := Verdict{ReviewID: review.ID, ProjectID: projectID, ProjectSlug: p.Slug, SHA: landed,
				Block: c.Block, Version: c.Version, Check: c.Check.Name, Evaluator: check.Evaluator(c.Check),
				EvaluatorVer: r.EvaluatorVer, Mode: "gate", State: state, Severity: severity,
				Detail: detail, At: at.String(), ExceptedBy: ref, EvidenceHash: r.EvidenceHash, AgentEvidence: r.AgentEvidence}
			prev, err := readLatestVerdict(ctx, tx, projectID, c.Block, c.Version, c.Check.Name)
			if _, err := database.Lookup(err); err != nil {
				return err
			}
			needed, err := fixRequired(ctx, tx, prev, v)
			if err != nil {
				return err
			}
			req := verdictAdmission{Verdict: v, PreviousID: prev.ID}
			if needed {
				req.Fix = &FixAttribution{Changes: []ids.ID{changeID}, Commits: landed}
			}
			if _, err := s.records.recordVerdictIn(ctx, tx, req); err != nil {
				return err
			}
		}
		n, err := queries(tx).CompleteLandingReview(ctx, conformancesql.CompleteLandingReviewParams{
			ID: review.ID, SourceRun: runID, Candidate: candidate, At: at.NullTime, Verdicts: int64(len(byCheck)),
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

// LandingVerdict reads one check actually recorded for a landing run. Absence
// means that check still needs a review; other checks' receipts prove nothing.
func (s *Service) LandingVerdict(ctx context.Context, projectID, runID ids.ID, c CheckIdentity) (*Verdict, error) {
	if _, err := s.require(ctx, permission{Action: readProject, ProjectID: projectID}); err != nil {
		return nil, err
	}
	row, err := queries(s.db).LandingVerdict(ctx, conformancesql.LandingVerdictParams{SourceRun: runID, ProjectID: projectID, Block: c.Block, Version: int64(c.Version), CheckName: c.Check})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v, err := verdict(row)
	return &v, err
}
