package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/strs"
)

// ChangeAdds is what an approval's commit brings into the app that the
// commit it starts from does not have, read only from declarations
// (design/architecture/work-landing-acceptance.md). Work owns its shape because
// a vote records it: the record shows what the approver was shown. Rows
// come from the AddsReader; Work adds the words and the digest, so a reader
// cannot word a list one way and record it another.
type ChangeAdds struct {
	// Base is where the change's commit and main meet; empty when main has
	// no commit yet. Head is the commit the approval assents to.
	Base string    `json:"base"`
	Head string    `json:"head"`
	Rows []AddsRow `json:"rows"`
	// Digest names exactly this list (base, head and rows). A vote sends
	// the digest it showed; a different one is refused.
	Digest string `json:"digest"`
	// ReadFrom names the rows that were read ("secrets, services and app
	// parts"); empty when none could be.
	ReadFrom string `json:"readFrom"`
	// NothingNew is the line shown when the rows read added nothing, naming
	// them ("Nothing new in secrets, services or app parts"); empty when
	// something is added or nothing could be read. Never a bare "Nothing new".
	NothingNew string `json:"nothingNew"`
	// Lines is the list as shown, in order.
	Lines []AddsLine `json:"lines"`
}

// AddsRow is one kind of declaration compared at two commits. Unknown says
// why it could not be read; a row that could not be read lists nothing and
// is never counted as adding nothing.
type AddsRow struct {
	Name    string   `json:"name"`
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Unknown string   `json:"unknown,omitempty"`
}

// AddsLine is one line of the section: what is added, what is no longer
// needed, or a row that could not be read.
type AddsLine struct {
	Kind string `json:"kind"` // added | removed | unknown
	Text string `json:"text"`
}

// AddsQuestion is what the reader is asked for one approval: the commit
// assented to, where to find it and main, and an uplift's recorded
// adoption target (nil for any other change).
type AddsQuestion struct {
	ProjectID, ChangeID ids.ID
	Head                string
	DefaultBranch       string
	// Repositories are searched in order for a commit: the change's
	// folder first, then the project's hub.
	Repositories []string
	Hub          string
	Uplift       json.RawMessage
}

// AddsReader compares the declarations at an approval's commit with those
// where it meets main. It answers every row, unknown where it could not
// read; an error is for a question it could not ask at all.
type AddsReader interface {
	ChangeAdds(ctx context.Context, q AddsQuestion) (ChangeAdds, error)
}

var (
	// ErrAddsNotShown refuses an approving vote that does not say which
	// list of what the change adds it was shown.
	ErrAddsNotShown error = addsRefusal{fault.Invalid, "adds-not-shown", "read what this change adds before approving it; the approval records the list you were shown"}
	// ErrAddsChanged refuses an approving vote whose list is no longer what
	// the change adds. It shares the question-changed code: the client
	// reads the question again and shows the new list.
	ErrAddsChanged error = addsRefusal{fault.Conflict, "question-changed", "what this change adds has changed since it was shown; read it again and decide"}
)

type addsRefusal struct {
	kind fault.Kind
	code string
	text string
}

func (e addsRefusal) Error() string         { return e.text }
func (e addsRefusal) FaultKind() fault.Kind { return e.kind }
func (e addsRefusal) FaultCode() string     { return e.code }

// finish gives the list its digest and words. Rows keep the reader's order.
func (a ChangeAdds) finish() (ChangeAdds, error) {
	for i := range a.Rows {
		r := &a.Rows[i]
		if r.Added == nil {
			r.Added = []string{}
		}
		if r.Removed == nil {
			r.Removed = []string{}
		}
		if r.Unknown != "" {
			// What could not be read lists nothing.
			r.Added, r.Removed = []string{}, []string{}
		}
	}
	body, err := json.Marshal(struct {
		Base string    `json:"base"`
		Head string    `json:"head"`
		Rows []AddsRow `json:"rows"`
	}{a.Base, a.Head, a.Rows})
	if err != nil {
		return ChangeAdds{}, err
	}
	sum := sha256.Sum256(body)
	a.Digest = hex.EncodeToString(sum[:])
	var read []string
	added := false
	a.Lines = []AddsLine{}
	for _, r := range a.Rows {
		if r.Unknown != "" {
			continue
		}
		read = append(read, r.Name)
		if len(r.Added) > 0 {
			added = true
			a.Lines = append(a.Lines, AddsLine{"added", fmt.Sprintf("%s: %s", capital(r.Name), strings.Join(r.Added, ", "))})
		}
	}
	for _, r := range a.Rows {
		if r.Unknown == "" && len(r.Removed) > 0 {
			a.Lines = append(a.Lines, AddsLine{"removed", fmt.Sprintf("%s removed: %s", capital(r.Name), strings.Join(r.Removed, ", "))})
		}
	}
	for _, r := range a.Rows {
		if r.Unknown != "" {
			a.Lines = append(a.Lines, AddsLine{"unknown", fmt.Sprintf("%s: unknown, %s", capital(r.Name), r.Unknown)})
		}
	}
	a.ReadFrom = list(read, "and")
	a.NothingNew = ""
	if len(read) > 0 && !added {
		a.NothingNew = "Nothing new in " + list(read, "or")
	}
	return a, nil
}

