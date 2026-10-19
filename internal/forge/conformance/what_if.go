package conformance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/conformance/internal/conformancesql"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/stamp"
)

// A what-if (design/subsystems/conformance.md) answers "what
// would happen to these projects if they took this pack version?" by
// judging the version's checks on each project's committed main beside the
// checks it adopted; a check it cannot judge there says why. A project not
// on the pack is asked as if it joined. It is hypothetical by construction: it
// begins no review, takes no exception snapshot and writes no verdict, and
// its two tables are read by nothing that decides standing, gates,
// exceptions or remediation.

// WhatIfRequest names the pack version and the projects to run it on.
type WhatIfRequest struct {
	Pack     string   `json:"pack"`
	Version  int64    `json:"version"`
	Projects []ids.ID `json:"projects"`
	// Rubrics includes model rubrics, which cost money: only with a spend
	// limit, which stops further rubric calls once observed spend reaches
	// it (one project's checklist may overshoot it).
	Rubrics          bool  `json:"rubrics,omitempty"`
	SpendLimitMicros int64 `json:"spendLimitMicros,omitempty"`
}

// What-if states. Queued: admitted, waiting for the conformance queue's
// worker. Interrupted: the forge stopped or restarted before it finished;
// it is not resumed, and projects it did not reach were not evaluated.
const (
	WhatIfQueued      = "queued"
	WhatIfRunning     = "running"
	WhatIfDone        = "done"
	WhatIfError       = "error"
	WhatIfInterrupted = "interrupted"
)

// WhatIfSpend is what a project's rubric calls cost: Incomplete counts
// checklists with some spend unknown (a checklist's retry is not counted
// apart), which make the total "at least".
type WhatIfSpend struct {
	CostMicros int64
	Incomplete int64
}

// What-if results, per project and check, matched by block and check name.
const (
	WhatIfWouldFail      = "would-fail"         // passes or is not required today; fails under the version
	WhatIfWouldPass      = "would-pass"         // fails today; passes under the version
	WhatIfUnchanged      = "unchanged"          // the same answer both ways
	WhatIfNotAssessed    = "not-assessed"       // the version's side could not be judged
	WhatIfNoLongerNeeded = "no-longer-required" // the version does not carry the check
)

// NotAssessed is why a check was not judged in a what-if or an eval's
// comparison: one of a fixed set, so every surface says it the same way,
// with the tool's or provider's own detail beside it.
type NotAssessed string

const (
	// NotAssessedError: the check ran and could not answer (a script that
	// neither passed nor failed, a tool that errored, a model that gave no
	// verdict).
	NotAssessedError NotAssessed = "error"
	// NotAssessedNoImage: an image tool needs the app's image of this commit,
	// and none could be had (the app ships none, or the build budget is
	// spent).
	NotAssessedNoImage NotAssessed = "no-image"
	// NotAssessedNoRunningApp: a runtime probe needs the app running this
	// exact commit, and no deployment serves it.
	NotAssessedNoRunningApp NotAssessed = "no-running-app"
	// NotAssessedRubricsNotIncluded: model rubrics cost money; the person who
	// started it did not include them.
	NotAssessedRubricsNotIncluded NotAssessed = "rubrics-not-included"
	// NotAssessedSpendLimit: the spend limit was reached before this
	// project's rubrics were sent.
	NotAssessedSpendLimit NotAssessed = "spend-limit"
	// NotAssessedSpendUnknown: an earlier model call's spend is unknown, so
	// no more were sent.
	NotAssessedSpendUnknown NotAssessed = "spend-unknown"
	// NotAssessedJudgesAChange: a diff rubric judges what a change did, and
	// there is no change here, only a commit.
	NotAssessedJudgesAChange NotAssessed = "judges-a-change"
	// NotAssessedNoEvaluator: the forge has no evaluator for this check.
	NotAssessedNoEvaluator NotAssessed = "no-evaluator"
	// NotAssessedNoMain: the project has no committed main to read.
	NotAssessedNoMain NotAssessed = "no-main"
	// NotAssessedNotAdoptable: the version could not be adopted as proposed.
	NotAssessedNotAdoptable NotAssessed = "not-adoptable"
)

// NotAssessedReasons is every reason, in the order a page lists them.
var NotAssessedReasons = []NotAssessed{NotAssessedError, NotAssessedNoImage, NotAssessedNoRunningApp, NotAssessedRubricsNotIncluded,
	NotAssessedSpendLimit, NotAssessedSpendUnknown, NotAssessedJudgesAChange, NotAssessedNoEvaluator, NotAssessedNoMain, NotAssessedNotAdoptable}

