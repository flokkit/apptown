// Package scm owns admitted merge operations and their durable outcomes. Work
// authority and external repository/provider effects enter through named ports.
package scm

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
)

var (
	ErrCaller     = errors.New("SCM requires an authorized Work operation")
	errUnresolved = errors.New("default branch moved without the recorded merge operation; inspect it before retrying")
	// ErrLeftPending: some pending merges could not be inspected. Each stays
	// pending, holding its landing until it is inspected again; the others
	// were inspected.
	ErrLeftPending = errors.New("merges left pending")
)

type Purpose uint8

const (
	Observe Purpose = iota + 1
	NewEffect
)

type MergeRequest struct {
	Identity RepositoryIdentity
	Project  string
	IID      int
}
type Source struct {
	RunID, ProjectID ids.ID
	// RunNumber is the run's number within its project: the commit title
	// names the run by it; trailers carry RunID.
	RunNumber                                    int64
	Repository, Workspace, Branch, DefaultBranch string
	WorkflowName                                 string
	WorkflowVersion                              int
	MR                                           MergeRequest
	// Candidate and Target pin a landing: the branch commit its gates checked
	// and the default-branch commit it was caught up with. Empty for a run
	// admitted before candidates were pinned.
	Candidate, Target string
	// PinRequired: the landing was admitted under candidate pinning, so it
	// never publishes its workspace at merge, even without a candidate.
	PinRequired bool
	// Attribution is what admitted this merge, as Work read it at the
	// check-in that admitted it; nil for an inspection. Read-only: the merge
	// commit and the merge request's description are rendered from it.
	Attribution *Attribution
}

func (s Source) repositoryBacked() bool { return s.Repository != "" }
func (s Source) validate() error {
	if s.RunID.IsZero() || s.ProjectID.IsZero() {
		return ErrCaller
	}
	if s.repositoryBacked() && (s.Branch == "" || s.DefaultBranch == "") {
		return fmt.Errorf("merge source is missing its branch identity")
	}
	if !s.repositoryBacked() && s.DefaultBranch != "" {
		return fmt.Errorf("merge source has no repository")
	}
	if s.MR.IID < 0 || ((s.MR.IID != 0) != (s.MR.Project != "")) || (s.MR.IID != 0 && !s.repositoryBacked()) {
		return fmt.Errorf("merge source has an invalid provider request")
	}
	return nil
}

type Refusal struct {
	Reason  string
	CheckID ids.ID
}

func (r Refusal) refused() bool { return r.Reason != "" }

// work resolves an explicit operation context, never an actor label from a
// command. Missing authority and unavailable scope are errors. Inspection has
// separate permission and cannot admit new work.
type work interface {
	Prepare(context.Context, Purpose, ids.ID) (Claim, error)
	RequireInspection(context.Context) error
}

// Claim is Work's immutable transaction participant. CheckIn must lock and
// compare its prepared project/run/source and, for NewEffect, enforce current
// gates. RecordIn commits the selected Work source and required receipt. It is
// not a callback for arbitrary SQL or a bag of foreign domain rows.
type Claim interface {
	Source() Source
	CheckIn(context.Context, *database.Tx, Purpose) (Refusal, error)
	RecordIn(context.Context, *database.Tx, Receipt) error
}

type ReceiptKind uint8

const (
	Completed ReceiptKind = iota + 1
	cancelled
)

type Receipt struct {
	Kind      ReceiptKind `json:"-"`
	Operation string      `json:"operation"`
	Branch    string      `json:"branch,omitempty"`
	SHA       string      `json:"sha,omitempty"`
	Via       string      `json:"via,omitempty"`
	IID       int         `json:"iid,omitempty"`
	Reason    string      `json:"reason,omitempty"`
}

func (r Receipt) Event() (string, error) {
	if r.Operation == "" {
		return "", fmt.Errorf("merge receipt requires its operation")
	}
	switch r.Kind {
	case Completed:
		if r.Branch == "" || r.SHA == "" {
			return "", fmt.Errorf("merge completion receipt requires its branch and result")
		}
		return "merge.completed", nil
	case cancelled:
		if r.Reason == "" {
			return "", fmt.Errorf("merge cancellation receipt requires its reason")
		}
		return "merge.cancelled", nil
	default:
		return "", fmt.Errorf("unknown merge receipt kind %d", r.Kind)
	}
}

type Record struct {
	State                       State
	Operation, Expected, Result string
}
type Result struct {
	Source           Source
	Merged           bool
	SHA, Operation   string
	Conflicts, Drift []string
	DriftDiff        string
	Refusal          Refusal
}

type planKind uint8

const (
	empty planKind = iota + 1
	existing
	LocalMerge
	providerMerge
	blocked
)

// Prepared describes checked external facts. Only the service can turn it into
// a durable intent or a committed receipt; drivers do not choose SCM transitions.
type Prepared struct {
	Kind             planKind
	Expected, Result string
	ReuseRecorded    bool
	Conflicts, Drift []string
	DriftDiff        string
}
type Execution struct {
	Result string
	// Unchanged is proof that the attempted effect left the target at Expected.
	// It permits cancellation; an error without this proof leaves the intent.
	Unchanged bool
	Conflicts []string
}
type inspectionKind uint8

const (
	Found inspectionKind = iota + 1
	unchanged
	unresolved
)

type Inspection struct {
	Kind   inspectionKind
	Result string
}

// Prepare may publish the selected workspace branch, but cannot merge the
// default branch. Execute runs only after Work admission (and local intent
// commit). Inspect can prove an old effect or an unchanged target, never replay it.
type Driver interface {
	Prepare(context.Context, Source, string) (Prepared, error)
	Execute(context.Context, Source, Prepared, string) (Execution, error)
	Inspect(context.Context, Source, Record) (Inspection, error)
}

// Summary counts an inspection pass; Failed counts merges whose inspection
// failed and that stay pending.
type Summary struct{ Inspected, Recorded, Cancelled, Unresolved, Failed int }
