package complaint

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/complaint/internal/complaintsql"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/stamp"
	"flokkit.io/forge/internal/strs"
)

// State is derived from answer text and archival, independently of Seen.
// Archived takes precedence even if the answer is subsequently changed.
type State uint8

const (
	New State = iota + 1
	Answered
	Archived
)

func (s State) String() string {
	switch s {
	case New:
		return "new"
	case Answered:
		return "answered"
	case Archived:
		return "archived"
	}
	return "invalid-complaint-state"
}

// StateOf returns Archived when archived, Answered when answer is nonempty,
// and New otherwise. It does not trim or validate the answer.
func StateOf(answer string, archived bool) State {
	switch {
	case archived:
		return Archived
	case answer != "":
		return Answered
	}
	return New
}

// Complaint is one row of complaints, with the project it names.
type Complaint struct {
	ID        ids.ID `json:"id"`
	Number    int64  `json:"number"` // within the forge, as people name it ("complaint 9")
	At        string `json:"at"`
	Source    string `json:"source"` // human | run | console
	RunID     ids.ID `json:"runId,omitzero"`
	RunNumber int64  `json:"runNumber"` // within its project; 0 when no run is named or it is gone
	Workflow  string `json:"workflow,omitempty"`
	Step      string `json:"step,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Actor     string `json:"actor,omitempty"`
	// ActorPrincipal is the filer's principal; zero when not resolved to a person.
	ActorPrincipal ids.ID `json:"actorPrincipal,omitzero"`
	Text           string `json:"text"`
	ChangeID       ids.ID `json:"changeId,omitzero"`
	ChangeNumber   int64  `json:"changeNumber"` // within its project; 0 when no change is named or it is gone
	Why            string `json:"why,omitempty"`
	Wish           string `json:"wish,omitempty"`
	Seen           bool   `json:"seen"`
	Answer         string `json:"answer,omitempty"`
	AnsweredBy     string `json:"answeredBy,omitempty"`
	// AnsweredByPrincipal is the answerer's principal; zero when not resolved to a person.
	AnsweredByPrincipal ids.ID     `json:"answeredByPrincipal,omitzero"`
	AnsweredAt          stamp.Time `json:"answeredAt,omitzero"`
	ArchivedAt          stamp.Time `json:"archivedAt,omitzero"`
	State               string     `json:"state"`
	Project             string     `json:"project,omitempty"`
	ProjectName         string     `json:"projectName,omitempty"`
	// About is what the filer said it is about: AboutForge,
	// AboutWayOfWorking or AboutProject. PackVersion names the project's
	// adopted packs when it is about the way of working.
	About       string `json:"about"`
	PackVersion string `json:"packVersion,omitempty"`
	// EvalID is the eval whose copy the complaint was filed in, from the
	// copy record; 0 for a complaint filed anywhere else.
	EvalID ids.ID `json:"evalId,omitzero"`
	// ArmID is the eval arm whose copy it was filed in, on an eval's list.
	ArmID ids.ID `json:"armId,omitzero"`
}

// What a complaint is about. The filer chooses; a complaint filed in an
// eval's copy is the eval's whatever it is about, and only one about the
// forge also reaches the forge's queue.
const (
	AboutForge        = "apptown"
	AboutWayOfWorking = "way-of-working"
	AboutProject      = "project"
)

// ValidAbout reports whether about is one of the three values.
func ValidAbout(about string) bool {
	return about == AboutForge || about == AboutWayOfWorking || about == AboutProject
}

// Entry is what a complaint is filed with. About defaults to AboutForge.
type Entry struct {
	ProjectID, RunID, ChangeID ids.ID
	Source, Workflow, Step     string
	SessionID, Actor           string
	Text, Why, Wish            string
	About                      string
}

// PackVersions names the packs and versions a project has adopted, as
// "pack@N" joined by ", ": what a way-of-working complaint is about.
type PackVersions interface {
	PackVersionsOf(ctx context.Context, projectID ids.ID) (string, error)
}

var (
	ErrText    = errors.New("a complaint needs text")
	ErrAbout   = fault.New(fault.Unprocessable, "a complaint is about AppTown, the way-of-working or the project")
	ErrAnswer  = errors.New("an answer is required")
	ErrMissing = fault.New(fault.NotFound, "no complaint")
)

// Retention is how long a complaint is kept, whatever its state.
const Retention = 90 * 24 * time.Hour

// Box records and manages complaints. Callers authorize operations and supply
// attribution; Box validates content and persists changes with required receipts.
type Box struct {
	db    *database.DB
	packs PackVersions
	clock func() time.Time
}

// NewBox builds the box; packs names a project's adopted packs for a
// way-of-working complaint, and may be nil where none are filed. now is the
// forge's clock, which stamps filing, answers, archiving and their receipts
// and sets the retention cutoffs (the real clock when nil).
func NewBox(db *database.DB, packs PackVersions, now func() time.Time) *Box {
	return &Box{db: db, packs: packs, clock: now}
}

func (b *Box) now() time.Time { return clock.Or(b.clock)().UTC() }

// File records a complaint with the supplied attribution. Text is trimmed and
// truncated to 2000 bytes; empty text returns ErrText. Why and Wish are trimmed.
// Credentials written into URLs are redacted from all three first.
// Filing does not start a workflow or create an audit receipt. About
// defaults to the forge; any value but the three is ErrAbout. A complaint
// about the way of working names the project's adopted packs, read here,
// never supplied by the filer.
func (b *Box) File(ctx context.Context, e Entry) (ids.ID, error) {
	text := strings.TrimSpace(redactCredentials(e.Text))
	if text == "" {
		return ids.Nil, ErrText
	}
	if e.About == "" {
		e.About = AboutForge
	}
	if !ValidAbout(e.About) {
		return ids.Nil, ErrAbout
	}
	var packVersion string
	if e.About == AboutWayOfWorking && !e.ProjectID.IsZero() && b.packs != nil {
		var err error
		if packVersion, err = b.packs.PackVersionsOf(ctx, e.ProjectID); err != nil {
			return ids.Nil, fmt.Errorf("reading the project's way of working: %w", err)
		}
	}
	if len(text) > 2000 {
		text = text[:2000]
	}
	return database.Transact(ctx, b.db, func(tx *database.Tx) (ids.ID, error) {
		number, err := queries(tx).NextComplaintNumber(ctx)
		if err != nil {
			return ids.Nil, err
		}
		filer, err := identity.ActorIn(ctx, tx, e.Actor)
		if err != nil {
			return ids.Nil, err
		}
		row, err := queries(tx).InsertComplaint(ctx, complaintsql.InsertComplaintParams{ID: ids.New(b.now()), Number: number,
			At: b.now(), ProjectID: e.ProjectID, Source: e.Source, RunID: e.RunID,
			Workflow: e.Workflow, Step: e.Step, SessionID: e.SessionID, Actor: e.Actor, ActorPrincipal: filer.Principal, ActorVia: filer.Via,
			Text: text, ChangeID: e.ChangeID, Why: strings.TrimSpace(redactCredentials(e.Why)), Wish: strings.TrimSpace(redactCredentials(e.Wish)),
			About: e.About, PackVersion: packVersion,
		})
		return row.ID, err
	})
}

// All lists the forge's queue newest first, limited to limit rows, including
// archived records: every complaint filed outside an eval's copies, and from
// them only those about the forge, marked with their eval. The copy record
// decides, so this holds after the copy and the eval are deleted. Project
// display names are joined when available. Errors return no rows.
func (b *Box) All(ctx context.Context, limit int) ([]Complaint, error) {
	rows, err := queries(b.db).ComplaintsWithProject(ctx, int32(limit))
	if err != nil {
		return []Complaint{}, err
	}
	out := make([]Complaint, 0, len(rows))
	for _, r := range rows {
		c := record(r.Complaint, r.ProjectSlug, r.ProjectName)
		c.EvalID, c.RunNumber, c.ChangeNumber = r.EvalID, r.RunNumber, r.ChangeNumber
		out = append(out, c)
	}
	return out, nil
}

// Answer replaces the answer and marks the complaint seen, committing both
// mutation and audit receipt together. Empty trimmed text returns ErrAnswer; a
// missing complaint returns ErrMissing. An archived complaint stays archived.
func (b *Box) Answer(ctx context.Context, id ids.ID, answer, who string) error {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return ErrAnswer
	}
	now := b.now()
	return b.db.InTx(ctx, func(tx *database.Tx) error {
		answerer, err := identity.ActorIn(ctx, tx, who)
		if err != nil {
			return err
		}
		_, err = queries(tx).AnswerComplaint(ctx, complaintsql.AnswerComplaintParams{
			ID: id, Answer: answer, AnsweredBy: who, AnsweredByPrincipal: answerer.Principal, AnsweredByVia: answerer.Via, AnsweredAt: stamp.Of(now).NullTime,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w %d", ErrMissing, id)
		}
		if err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "complaint.answered", fmt.Sprint(id), map[string]any{"answer": strs.Truncate(answer, 200)})
	})
}

// Archive marks a complaint archived and seen without changing its answer.
// Mutation and audit receipt commit together. An already archived or missing
// complaint returns ErrMissing; this operation is not an idempotent success.
func (b *Box) Archive(ctx context.Context, id ids.ID, who string) error {
	now := b.now()
	return b.db.InTx(ctx, func(tx *database.Tx) error {
		_, err := queries(tx).ArchiveComplaint(ctx, complaintsql.ArchiveComplaintParams{
			ID: id, ArchivedAt: stamp.Of(now).NullTime,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w %d", ErrMissing, id)
		}
		if err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "complaint.archived", fmt.Sprint(id), nil)
	})
}

// ArchiveOlder archives unarchived complaints filed before now minus window.
// It preserves answers and commits the mutations with one audit receipt, even
// when no rows change. The count is valid only when the returned error is nil.
func (b *Box) ArchiveOlder(ctx context.Context, window time.Duration, who string) (int64, error) {
	now := b.now()
	cutoff := now.Add(-window)
	var n int64
	err := b.db.InTx(ctx, func(tx *database.Tx) error {
		var err error
		n, err = queries(tx).ArchiveComplaintsBefore(ctx, complaintsql.ArchiveComplaintsBeforeParams{
			ArchivedAt: stamp.Of(now).NullTime, At: cutoff,
		})
		if err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "complaints.archived-older", fmt.Sprintf("%gh", window.Hours()), map[string]any{"archived": n, "cutoff": cutoff.Format(time.RFC3339)})
	})
	return n, err
}

// Delete removes a complaint for good; the ledger keeps a line saying it
// was deleted and by whom, with the start of its text. The deletion and receipt
// commit together. The current implementation returns ErrMissing for any initial
// lookup failure, including a database read error.
func (b *Box) Delete(ctx context.Context, id ids.ID, who string) error {
	now := b.now()
	return b.db.InTx(ctx, func(tx *database.Tx) error {
		row, err := queries(tx).ComplaintByID(ctx, id)
		if err != nil {
			return fmt.Errorf("%w %d", ErrMissing, id)
		}
		text := row.Text
		if _, err := queries(tx).DeleteComplaint(ctx, id); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "complaint.deleted", fmt.Sprint(id), map[string]any{"text": strs.Truncate(text, 120)})
	})
}

// MarkSeen marks everything read.
func (b *Box) MarkSeen(ctx context.Context) error {
	return queries(b.db).MarkComplaintsSeen(ctx)
}

// Expire deletes complaints older than the retention, whatever their
// state, and commits one audit receipt with each nonempty sweep. No receipt is
// written for an empty sweep. The count is valid only when the error is nil.
func (b *Box) Expire(ctx context.Context) (int64, error) {
	now := b.now()
	cutoff := now.Add(-Retention)
	var n int64
	err := b.db.InTx(ctx, func(tx *database.Tx) error {
		var err error
		if n, err = queries(tx).DeleteComplaintsBefore(ctx, cutoff); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		return audit.At(now).RecordIn(ctx, tx, "apptown-system", ids.Nil, "complaints.expired", fmt.Sprint(n), map[string]any{"olderThan": cutoff.Format(time.RFC3339)})
	})
	return n, err
}

// Complaints are pasted error output, and error output carries URLs: a
// password in a URL's user part, or a token in its query, would be kept in
// plain text for every reader of the box. Both are replaced before storing.
var (
	urlUserinfo = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^\s/@]+@`)
	urlSecret   = regexp.MustCompile(`(?i)([?&](?:[a-z_-]*token|password|passwd|secret|key|api[_-]?key|sig|signature)=)[^&#\s]+`)
)

// redactCredentials replaces a URL's user part and its credential-looking
// query values with "redacted".
func redactCredentials(s string) string {
	s = urlUserinfo.ReplaceAllString(s, "${1}redacted@")
	return urlSecret.ReplaceAllString(s, "${1}redacted")
}

// OfEval is every complaint filed in an eval's copies, whatever it is
// about, oldest first, each with the arm it was filed in (ArmID). The copy
// record decides, so the copies need not exist any more.
func (b *Box) OfEval(ctx context.Context, evalID ids.ID) ([]Complaint, error) {
	rows, err := queries(b.db).ComplaintsOfEval(ctx, evalID)
	if err != nil {
		return nil, err
	}
	out := make([]Complaint, 0, len(rows))
	for _, r := range rows {
		c := record(r.Complaint, r.ProjectSlug, r.ProjectName)
		c.EvalID, c.ArmID, c.RunNumber, c.ChangeNumber = r.EvalID, r.ArmID, r.RunNumber, r.ChangeNumber
		out = append(out, c)
	}
	return out, nil
}
