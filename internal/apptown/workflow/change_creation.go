package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// This command is private Work admission. Composition supplies the initiator
// after its existing capability guard; worker operations explicitly use system
// ownership. Pinned acceptance is data, never a title convention or encoded map.
type ChangeDraft struct {
	ProjectID                      ids.ID
	Type, Title, Body, ExternalRef string
	TitleFromChat                  bool
	Acceptance                     *Acceptance
}

type CreatedChange struct {
	Change              *Change
	Slug, DefaultBranch string
}

type changeCreatedReceipt struct {
	ChangeID ids.ID      `json:"changeId"`
	State    ChangeState `json:"state"`
}

type Creator struct {
	records changeStore
	prepare func(context.Context, CreatedChange) error
}

func (c Creator) Create(ctx context.Context, in ChangeDraft, who string) (*Change, error) {
	return c.CreateWith(ctx, in, who, nil)
}

// CreateWith is Create with a participant joined to the Change's admission
// transaction; see AdmissionParticipant. The workspace is prepared only after
// both commit.
func (c Creator) CreateWith(ctx context.Context, in ChangeDraft, who string, join AdmissionParticipant) (*Change, error) {
	created, err := c.records.create(ctx, in, who, join)
	if err != nil {
		return nil, err
	}
	return c.prepareCreated(ctx, created)
}

func (c Creator) prepareCreated(ctx context.Context, created CreatedChange) (*Change, error) {
	ch := created.Change
	if ch.Branch == "" {
		return ch, nil
	}
	if c.prepare == nil {
		return ch, fmt.Errorf("Change #%d is recorded; workspace preparation is not configured", ch.ID)
	}
	if err := c.prepare(ctx, created); err != nil {
		return ch, fmt.Errorf("Change #%d is recorded; preparing its workspace: %w", ch.ID, err)
	}
	// Workspace preparation records the base. Return checked current facts while
	// retaining the committed identity if a later read fails.
	out, err := readChange(ctx, c.records.db, ch.ID)
	if err != nil {
		return ch, fmt.Errorf("Change #%d is recorded; reading its prepared workspace: %w", ch.ID, err)
	}
	return out, nil
}

func (s changeStore) create(ctx context.Context, in ChangeDraft, who string, join AdmissionParticipant) (CreatedChange, error) {
	return database.Transact(ctx, s.db, func(tx *database.Tx) (CreatedChange, error) {
		created, err := CreateChangeIn(ctx, tx, in, who, s.now())
		if err != nil || join == nil {
			return created, err
		}
		if err := join(ctx, tx, created.Change); err != nil {
			return CreatedChange{}, err
		}
		return created, nil
	})
}

// The project lock precedes creation. Atomic remediation already holds it before
// its item locks; this participant joins that transaction without committing or
// preparing a workspace. Other admission opens its own transaction above.
// at is AppTown's time the Change is made.
func CreateChangeIn(ctx context.Context, tx *database.Tx, in ChangeDraft, who string, at time.Time) (CreatedChange, error) {
	if in.ProjectID.IsZero() || strings.TrimSpace(in.Title) == "" || strings.TrimSpace(who) == "" {
		return CreatedChange{}, fmt.Errorf("invalid Change creation request")
	}
	if in.Type == "" {
		in.Type = "feature"
	}
	project, err := workflowsql.New(tx.SQL()).ProjectForUpdate(ctx, in.ProjectID)
	if err != nil {
		return CreatedChange{}, err
	}
	if err := refuseUnlessActive(ctx, tx, in.ProjectID); err != nil {
		return CreatedChange{}, err
	}
	created := CreatedChange{Slug: project.Slug, DefaultBranch: project.DefaultBranch}
	return insertChangeIn(ctx, tx, in, who, created, changeOrigin{autoBranch: true}, at)
}

// All Change admission shares this insertion and initiator receipt. Specialized
// commands supply validated origin facts while holding the project lock.
type changeOrigin struct {
	autoBranch               bool
	branch, base, provenance string
	expectedRevision         int64
}

func insertChangeIn(ctx context.Context, tx *database.Tx, in ChangeDraft, who string, created CreatedChange, origin changeOrigin, at time.Time) (CreatedChange, error) {
	if in.ProjectID.IsZero() || strings.TrimSpace(in.Title) == "" || strings.TrimSpace(who) == "" {
		return CreatedChange{}, fmt.Errorf("invalid Change creation request")
	}
	if in.Type == "" {
		in.Type = "feature"
	}
	state, owner := ChangeInProgress, titleHuman
	if in.TitleFromChat {
		owner = titleAwaitingChat
	}
	acceptance := []byte("{}")
	if in.Acceptance != nil {
		var err error
		acceptance, err = json.Marshal(in.Acceptance)
		if err != nil {
			return CreatedChange{}, err
		}
	}
	ownerValue, err := owner.Value()
	if err != nil {
		return CreatedChange{}, err
	}
	opener, err := identity.ActorIn(ctx, tx, who)
	if err != nil {
		return CreatedChange{}, err
	}
	number, err := workflowsql.New(tx.SQL()).NextChangeNumber(ctx, in.ProjectID)
	if err != nil {
		return CreatedChange{}, err
	}
	row, err := workflowsql.New(tx.SQL()).InsertChange(ctx, workflowsql.InsertChangeParams{ID: ids.New(at), Number: number,
		ProjectID: in.ProjectID, Type: in.Type, Title: in.Title, Body: in.Body, ExternalRef: in.ExternalRef,
		AcceptanceJson: acceptance, State: state.String(), TitleOwnership: ownerValue.(string),
		Branch: origin.branch, BaseSha: origin.base, Provenance: origin.provenance, ExpectedRevision: origin.expectedRevision, OpenedBy: who, OpenedByPrincipal: opener.Principal, OpenedByVia: opener.Via, CreatedAt: at})
	if err != nil {
		return CreatedChange{}, err
	}
	id := row.ID
	if origin.autoBranch && created.DefaultBranch != "" {
		n, err := workflowsql.New(tx.SQL()).SetChangeBranch(ctx, workflowsql.SetChangeBranchParams{Branch: fmt.Sprintf("apptown/change-%d", id), ID: id, ProjectID: in.ProjectID})
		if err != nil {
			return CreatedChange{}, err
		}
		if n != 1 {
			return CreatedChange{}, fmt.Errorf("Change branch was not recorded")
		}
	}
	if err := audit.At(at).RecordIn(ctx, tx, who, in.ProjectID, "change.created", in.Title, changeCreatedReceipt{id, state}); err != nil {
		return CreatedChange{}, err
	}
	ch, err := readChange(ctx, tx, id)
	if err != nil {
		return CreatedChange{}, err
	}
	created.Change = ch
	return created, nil
}

// prepareCreated readies a new change's workspace after its row commits.
func (s *Service) prepareCreated(ctx context.Context, created CreatedChange) error {
	return s.workspaces.PrepareChange(ctx, ChangeRef{ID: created.Change.ID, ProjectID: created.Change.ProjectID})
}

func NewCreator(db *database.DB, prepare func(context.Context, CreatedChange) error) Creator {
	return Creator{records: changeStore{db: db}, prepare: prepare}
}
