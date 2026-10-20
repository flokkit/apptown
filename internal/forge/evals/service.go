package evals

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/complaint"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/evals/internal/evalssql"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/forge/wow"
)

// Meaning is what starting an eval means, shown to the starter when they
// decide and kept on the start receipt. Starting is the one human act: the
// eval's copies land and deploy with no one approving them.
const Meaning = "I start this eval; its throwaway copies may land and deploy to the eval domain."

// MaxChanges is the most Changes one eval re-runs (the first slice).
const MaxChanges = 5

// What an eval varies: a model profile, or a pack version. One per eval.
const (
	SubjectModel = "model"
	SubjectPack  = "pack"
)

// Eval states.
const (
	StateRunning  = "running"
	StateHeld     = "held"
	StateFinished = "finished"
	StateStopped  = "stopped"
	// StateStopping: the budget is spent; running arms are being stopped,
	// and work that could not stop is still observed until it ends.
	StateStopping = "stopping"
)

// Arm states, in the order an arm moves through them.
const (
	ArmQueued = "queued"
	// ArmCopying: its copy is being made, linked to the arm. Found at the
	// start of a pass, it was interrupted.
	ArmCopying = "copying"
	// ArmCopied: its copy is made and given its models; its lane waits for
	// the eval to admit a new arm (a hold on unknown spend).
	ArmCopied = "copied"
	// ArmStarting: its lane is being opened. Found at the start of a pass,
	// it was interrupted (a Change may exist in its copy).
	ArmStarting = "starting"
	ArmWorking  = "working"
	ArmLanding  = "landing"
	ArmScored   = "scored"
	ArmFailed   = "failed"
	ArmNotRun   = "not-run"
)

var (
	ErrMeaning  = fault.New(fault.Invalid, "an eval starts only once its starter has been shown what starting means: "+Meaning)
	ErrChanges  = fault.New(fault.Invalid, fmt.Sprintf("name between 1 and %d distinct landed Changes", MaxChanges))
	ErrBudget   = fault.New(fault.Invalid, "an eval needs a budget above zero")
	ErrMissing  = fault.New(fault.NotFound, "no such eval")
	ErrNotHeld  = fault.New(fault.Conflict, "the eval is not held")
	ErrSubject  = fault.New(fault.Invalid, "name the one thing that varies: a model profile this AppTown offers, or a published pack version")
	ErrDeleting = fault.New(fault.Conflict, "the eval's copies could not all be deleted")
)

// Work is what an eval asks of Work, as its starter or as its driver in a
// copy.
type Work interface {
	Create(ctx context.Context, in workflow.ChangeDraft) (*workflow.Change, error)
	StartEvalRun(ctx context.Context, in workflow.EvalRun) (*workflow.Run, error)
	ChangeRuns(ctx context.Context, id ids.ID, in workflow.ChangeRuns) ([]*workflow.Run, error)
	Snapshot(ctx context.Context, id ids.ID, view workflow.ReadView) (*workflow.RunSnapshot, error)
	Abandon(ctx context.Context, in workflow.StopWork) (*workflow.Abandoned, error)
	Changes(ctx context.Context, project ids.ID, in workflow.ChangeList) ([]workflow.ChangeSummary, error)
}

// Copier makes a copy project (projectlifecycle.Service.CreateCopy).
type Copier interface {
	CreateCopy(ctx context.Context, in projectlifecycle.CopyDraft) (*project.Project, error)
}

// Models is the model owner's part: what an arm runs, and giving a copy
// its models.
type Models interface {
	ArmSlots(ctx context.Context, projectID ids.ID, profile string) (map[string]string, error)
	OfferedProfile(ctx context.Context, profile string) (bool, error)
	Profiles(ctx context.Context) ([]string, error)
	CopyModels(ctx context.Context, from, to ids.ID, profile string, withOverrides bool, who string) error
}

// Adoption is the adoption owner's part: what a copy adopts, the
// candidate a pack version would be for a project, and a copy's receipt.
type Adoption interface {
	CopyTarget(ctx context.Context, src ids.ID, picks []adoption.PackPick) (adoption.Receipt, []adoption.PackPick, error)
	PicksWith(ctx context.Context, projectID ids.ID, pack string, version int64) ([]adoption.PackPick, error)
	CurrentReceipt(ctx context.Context, projectID ids.ID) (*adoption.RecordedRevision, error)
}