// Words says the reason in a sentence a reader acts on.
func (u NotAssessed) Words() string {
	switch u {
	case NotAssessedError:
		return "the check could not answer"
	case NotAssessedNoImage:
		return "no image of the app at this commit could be used"
	case NotAssessedNoRunningApp:
		return "needs the app running this commit, and no deployment serves it; an eval runs it"
	case NotAssessedRubricsNotIncluded:
		return "model rubrics were not included"
	case NotAssessedSpendLimit:
		return "the spend limit was reached"
	case NotAssessedSpendUnknown:
		return "an earlier model call's spend is unknown, so no more were sent"
	case NotAssessedJudgesAChange:
		return "judges a change, not a commit; an eval judges it on re-run work"
	case NotAssessedNoEvaluator:
		return "AppTown has no evaluator for this check"
	case NotAssessedNoMain:
		return "no committed main to read"
	case NotAssessedNotAdoptable:
		return "could not be adopted as proposed"
	}
	return string(u)
}

// WhatIfResult classifies one check. A nil outcome is a check that side
// does not carry. An excepted check counts as failing on either side, so
// "excepted" to "does not conform" is unchanged here; whether its exception
// still covers the version's check is the project's answer
// (WhatIfByProject), and the row says so. Inability to judge either side is
// never an improvement.
func WhatIfResult(current, proposed *Outcome) string {
	failing := func(o *Outcome) bool { return o != nil && (*o == DoesNotConform || *o == Excepted) }
	judged := func(o *Outcome) bool { return o != nil && (*o == Conforms || failing(o)) }
	switch {
	case proposed == nil:
		return WhatIfNoLongerNeeded
	case !judged(proposed) || (current != nil && !judged(current)):
		return WhatIfNotAssessed
	case failing(proposed) && !failing(current):
		return WhatIfWouldFail
	case !failing(proposed) && failing(current):
		return WhatIfWouldPass
	}
	return WhatIfUnchanged
}

// AttemptHistory is what past fixes of one block's check cost: shown only
// with at least three settled attempts.
type AttemptHistory struct {
	Attempts       int     `json:"attempts"`
	MedianCost     float64 `json:"medianCost"`
	MedianMinutes  float64 `json:"medianMinutes"`
	SucceededShare float64 `json:"succeededShare"`
}

// WhatIfRow is one project's answer for one check, or for the whole
// project when Block is empty (no source, or it could not adopt the version).
type WhatIfRow struct {
	ProjectID        ids.ID          `json:"projectId"`
	ProjectSlug      string          `json:"projectSlug"`
	SHA              string          `json:"sha"`
	AdoptionRevision int64           `json:"adoptionRevision"`
	CandidateDigest  string          `json:"candidateDigest,omitempty"`
	Block            string          `json:"block,omitempty"`
	Check            string          `json:"check,omitempty"`
	CurrentVersion   int             `json:"currentVersion,omitempty"`
	ProposedVersion  int             `json:"proposedVersion,omitempty"`
	Current          string          `json:"current,omitempty"`  // today's outcome; empty when not required
	Proposed         string          `json:"proposed,omitempty"` // under the version; empty when not required
	Result           string          `json:"result"`
	Detail           string          `json:"detail,omitempty"`
	Brief            string          `json:"brief,omitempty"`
	History          *AttemptHistory `json:"history,omitempty"`
	// Reason is why a not-assessed row was not judged; its words lead the
	// detail.
	Reason NotAssessed `json:"reason,omitempty"`
	// Joins: the project is not on the pack; the row is what joining says.
	Joins bool `json:"joins,omitempty"`
}

// WhatIf is one what-if with its rows.
type WhatIf struct {
	ID          ids.ID `json:"id"`
	Pack        string `json:"pack"`
	Version     int64  `json:"version"`
	RequestedBy string `json:"requestedBy"`
	// RequestedByPrincipal is the requester's principal; zero when not resolved to a person.
	RequestedByPrincipal ids.ID     `json:"requestedByPrincipal,omitzero"`
	State                string     `json:"state"`
	Detail               string     `json:"detail,omitempty"`
	ProjectsTotal        int64      `json:"projectsTotal"`
	ProjectsDone         int64      `json:"projectsDone"`
	CreatedAt            stamp.Time `json:"createdAt"`
	FinishedAt           stamp.Time `json:"finishedAt"`
	// Rubrics says model rubrics were included, under SpendLimitMicros;
	// CostMicros is what they cost, "at least" when Incomplete > 0.
	Rubrics          bool        `json:"rubrics"`
	SpendLimitMicros int64       `json:"spendLimitMicros"`
	CostMicros       int64       `json:"costMicros"`
	Incomplete       int64       `json:"incomplete"`
	Rows             []WhatIfRow `json:"rows"`
	// Projects is each selected project's answer, in selection order
	// (WhatIfByProject).
	Projects []WhatIfProject `json:"projects"`
}