// sameRows reports whether two lists name the same additions, removals and
// unknowns, whatever commit they start from.
func (a ChangeAdds) sameRows(b ChangeAdds) bool {
	return slices.EqualFunc(a.Rows, b.Rows, func(x, y AddsRow) bool {
		return x.Name == y.Name && x.Unknown == y.Unknown && slices.Equal(x.Added, y.Added) && slices.Equal(x.Removed, y.Removed)
	})
}

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func list(items []string, last string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + last + " " + items[len(items)-1]
}

// approvalAdds answers, for an approval that assents to a commit, what that
// commit adds. Approvals that assent to no commit (a plan approval part-way
// through a run) have no list.
type approvalAdds struct {
	reader AddsReader
	pins   landingPins
}

// forStep is the list for step's approval as it stands, or nil when the
// approval assents to no commit or no reader is configured.
func (a approvalAdds) forStep(ctx context.Context, run *Run, step *Step) (*ChangeAdds, error) {
	if a.reader == nil || a.pins.db == nil || run == nil || step == nil || step.Type != "approval" {
		return nil, nil
	}
	head, err := a.head(ctx, run, step)
	if err != nil || head == "" {
		return nil, err
	}
	ref := RunRef{ID: run.ID, ProjectID: run.ProjectID}
	source, err := ReadSourceIn(ctx, a.pins.db, ref)
	if err != nil {
		return nil, err
	}
	q := AddsQuestion{ProjectID: run.ProjectID, ChangeID: source.ChangeID, Head: head, DefaultBranch: source.DefaultBranch, Hub: source.Repository(a.pins.dataDir)}
	q.Repositories = []string{source.Workspace(a.pins.dataDir), q.Hub}
	if !source.ChangeID.IsZero() {
		change, err := readChange(ctx, a.pins.db, source.ChangeID)
		if err != nil {
			return nil, err
		}
		if change.Type == "uplift" {
			q.Uplift = json.RawMessage(change.TargetCandidateJSON)
		}
	}
	adds, err := a.reader.ChangeAdds(ctx, q)
	if err != nil {
		return nil, err
	}
	if adds.Head != head {
		return nil, fmt.Errorf("what the change adds was read for %s, not the approval's commit %s", strs.ShortSHA(adds.Head), strs.ShortSHA(head))
	}
	adds, err = adds.finish()
	if err != nil {
		return nil, err
	}
	return &adds, nil
}

// head is the commit step's approval assents to: a landing's pinned
// candidate, or for a change's acceptance the branch it accepts
// (acceptedTip). Empty for any other approval.
func (a approvalAdds) head(ctx context.Context, run *Run, step *Step) (string, error) {
	if isAcceptance(run, step) {
		return a.pins.acceptedTip(ctx, RunRef{ID: run.ID, ProjectID: run.ProjectID})
	}
	if run.ChangeID.IsZero() {
		return "", nil
	}
	c, pinned, err := LandingCandidateIn(ctx, a.pins.db, run.ID)
	if err != nil || !pinned {
		return "", err
	}
	return c.Candidate, nil
}

// readDecisionStep reads the run and step a decision was authorized for,
// outside the vote's transaction.
func (s *Service) readDecisionStep(ctx context.Context, expected stepDecisionSnapshot) (*Run, *Step, error) {
	run, err := readRun(ctx, s.db, expected.scope.RunID)
	if err != nil {
		return nil, nil, err
	}
	step, err := ReadStepIn(ctx, s.db, expected.scope.StepID)
	if err != nil {
		return nil, nil, err
	}
	return run, step, nil
}