// Comparer judges an arm's landed work against its Change's base over both
// arms' checks (verification.Fleet.CompareArm).
type Comparer interface {
	CompareArm(ctx context.Context, in verification.ArmComparison) (*verification.ArmCompared, error)
}

// Packs lists the packs a pack-version eval may vary.
type Packs interface {
	Packs(ctx context.Context) ([]wow.Pack, error)
}

// Deployments takes a copy's standing deployments down, main included, with
// no final copy or backup on any target: a copy's data is synthetic and
// nothing will restore it (dataprotection.Service.TakeDownWithProject).
type Deployments interface {
	StandingNames(ctx context.Context, projectID ids.ID) ([]string, error)
	TakeDownWithProject(ctx context.Context, projectID ids.ID, name string) error
}

// Deleter deletes a copy project: marked deleting at once, its deployments
// taken down, then purged with no kept period (projectlifecycle.Deletion).
type Deleter interface {
	RequestCopy(ctx context.Context, projectID ids.ID, who string) (projectlifecycle.Status, error)
}

type Dependencies struct {
	Authority  access.Authorizer
	Projects   *project.Projects
	Work       Work
	Copier     Copier
	Models     Models
	Spend      Spend
	Complaints interface {
		OfEval(context.Context, ids.ID) ([]complaint.Complaint, error)
	}
	Deployments Deployments
	Deleter     Deleter
	Adoption    Adoption
	Comparer    Comparer
	Packs       Packs
	// DataDir is where projects' repositories live: an arm's diff is read
	// from its copy's.
	DataDir string
	// InCopy is the eval's driver acting in one of its copies: an eval
	// instrument bound to that copy (access's "eval" instrument).
	InCopy func(ctx context.Context, evalID, projectID ids.ID) context.Context
	// Now is AppTown's clock, which stamps evals, arms and their
	// receipts (the real clock when nil).
	Now func() time.Time
	// RequestPass asks the job queue for a pass now (PassJob); nil leaves
	// the work to the scheduled pass.
	RequestPass func(context.Context) error
}

// Service starts evals, drives their arms and reads their results. It owns
// the evals, their arms and the copy record.
type Service struct {
	db   *database.DB
	deps Dependencies
	gate *Gate
}

func New(db *database.DB, deps Dependencies) *Service {
	return &Service{db: db, deps: deps, gate: NewGate(db, deps.Spend)}
}

func (s *Service) now() time.Time { return clock.Or(s.deps.Now)().UTC() }

// Gate is the eval's one "may this proceed?" rule (gate.go).
func (s *Service) Gate() *Gate { return s.gate }

// Start is the request a starter makes: one of Profile, or Pack at
// PackVersion, varies.
type Start struct {
	Changes      []ids.ID
	Profile      string
	Pack         string
	PackVersion  int64
	BudgetMicros int64
	// Shown is the meaning the starter was shown; it must be Meaning.
	Shown string
}