// ErrNoWhatIf is the absence of a what-if a caller addressed.
var ErrNoWhatIf = fault.New(fault.NotFound, "no such what-if")

// WhatIfRun is a claimed what-if: its projects, their source and
// exceptions read without writing, the version's checks, and its own rows.
type WhatIfRun struct {
	service  *Service
	id       ids.ID
	selected []ids.ID
}

// AdmitWhatIfIn admits a what-if with the authority of a fleet review, as
// queued in the caller's transaction, which also queues the job that runs
// it: neither commits alone. The record keeps the request; the job names
// only its ID. A request that names no projects (nil) runs on the eligible
// ones, read only once the starter's authority holds and stored as the
// list they were then; none is refused.
func (s *Service) AdmitWhatIfIn(ctx context.Context, tx *database.Tx, in WhatIfRequest, eligible func() ([]ids.ID, error)) (ids.ID, error) {
	if in.Pack == "" || in.Version < 1 {
		return ids.Nil, fault.New(fault.Invalid, "a what-if names a pack and a version")
	}
	if in.Rubrics != (in.SpendLimitMicros > 0) {
		return ids.Nil, fault.New(fault.Invalid, "model rubrics are included only with a spend limit above zero, and a spend limit only with them")
	}
	who, err := s.require(ctx, Permission{Action: RunWhatIf, Projects: slices.Clone(in.Projects)})
	if err != nil {
		return ids.Nil, err
	}
	if in.Projects == nil {
		if in.Projects, err = eligible(); err != nil {
			return ids.Nil, err
		}
		if len(in.Projects) == 0 {
			return ids.Nil, fault.New(fault.Invalid, fmt.Sprintf("no project with a repository is on an earlier version of %s than v%d; name the projects to run it on", in.Pack, in.Version))
		}
	}
	// JSON keeps the difference between the whole estate (null) and no
	// projects ([]).
	projects, err := json.Marshal(in.Projects)
	if err != nil {
		return ids.Nil, err
	}
	requester, err := identity.ActorIn(ctx, tx, who)
	if err != nil {
		return ids.Nil, err
	}
	r, err := queries(tx).InsertWhatIf(ctx, conformancesql.InsertWhatIfParams{ID: ids.New(s.records.now().UTC()), Pack: in.Pack, PackVersion: in.Version, RequestedBy: who, RequestedByPrincipal: requester.Principal, RequestedByVia: requester.Via, Rubrics: in.Rubrics, SpendLimitMicros: in.SpendLimitMicros, CreatedAt: s.records.now().UTC(), Projects: string(projects)})
	if err != nil {
		return ids.Nil, err
	}
	return r.ID, nil
}

// ClaimWhatIf starts a queued what-if for the one delivery that claims it,
// with its saved request. It needs no caller: its authority is the
// admission, and it reaches only what that record asked. A what-if already
// claimed or ended is ErrNotQueued.
func (s *Service) ClaimWhatIf(ctx context.Context, id ids.ID) (*WhatIfRun, WhatIfRequest, error) {
	r, err := queries(s.db).ClaimQueuedWhatIf(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, WhatIfRequest{}, ErrNotQueued
	}
	if err != nil {
		return nil, WhatIfRequest{}, err
	}
	in := WhatIfRequest{Pack: r.Pack, Version: r.PackVersion, Rubrics: r.Rubrics, SpendLimitMicros: r.SpendLimitMicros}
	if err := json.Unmarshal(r.Projects, &in.Projects); err != nil {
		return nil, WhatIfRequest{}, fmt.Errorf("what-if %d's selection: %w", id, err)
	}
	return &WhatIfRun{service: s, id: r.ID, selected: slices.Clone(in.Projects)}, in, nil
}

func (r *WhatIfRun) ID() ids.ID { return r.id }

// Projects is the selection with the checks each adopted, repo-backed only.
func (r *WhatIfRun) Projects(ctx context.Context) ([]Project, error) {
	projects, err := readConformance(ctx, r.service.db, func(db database.Queryer) ([]Project, error) {
		return readPreparedConformanceProjects(ctx, db, r.service.source, r.selected, true)
	})
	if err != nil {
		return nil, err
	}
	_, err = queries(r.service.db).WhatIfStarted(ctx, conformancesql.WhatIfStartedParams{ID: r.id, ProjectsTotal: int64(len(projects))})
	return projects, err
}