// Eval is an eval and its arms as the starter reads them.
type Eval struct {
	ID          ids.ID    `json:"id"`
	Number      int64     `json:"number"` // within AppTown, as people name it ("eval 9")
	StartedBy   string    `json:"startedBy"`
	StartedAt   time.Time `json:"startedAt"`
	Meaning     string    `json:"meaning"`
	SubjectKind string    `json:"subjectKind"`
	Subject     string    `json:"subject"`
	// SubjectDigest names exactly the pack version tested: its resolved
	// spec and pins at start.
	SubjectDigest string     `json:"subjectDigest,omitempty"`
	BudgetMicros  int64      `json:"budgetMicros"`
	State         string     `json:"state"`
	Reason        string     `json:"reason,omitempty"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	Arms          []Arm      `json:"arms"`
}

// Arm is one Change re-run under one setup.
type Arm struct {
	ID              ids.ID `json:"id"`
	SourceProjectID ids.ID `json:"sourceProjectId"`
	SourceChangeID  ids.ID `json:"sourceChangeId"`
	// SourceChangeNumber and ChangeNumber name the source change and the
	// arm's change within their projects; 0 when none or gone.
	SourceChangeNumber int64             `json:"sourceChangeNumber"`
	Arm                string            `json:"arm"`
	Profile            string            `json:"profile"`
	Models             map[string]string `json:"models"`
	State              string            `json:"state"`
	Reason             string            `json:"reason,omitempty"`
	ProjectID          ids.ID            `json:"projectId,omitzero"`
	ChangeID           ids.ID            `json:"changeId,omitzero"`
	ChangeNumber       int64             `json:"changeNumber"`
	RunID              ids.ID            `json:"runId,omitzero"`
	LandingRunID       ids.ID            `json:"landingRunId,omitzero"`
	StartedAt          *time.Time        `json:"startedAt,omitempty"`
	ReadyAt            *time.Time        `json:"readyAt,omitempty"`
	FinishedAt         *time.Time        `json:"finishedAt,omitempty"`
	Score              Score             `json:"score"`
	// Declares is the block versions the arm's copy puts in force, as fixed
	// at start: a check only one arm declares is marked so.
	Declares []conformance.CheckVersion `json:"declares"`
}

func (a Arm) terminal() bool {
	return a.State == ArmScored || a.State == ArmFailed || a.State == ArmNotRun
}
func (a Arm) running() bool { return a.State == ArmWorking || a.State == ArmLanding }

// busy: the arm holds one of the eval's MaxRunning places.
func (a Arm) busy() bool { return a.running() || a.State == ArmCopied }

// Start admits an eval: the starter manages packs, may read every source
// project, was shown what starting means, and names landed, repository-
// backed Changes and an offered model profile. Two arms are recorded per
// Change, each with the models it runs; the start receipt keeps the
// meaning. Nothing is copied yet: the driver makes the copies.
func (s *Service) Start(ctx context.Context, in Start) (*Eval, error) {
	caller, err := s.deps.Authority.Require(ctx, access.Permission{Capability: access.CapStandardsPacks, Resource: access.Resource{Type: "instance"}, Context: access.Context{Route: "evals"}})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Shown) != Meaning {
		return nil, ErrMeaning
	}
	if in.BudgetMicros <= 0 {
		return nil, ErrBudget
	}
	idList := slices.Clone(in.Changes)
	slices.SortFunc(idList, ids.ID.Compare)
	if len(idList) == 0 || len(idList) > MaxChanges || len(slices.Compact(idList)) != len(in.Changes) {
		return nil, ErrChanges
	}
	kind, subject, digest, err := s.subject(ctx, in)
	if err != nil {
		return nil, err
	}
	type source struct {
		change   *workflow.Change
		lane     string
		baseline armAdoption
		variant  armAdoption
	}
	var sources []source
	for _, id := range in.Changes {
		ch, err := workflow.ReadChangeIn(ctx, s.db, id)
		if found, err := database.Lookup(err); err != nil {
			return nil, err
		} else if !found {
			return nil, fault.New(fault.NotFound, fmt.Sprintf("no change %s", id))
		}
		if _, err := s.deps.Authority.Require(ctx, access.Permission{Capability: access.CapProjectRead, Resource: access.Resource{Type: "project", ID: ch.ProjectID.String(), ProjectID: ch.ProjectID}, Context: access.Context{Route: "evals"}}); err != nil {
			return nil, err
		}
		p, err := s.deps.Projects.ByID(ctx, ch.ProjectID)
		if err != nil {
			return nil, err
		}
		name := fmt.Sprintf("change %s#%d", p.Slug, ch.Number) // as people name it in refusals
		if copy, _, err := copyOfIn(ctx, s.db, ch.ProjectID); err != nil {
			return nil, err
		} else if !copy.EvalID.IsZero() {
			return nil, fault.New(fault.Invalid, fmt.Sprintf("%s is itself in an eval's copy", name))
		}
		if ch.State != workflow.ChangeShipped || ch.BaseSHA == "" || strings.TrimSpace(ch.Title+ch.Body) == "" {
			return nil, fault.New(fault.Invalid, fmt.Sprintf("%s is not a landed change with a request and a base commit", name))
		}
		if p.DefaultBranch == "" {
			return nil, fault.New(fault.Unprocessable, fmt.Sprintf("%s's project is not repo-backed", name))
		}
		lane, err := s.originalLane(ctx, ch.ID, name)
		if err != nil {
			return nil, err
		}
		baseline, err := s.adoptionOf(ctx, ch, name, lane, nil)
		if err != nil {
			return nil, err
		}
		variant := baseline
		if kind == SubjectPack {
			picks, err := s.deps.Adoption.PicksWith(ctx, ch.ProjectID, in.Pack, in.PackVersion)
			if err != nil {
				return nil, err
			}
			if variant, err = s.adoptionOf(ctx, ch, name, lane, picks); err != nil {
				return nil, err
			}
		}
		sources = append(sources, source{ch, lane, baseline, variant})
	}
	who := caller.Principal.Name
	var out *Eval
	now := s.now()
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		q := queries(tx)
		number, err := q.NextEvalNumber(ctx)
		if err != nil {
			return err
		}
		starter, err := identity.ActorIn(ctx, tx, who)
		if err != nil {
			return err
		}
		e, err := q.InsertEval(ctx, evalssql.InsertEvalParams{ID: ids.New(now), Number: number, StartedBy: who, StartedByPrincipal: starter.Principal, StartedByVia: starter.Via, StartedAt: now, Meaning: Meaning, SubjectKind: kind, Subject: subject, BudgetMicros: in.BudgetMicros, SubjectDigest: digest})
		if err != nil {
			return err
		}
		for _, src := range sources {
			// The source holds still while its arms are recorded: a
			// deletion waits for this transaction, then counts them as
			// live work (LiveEvalsFromIn), or this one sees it and refuses.
			if err := project.RefuseUnlessActiveIn(ctx, tx, src.change.ProjectID); err != nil {
				return err
			}
			p, err := project.ByIDIn(ctx, tx, src.change.ProjectID)
			if err != nil {
				return err
			}
			// The judge of both arms' rubrics is the baseline's fast model,
			// so the judge is not what varies.
			baselineSlots, err := s.deps.Models.ArmSlots(ctx, src.change.ProjectID, profileOf(p.ModelProfile))
			if err != nil {
				return err
			}
			for _, arm := range []string{ArmBaseline, ArmVariant} {
				// A pack-version eval holds the model: both arms run the
				// baseline's, overrides included.
				profile, from, adopted := profileOf(p.ModelProfile), src.change.ProjectID, src.baseline
				if arm == ArmVariant {
					adopted = src.variant
					if kind == SubjectModel {
						profile, from = in.Profile, ids.Nil
					}
				}
				slots, err := s.deps.Models.ArmSlots(ctx, from, profile)
				if err != nil {
					return err
				}
				raw, _ := json.Marshal(armPlan{Slots: slots, Lane: src.lane, Adoption: &adopted, Judge: baselineSlots["fast"],
					Union: unionBlocks(src.baseline.Blocks, src.variant.Blocks)})
				if _, err := q.InsertArm(ctx, evalssql.InsertArmParams{ID: ids.New(now), EvalID: e.ID, SourceProjectID: src.change.ProjectID, SourceChangeID: src.change.ID, Arm: arm, Profile: profile, ModelsJson: raw}); err != nil {
					return err
				}
			}
		}
		if err := audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "eval.started", e.ID.String(), map[string]any{
			"meaning": Meaning, "subject": subject, "subjectKind": kind, "subjectDigest": digest, "changes": in.Changes, "budgetMicros": in.BudgetMicros}); err != nil {
			return err
		}
		out, err = evalIn(ctx, tx, e.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.requestPass(ctx)
	return out, nil
}

// armPlan is what an arm was started with: the slots it runs, the lane
// that re-runs its Change, the adoption its copy takes, the checks both of
// its Change's arms declare, and the one model that judges both arms'
// rubrics when they are compared. All are fixed at start.
type armPlan struct {
	Slots    map[string]string          `json:"slots"`
	Lane     string                     `json:"lane"`
	Adoption *armAdoption               `json:"adoption,omitempty"`
	Union    []conformance.CheckVersion `json:"union,omitempty"`
	Judge    string                     `json:"judge,omitempty"`
}

// armAdoption is what an arm's copy adopts: the pack picks (none: the
// source's adoption as it was), the receipt's digest the copy must have,
// and the block versions it puts in force.
type armAdoption struct {
	Picks  []adoption.PackPick        `json:"picks,omitempty"`
	Digest string                     `json:"digest"`
	Blocks []conformance.CheckVersion `json:"blocks"`
}

// subject is what an eval varies, said once: a model profile this AppTown
// offers, or a published pack version with the digest of its resolved
// spec and pins.
func (s *Service) subject(ctx context.Context, in Start) (kind, subject, digest string, err error) {
	switch {
	case in.Profile != "" && in.Pack == "":
		offered, err := s.deps.Models.OfferedProfile(ctx, in.Profile)
		if err != nil {
			return "", "", "", err
		}
		if !offered {
			return "", "", "", ErrSubject
		}
		return SubjectModel, in.Profile, "", nil
	case in.Pack != "" && in.Profile == "" && in.PackVersion > 0:
		pv, err := wow.PackVersionIn(ctx, s.db, in.Pack, in.PackVersion)
		if err != nil {
			return "", "", "", fmt.Errorf("%w: %w", ErrSubject, err)
		}
		sum := sha256.Sum256([]byte(pv.SpecJSON + "\x00" + pv.PinsJSON))
		return SubjectPack, fmt.Sprintf("%s@%d", in.Pack, in.PackVersion), hex.EncodeToString(sum[:8]), nil
	}
	return "", "", "", ErrSubject
}

// adoptionOf is what a copy of the Change's project adopts with these
// picks (nil: as it is now), fixed at start. A version the project could
// not adopt, or one without the lane the Change was worked in, refuses
// that Change here rather than leaving it to fail as an arm.
func (s *Service) adoptionOf(ctx context.Context, ch *workflow.Change, name, lane string, picks []adoption.PackPick) (armAdoption, error) {
	target, _, err := s.deps.Adoption.CopyTarget(ctx, ch.ProjectID, picks)
	if err != nil {
		return armAdoption{}, fault.New(fault.Invalid, fmt.Sprintf("%s's project cannot adopt this: %v", name, err))
	}
	if !slices.ContainsFunc(target.Workflows, func(w adoption.ReceiptWorkflow) bool { return w.Name == lane }) {
		return armAdoption{}, fault.New(fault.Invalid, fmt.Sprintf("%s was worked in the %s lane, which this would not have", name, lane))
	}
	out := armAdoption{Picks: picks, Digest: adoption.Digest(target), Blocks: []conformance.CheckVersion{}}
	for _, b := range target.Guidance {
		if !b.Disabled {
			out.Blocks = append(out.Blocks, conformance.CheckVersion{Block: b.Name, Version: b.Version})
		}
	}
	return out, nil
}

// unionBlocks is every block version either arm puts in force.
func unionBlocks(sides ...[]conformance.CheckVersion) []conformance.CheckVersion {
	out := []conformance.CheckVersion{}
	for _, side := range sides {
		for _, b := range side {
			if !slices.Contains(out, b) {
				out = append(out, b)
			}
		}
	}
	return out
}

func profileOf(p string) string {
	if p == "" {
		return "standard"
	}
	return p
}

// originalLane is the workflow the Change was worked under: its first run
// that was not a landing.
func (s *Service) originalLane(ctx context.Context, changeID ids.ID, name string) (string, error) {
	runs, err := s.deps.Work.ChangeRuns(ctx, changeID, workflow.ChangeRuns{RootsOnly: true})
	if err != nil {
		return "", err
	}
	lane := ""
	for _, r := range runs { // newest first
		spec, err := r.Definition()
		if err != nil || spec == nil || spec.Role == "landing" {
			continue
		}
		lane = r.WorkflowName
	}
	if lane == "" {
		return "", fault.New(fault.Invalid, fmt.Sprintf("%s has no working run to re-run", name))
	}
	return lane, nil
}

// requireRead is who reads evals: a person holding standards:read-evals
// on the instance (standards administrators and auditors), or their own
// command line or agent token. It is a read, so an agent may read through
// its person, which standards:manage-packs, an edit, never allows. An
// instrument (a step's or a chat's credential, bound to one project) never
// reads evals: an eval spans projects, and a read capability alone would
// admit any instrument (E2B-01).
func (s *Service) requireRead(ctx context.Context) error {
	caller, err := s.deps.Authority.Require(ctx, access.Permission{Capability: access.CapStandardsEvals, Resource: access.Resource{Type: "instance"}, Context: access.Context{Route: "evals"}})
	if err != nil {
		return err
	}
	if caller.Principal.Kind != "user" {
		return &access.Denied{Code: access.DenyOutsideScope, Reason: "evals are read by people and their own agents, not by a project's steps or chats"}
	}
	return nil
}

// Get reads an eval for someone who may read evals.
func (s *Service) Get(ctx context.Context, id ids.ID) (*Eval, error) {
	if err := s.requireRead(ctx); err != nil {
		return nil, err
	}
	return evalIn(ctx, s.db, id)
}

// List reads the newest evals for someone who may read evals.
func (s *Service) List(ctx context.Context) ([]Eval, error) {
	if err := s.requireRead(ctx); err != nil {
		return nil, err
	}
	rows, err := queries(s.db).Evals(ctx, 50)
	out := make([]Eval, 0, len(rows))
	for _, r := range rows {
		out = append(out, evalOf(r))
	}
	return out, err
}

func evalIn(ctx context.Context, q database.Queryer, id ids.ID) (*Eval, error) {
	r, err := queries(q).EvalByID(ctx, id)
	if found, err := database.Lookup(err); err != nil {
		return nil, err
	} else if !found || r.DeletedAt.Valid {
		return nil, ErrMissing
	}
	e := evalOf(r)
	arms, err := queries(q).ArmsOfEval(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, a := range arms {
		e.Arms = append(e.Arms, armOf(a.EvalArm, a.SourceChangeNumber, a.ChangeNumber))
	}
	return &e, nil
}

func evalOf(r evalssql.Eval) Eval {
	e := Eval{ID: r.ID, Number: r.Number, StartedBy: r.StartedBy, StartedAt: r.StartedAt, Meaning: r.Meaning, SubjectKind: r.SubjectKind, Subject: r.Subject, SubjectDigest: r.SubjectDigest,
		BudgetMicros: r.BudgetMicros, State: r.State, Reason: r.Reason, Arms: []Arm{}}
	if r.FinishedAt.Valid {
		t := r.FinishedAt.Time
		e.FinishedAt = &t
	}
	return e
}

func armOf(r evalssql.EvalArm, sourceChangeNumber, changeNumber int64) Arm {
	a := Arm{ID: r.ID, SourceProjectID: r.SourceProjectID, SourceChangeID: r.SourceChangeID, SourceChangeNumber: sourceChangeNumber, ChangeNumber: changeNumber, Arm: r.Arm, Profile: r.Profile, State: r.State, Reason: r.Reason,
		ProjectID: r.ProjectID, ChangeID: r.ChangeID, RunID: r.RunID, LandingRunID: r.LandingRunID}
	var plan armPlan
	_ = json.Unmarshal(r.ModelsJson, &plan)
	a.Models = plan.Slots
	a.Declares = []conformance.CheckVersion{}
	if plan.Adoption != nil {
		a.Declares = plan.Adoption.Blocks
	}
	// A stored score that cannot be read is no reading: the arm is left out
	// of better and worse, saying why, never shown as an empty or known result.
	if err := json.Unmarshal(r.ScoreJson, &a.Score); err != nil {
		a.Score = Score{NotComparable: true, Unreadable: "its stored score could not be read: " + err.Error()}
	}
	if a.Score.Checks == nil {
		a.Score.Checks = []CheckResult{} // not scored yet: none, never null
	}
	for _, t := range []struct {
		from sql.NullTime
		to   **time.Time
	}{{r.StartedAt, &a.StartedAt}, {r.ReadyAt, &a.ReadyAt}, {r.FinishedAt, &a.FinishedAt}} {
		if t.from.Valid {
			v := t.from.Time
			*t.to = &v
		}
	}
	return a
}

// Continue lets a held eval start its next arm. Only the starter who is
// still a standards manager answers for unknown spend; the incomplete
// observations so far are acknowledged, and a later one holds it again.
// The cost stays "at least". What is acknowledged is counted only after
// the copies' expired pending spend lines are locked in as unknown, in the
// same transaction (usage.Ledger.LockInUnknownIn): the count then covers
// only lines that can never change, so a call that later settles complete
// cannot leave room for a different unknown charge to go unacknowledged.
func (s *Service) Continue(ctx context.Context, id ids.ID) (*Eval, error) {
	caller, err := s.deps.Authority.Require(ctx, access.Permission{Capability: access.CapStandardsPacks, Resource: access.Resource{Type: "instance"}, Context: access.Context{Route: "evals"}})
	if err != nil {
		return nil, err
	}
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		e, err := queries(tx).EvalForUpdate(ctx, id)
		if found, err := database.Lookup(err); err != nil {
			return err
		} else if !found || e.DeletedAt.Valid {
			return ErrMissing
		}
		if !sameStarter(ctx, tx, e, caller.Principal) {
			return &access.Denied{Code: access.DenyOutsideScope, Reason: "only the person who started this eval answers for its unknown spend"}
		}
		if e.State != StateHeld {
			return ErrNotHeld
		}
		copies, err := queries(tx).CopiesOfEval(ctx, id)
		if err != nil {
			return err
		}
		seen, err := s.deps.Spend.LockInUnknownIn(ctx, tx, copies...)
		if err != nil {
			return err
		}
		if _, err := queries(tx).ContinueEval(ctx, evalssql.ContinueEvalParams{ID: id, IncompleteSeen: seen}); err != nil {
			return err
		}
		return audit.At(s.now()).RecordIn(ctx, tx, caller.Principal.Name, ids.Nil, "eval.continued", id.String(), map[string]any{"incomplete": seen})
	})
	if err != nil {
		return nil, err
	}
	s.requestPass(ctx)
	return evalIn(ctx, s.db, id)
}

// mayDelete is who may delete an eval, asked at the deletion itself: an
// instance administrator, or the person who started it while they still
// manage packs. A name alone is not permission; current grants decide.
func (s *Service) mayDelete(ctx context.Context, e evalssql.Eval) (string, error) {
	caller, err := s.deps.Authority.Require(ctx, access.Permission{Capability: access.CapIdentityAuth, Resource: access.Resource{Type: "instance"}, Context: access.Context{Route: "evals"}})
	if err == nil {
		return caller.Principal.Name, nil
	}
	var denied *access.Denied
	if !errors.As(err, &denied) {
		return "", err
	}
	caller, err = s.deps.Authority.Require(ctx, access.Permission{Capability: access.CapStandardsPacks, Resource: access.Resource{Type: "instance"}, Context: access.Context{Route: "evals"}})
	if err != nil {
		return "", err
	}
	if caller.Principal.Kind != "user" || e.StartedBy == "" || !sameStarter(ctx, s.db, e, caller.Principal) {
		return "", &access.Denied{Code: access.DenyOutsideScope, Reason: "an eval is deleted by the person who started it, or by an administrator"}
	}
	return caller.Principal.Name, nil
}

// Delete deletes an eval: its running arms are abandoned, its copies'
// deployments taken down (no final backup: synthetic data) and its copy
// projects deleted through ordinary project deletion. The copy record
// stays, so its complaints and spend stay attributed. The receipt names
// who deleted it. A copy that cannot be deleted leaves the eval marked
// deleted and is reported; deleting again retries it.
func (s *Service) Delete(ctx context.Context, id ids.ID) error {
	e, err := queries(s.db).EvalByID(ctx, id)
	found, err := database.Lookup(err)
	if err != nil {
		return err
	}
	// Who may delete is asked first: nobody learns whether an eval exists
	// from a refusal.
	who, err := s.mayDelete(ctx, e)
	if err != nil {
		return err
	}
	if !found {
		return ErrMissing
	}
	now := s.now()
	if err := s.db.InTx(ctx, func(tx *database.Tx) error {
		if _, err := queries(tx).MarkEvalDeleted(ctx, evalssql.MarkEvalDeletedParams{ID: id, DeletedAt: sql.NullTime{Time: now, Valid: true}}); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "eval.deleted", id.String(), map[string]any{"startedBy": e.StartedBy})
	}); err != nil {
		return err
	}
	idList, err := queries(s.db).CopiesOfEval(ctx, id)
	if err != nil {
		return err
	}
	var failed []string
	for _, pid := range idList {
		if err := s.deleteCopy(ctx, id, pid, who); err != nil {
			failed = append(failed, fmt.Sprintf("project %d: %v", pid, err))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%w: %s", ErrDeleting, strings.Join(failed, "; "))
	}
	return nil
}

func (s *Service) deleteCopy(ctx context.Context, evalID, projectID ids.ID, who string) error {
	p, err := s.deps.Projects.ByID(ctx, projectID)
	if found, err := database.Lookup(err); err != nil {
		return err
	} else if !found {
		return nil // already deleted
	}
	in := s.deps.InCopy(ctx, evalID, projectID)
	// Every open Change of the copy, the arm's or one a driver admitted
	// before the deletion was recorded.
	changes, err := s.deps.Work.Changes(in, projectID, workflow.ChangeList{})
	if err != nil {
		return err
	}
	for _, ch := range changes {
		if !ch.State.Closed() {
			if _, err := s.deps.Work.Abandon(in, workflow.StopWork{ChangeID: ch.ID, Reason: fmt.Sprintf("eval %d deleted", evalID)}); err != nil {
				return fmt.Errorf("abandoning its change %s#%d: %w", p.Slug, ch.Number, err)
			}
		}
	}
	_, err = s.deps.Deleter.RequestCopy(in, p.ID, who)
	return err
}

// StartForm is what a starter is shown before starting: the meaning they
// agree to, and the profiles an eval may vary to.
type StartForm struct {
	Meaning    string       `json:"meaning"`
	Profiles   []string     `json:"profiles"`
	Packs      []PackChoice `json:"packs"`
	MaxChanges int          `json:"maxChanges"`
}

// PackChoice is a pack a pack-version eval may vary: any of its versions up
// to its latest, the recommended one being what projects are offered now.
type PackChoice struct {
	Name        string `json:"name"`
	Latest      int64  `json:"latest"`
	Recommended int64  `json:"recommended"`
}

// StartForm reads the start form for someone who manages packs.
func (s *Service) StartForm(ctx context.Context) (StartForm, error) {
	if _, err := s.deps.Authority.Require(ctx, access.Permission{Capability: access.CapStandardsPacks, Resource: access.Resource{Type: "instance"}, Context: access.Context{Route: "evals"}}); err != nil {
		return StartForm{}, err
	}
	profiles, err := s.deps.Models.Profiles(ctx)
	if err != nil {
		return StartForm{}, err
	}
	form := StartForm{Meaning: Meaning, Profiles: profiles, Packs: []PackChoice{}, MaxChanges: MaxChanges}
	if s.deps.Packs != nil {
		packs, err := s.deps.Packs.Packs(ctx)
		if err != nil {
			return StartForm{}, err
		}
		for _, p := range packs {
			if !p.Platform && p.LatestVersion > 0 {
				form.Packs = append(form.Packs, PackChoice{Name: p.Name, Latest: p.LatestVersion, Recommended: p.Version})
			}
		}
	}
	return form, nil
}

// LatestOf reads the newest eval of a pack version (pack@N) or a model
// profile for someone who may read evals; nil when none ran.
func (s *Service) LatestOf(ctx context.Context, kind, subject string) (*Scorecard, error) {
	if err := s.requireRead(ctx); err != nil {
		return nil, err
	}
	r, err := queries(s.db).LatestEvalOf(ctx, evalssql.LatestEvalOfParams{SubjectKind: kind, Subject: subject})
	if found, err := database.Lookup(err); err != nil || !found {
		return nil, err
	}
	return s.Scorecard(ctx, r.ID)
}

// ArmDiff is what an arm's work changed: its copy's main against the
// Change's base, read for someone who reads the scorecard (copies have no
// members) and may read the source project, whose code it is. The arm is
// found within the eval named, never by its id alone.
func (s *Service) ArmDiff(ctx context.Context, evalID, armID ids.ID) (string, error) {
	e, err := s.Get(ctx, evalID)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(e.Arms, func(a Arm) bool { return a.ID == armID })
	if i < 0 {
		return "", fault.New(fault.NotFound, fmt.Sprintf("eval %d has no arm %d", evalID, armID))
	}
	a := e.Arms[i]
	if _, err := s.deps.Authority.Require(ctx, access.Permission{Capability: access.CapProjectRead, Resource: access.Resource{Type: "project", ID: a.SourceProjectID.String(), ProjectID: a.SourceProjectID}, Context: access.Context{Route: "evals"}}); err != nil {
		return "", err
	}
	if a.ProjectID.IsZero() {
		return "", fault.New(fault.NotFound, "this arm has no copy: it never started")
	}
	p, err := s.deps.Projects.ByID(ctx, a.ProjectID)
	if found, err := database.Lookup(err); err != nil {
		return "", err
	} else if !found {
		return "", fault.New(fault.NotFound, "this arm's copy was deleted, and its work with it")
	}
	source, err := workflow.ReadChangeIn(ctx, s.db, a.SourceChangeID)
	if err != nil {
		return "", err
	}
	repo := filepath.Join(s.deps.DataDir, "workspaces", "project-"+p.Slug)
	cmp, err := scm.ReadCommittedDiff(ctx, repo, source.BaseSHA, p.DefaultBranch)
	if err != nil || cmp.Empty {
		return "", err
	}
	diff, err := cmp.Patch(ctx, repo)
	if len(diff) > 512*1024 {
		diff = diff[:512*1024] + "\n... (truncated)"
	}
	return diff, err
}

// sameStarter is whether the caller is the person who started the eval: by
// principal when the eval recorded one, otherwise by name.
func sameStarter(ctx context.Context, q database.Queryer, e evalssql.Eval, pr access.Principal) bool {
	if e.StartedByPrincipal.IsZero() {
		return e.StartedBy != "" && e.StartedBy == pr.Name
	}
	caller := pr.ID
	if caller.IsZero() {
		resolved, err := identity.ActorIn(ctx, q, pr.Name)
		if err != nil {
			return false
		}
		caller = resolved.Principal
	}
	return caller == e.StartedByPrincipal
}