// Source is a project's committed head.
func (r *WhatIfRun) Source(ctx context.Context, p Project) (SourceHead, error) {
	return r.service.sourceHead(ctx, p)
}

// Exceptions are a project's active exceptions, read and not snapshotted.
func (r *WhatIfRun) Exceptions(ctx context.Context, projectID ids.ID) ([]Disposition, error) {
	return readDispositions(ctx, r.service.db, []ids.ID{projectID}, true, r.service.records.now())
}

// ChecksAt is the checks the given block versions declare.
func (r *WhatIfRun) ChecksAt(ctx context.Context, blocks []CheckVersion) ([]DeclaredCheck, error) {
	return r.service.ChecksAt(ctx, blocks)
}

// ChecksAt is the checks the given block versions declare: published
// library content, read for the forge's own comparisons (a what-if, an
// eval's arms), so it asks no grant and decides nothing.
func (s *Service) ChecksAt(ctx context.Context, blocks []CheckVersion) ([]DeclaredCheck, error) {
	if s.source == nil {
		return nil, errors.New("the check source is unavailable")
	}
	return s.source.HistoricalChecksIn(ctx, s.db, blocks)
}

// Record keeps one project's rows and what its rubric calls cost, and
// counts it done, together.
func (r *WhatIfRun) Record(ctx context.Context, rows []WhatIfRow, spend WhatIfSpend) error {
	return r.service.db.InTx(ctx, func(tx *database.Tx) error {
		q := queries(tx)
		for _, row := range rows {
			history := []byte("{}")
			if row.History != nil {
				history, _ = json.Marshal(row.History)
			}
			if err := q.InsertWhatIfRow(ctx, conformancesql.InsertWhatIfRowParams{ID: ids.New(r.service.records.now()), WhatIfID: r.id, ProjectID: row.ProjectID, ProjectSlug: row.ProjectSlug,
				Sha: row.SHA, AdoptionRevision: row.AdoptionRevision, CandidateDigest: row.CandidateDigest, Block: row.Block, CheckName: row.Check,
				CurrentVersion: int64(row.CurrentVersion), ProposedVersion: int64(row.ProposedVersion), CurrentState: row.Current, ProposedState: row.Proposed,
				Result: row.Result, Detail: row.Detail, Brief: row.Brief, HistoryJson: history, Reason: string(row.Reason), Joins: row.Joins}); err != nil {
				return err
			}
		}
		n, err := q.WhatIfProgress(ctx, conformancesql.WhatIfProgressParams{ID: r.id, CostMicros: spend.CostMicros, Incomplete: spend.Incomplete})
		if err == nil && n != 1 {
			err = fmt.Errorf("what-if %d is no longer running", r.id)
		}
		return err
	})
}

// ErrInterrupted is work stopped before it finished, by the forge shutting
// down or its time running out; it is not resumed.
var ErrInterrupted = errors.New("stopped before it finished")

// Finish settles the what-if: done, stopped with why, or interrupted when
// walkErr is ErrInterrupted. Projects not recorded were not evaluated, and
// the page says so.
func (r *WhatIfRun) Finish(ctx context.Context, walkErr error) error {
	state, detail := WhatIfDone, ""
	switch {
	case errors.Is(walkErr, ErrInterrupted):
		state, detail = WhatIfInterrupted, "AppTown stopped this what-if before it finished; projects it did not reach were not evaluated"
	case walkErr != nil:
		state, detail = WhatIfError, walkErr.Error()
	}
	_, err := queries(r.service.db).FinishWhatIf(ctx, conformancesql.FinishWhatIfParams{ID: r.id, State: state, Detail: detail, At: r.service.records.now().UTC()})
	return err
}

// WhatIf reads one what-if with its rows. Reading the fleet's conformance
// is an instance grant, which reaches every project's rows.
func (s *Service) WhatIf(ctx context.Context, id ids.ID) (*WhatIf, error) {
	if _, err := s.require(ctx, Permission{Action: ReadFleet}); err != nil {
		return nil, err
	}
	return readConformance(ctx, s.db, func(db database.Queryer) (*WhatIf, error) {
		r, err := queries(db).WhatIfByID(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w %d", ErrNoWhatIf, id)
		}
		if err != nil {
			return nil, err
		}
		return whatIfWithRows(ctx, db, r)
	})
}

// LatestWhatIf is the newest what-if of a pack version, nil when none ran.
func (s *Service) LatestWhatIf(ctx context.Context, pack string, version int64) (*WhatIf, error) {
	if _, err := s.require(ctx, Permission{Action: ReadFleet}); err != nil {
		return nil, err
	}
	return readConformance(ctx, s.db, func(db database.Queryer) (*WhatIf, error) {
		r, err := queries(db).LatestWhatIf(ctx, conformancesql.LatestWhatIfParams{Pack: pack, PackVersion: version})
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return whatIfWithRows(ctx, db, r)
	})
}

func whatIfWithRows(ctx context.Context, db database.Queryer, r conformancesql.WhatIfReview) (*WhatIf, error) {
	out := &WhatIf{ID: r.ID, Pack: r.Pack, Version: r.PackVersion, RequestedBy: r.RequestedBy, RequestedByPrincipal: r.RequestedByPrincipal, State: r.State, Detail: r.Detail,
		ProjectsTotal: r.ProjectsTotal, ProjectsDone: r.ProjectsDone, CreatedAt: stamp.Time{NullTime: sql.NullTime{Time: r.CreatedAt, Valid: true}},
		FinishedAt: stamp.Time{NullTime: r.FinishedAt}, Rubrics: r.Rubrics, SpendLimitMicros: r.SpendLimitMicros, CostMicros: r.CostMicros, Incomplete: r.Incomplete, Rows: []WhatIfRow{}}
	rows, err := queries(db).WhatIfRows(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		w := WhatIfRow{ProjectID: row.ProjectID, ProjectSlug: row.ProjectSlug, SHA: row.Sha, AdoptionRevision: row.AdoptionRevision, CandidateDigest: row.CandidateDigest,
			Block: row.Block, Check: row.CheckName, CurrentVersion: int(row.CurrentVersion), ProposedVersion: int(row.ProposedVersion),
			Current: row.CurrentState, Proposed: row.ProposedState, Result: row.Result, Detail: row.Detail, Brief: row.Brief, Reason: NotAssessed(row.Reason), Joins: row.Joins}
		var h AttemptHistory
		if json.Unmarshal(row.HistoryJson, &h) == nil && h.Attempts > 0 {
			w.History = &h
		}
		out.Rows = append(out.Rows, w)
	}
	var selected []ids.ID
	if err := json.Unmarshal(r.Projects, &selected); err != nil {
		return nil, fmt.Errorf("what-if %d's selection: %w", r.ID, err)
	}
	answered := map[ids.ID]bool{}
	for _, row := range out.Rows {
		answered[row.ProjectID] = true
	}
	var unanswered []ids.ID
	for _, id := range selected {
		if !answered[id] {
			unanswered = append(unanswered, id)
		}
	}
	slugs := map[ids.ID]string{}
	if len(unanswered) > 0 {
		projects, err := readConformanceProjects(ctx, db, unanswered, false)
		if err != nil {
			return nil, err
		}
		for _, p := range projects {
			slugs[p.ID] = p.Slug
		}
	}
	out.Projects = WhatIfByProject(selected, out.Rows, slugs, r.State != WhatIfQueued && r.State != WhatIfRunning)
	return out, nil
}

// RepairInterruptedWhatIfs participates in the startup repair transaction:
// a what-if found queued or running when the forge starts was interrupted
// by the restart. It is settled as interrupted, never left unfinished; the
// rows it recorded stand, it is not resumed, and the job that later
// delivers a queued one finds it ended. The caller owns the commit. at is
// the forge's clock, which stamps their ends.
func RepairInterruptedWhatIfs(ctx context.Context, tx *database.Tx, at time.Time) (int64, error) {
	return queries(tx).InterruptUnfinishedWhatIfs(ctx, conformancesql.InterruptUnfinishedWhatIfsParams{Detail: "AppTown restarted before this what-if finished; projects it did not reach were not evaluated", At: at.UTC()})
}

// RubricSpend is a what-if's rubric spend so far and its limit, read
// before each project's rubric checklist is sent.
func (r *WhatIfRun) RubricSpend(ctx context.Context) (spent WhatIfSpend, limit int64, err error) {
	w, err := queries(r.service.db).WhatIfByID(ctx, r.id)
	if err != nil {
		return WhatIfSpend{}, 0, err
	}
	return WhatIfSpend{CostMicros: w.CostMicros, Incomplete: w.Incomplete}, w.SpendLimitMicros, nil
}
