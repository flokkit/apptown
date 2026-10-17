package adoption

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption/internal/adoptionsql"
	"flokkit.io/forge/internal/stamp"
)

const ResolverVersion = 1

type ReceiptBlock struct {
	Position int    `json:"position"`
	Name     string `json:"name"`
	BlockID  ids.ID `json:"blockId"`
	Version  int    `json:"version"`
	Disabled bool   `json:"disabled,omitempty"`
	Tier     string `json:"tier,omitempty"`
}

type ReceiptWorkflow struct {
	Name     string `json:"name"`
	Version  int    `json:"version"`
	Origin   string `json:"origin"`
	YAMLHash string `json:"yamlHash"`
	System   bool   `json:"system,omitempty"`
}

type ReceiptPolicy struct {
	Name       string `json:"name"`
	Rule       string `json:"rule"`
	ConfigHash string `json:"configHash"`
	Origin     string `json:"origin,omitempty"`
}

// Receipt is the resolved union, per component type.
type Receipt struct {
	Resolver   int               `json:"resolver"`
	Pack       string            `json:"pack,omitempty"`
	Guidance   []ReceiptBlock    `json:"guidance"`
	Workflows  []ReceiptWorkflow `json:"workflows"`
	Policies   []ReceiptPolicy   `json:"policies"`
	Extensions []string          `json:"extensions"`
}

// EmptyReceipt is a receipt with every list present, so a stored receipt
// never carries null lists.
func EmptyReceipt() Receipt {
	return Receipt{Resolver: ResolverVersion, Guidance: []ReceiptBlock{}, Workflows: []ReceiptWorkflow{}, Policies: []ReceiptPolicy{}, Extensions: []string{}}
}

// Digest is a receipt's identity.
func Digest(r Receipt) string {
	b, _ := json.Marshal(r)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashText is the short hash used for workflow YAML and policy options.
func HashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

// ImportsOf turns a receipt's guidance into the import shape readers use.
func (r Receipt) ImportsOf() []Import {
	out := make([]Import, 0, len(r.Guidance))
	for _, b := range r.Guidance {
		out = append(out, Import{Position: b.Position, BlockID: b.BlockID, Name: b.Name, Version: b.Version, Disabled: b.Disabled})
	}
	return out
}

// Diff says in words what differs between two receipts.
func Diff(from, to Receipt) string {
	var out []string
	key := func(b ReceiptBlock) string {
		return fmt.Sprintf("%s@%d%s", b.Name, b.Version, map[bool]string{true: " (disabled)", false: ""}[b.Disabled])
	}
	was := map[string]bool{}
	for _, b := range from.Guidance {
		was[key(b)] = true
	}
	now := map[string]bool{}
	for _, b := range to.Guidance {
		now[key(b)] = true
		if !was[key(b)] {
			out = append(out, "guidance +"+key(b))
		}
	}
	for _, b := range from.Guidance {
		if !now[key(b)] {
			out = append(out, "guidance -"+key(b))
		}
	}
	wasW := map[string]int{}
	for _, w := range from.Workflows {
		wasW[w.Name] = w.Version
	}
	nowW := map[string]int{}
	for _, w := range to.Workflows {
		nowW[w.Name] = w.Version
		if v, ok := wasW[w.Name]; !ok {
			out = append(out, fmt.Sprintf("workflow +%s@%d", w.Name, w.Version))
		} else if v != w.Version {
			out = append(out, fmt.Sprintf("workflow %s v%d→v%d", w.Name, v, w.Version))
		}
	}
	for n, v := range wasW {
		if _, ok := nowW[n]; !ok {
			out = append(out, fmt.Sprintf("workflow -%s@%d", n, v))
		}
	}
	if !slices.Equal(from.Policies, to.Policies) {
		out = append(out, "policies changed")
	}
	if !slices.Equal(from.Extensions, to.Extensions) {
		out = append(out, "extensions changed")
	}
	if from.Pack != to.Pack {
		out = append(out, fmt.Sprintf("pack %q→%q", from.Pack, to.Pack))
	}
	if len(out) == 0 {
		return "no visible difference (order or metadata)"
	}
	return strings.Join(out, ", ")
}

// RecordedRevision is one row of project_adoptions.
type RecordedRevision struct {
	ID        ids.ID       `json:"id"`
	ProjectID ids.ID       `json:"projectId"`
	Revision  int64        `json:"revision"`
	ChangeID  ids.ID       `json:"changeId"` // an uplift target's change; Nil for a revision
	Kind      AdoptionKind `json:"kind"`
	Reason    string       `json:"reason"`
	Digest    string       `json:"digest"`
	CreatedBy string       `json:"createdBy"`
	// CreatedByPrincipal is the author's principal; zero for the forge, or
	// one not resolved to a person.
	CreatedByPrincipal ids.ID     `json:"createdByPrincipal,omitzero"`
	CreatedAt          stamp.Time `json:"createdAt"`
	Receipt            Receipt    `json:"receipt"`
}

// adoptionStore is the supported API over a project's revisions.
type adoptionStore struct {
	db      *database.DB
	clock   func() time.Time // the forge's clock; nil is the real one
	reviews ReviewRequests   // see Dependencies.Reviews
}

func newAdoptions(db *database.DB) *adoptionStore { return &adoptionStore{db: db} }

func (s *adoptionStore) now() time.Time { return clock.Or(s.clock)() }

// CurrentReceiptIn reads the project's current receipt in one statement snapshot
// through q, so a caller's transaction sees what it locked. Absence is
// sql.ErrNoRows.
func CurrentReceiptIn(ctx context.Context, q database.Queryer, projectID ids.ID) (*RecordedRevision, error) {
	r, err := adoptionsql.New(q.SQL()).CurrentAdoption(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return adoptionRecord(r)
}

func (s *adoptionStore) Current(ctx context.Context, projectID ids.ID) (*RecordedRevision, error) {
	return CurrentReceiptIn(ctx, s.db, projectID)
}

// ByIDIn reads one revision row through q; ByID through the database.
func ByIDIn(ctx context.Context, q database.Queryer, id ids.ID) (*RecordedRevision, error) {
	r, err := adoptionsql.New(q.SQL()).AdoptionByID(ctx, adoptionsql.AdoptionByIDParams{ID: id})
	if err != nil {
		return nil, err
	}
	return adoptionRecord(r)
}

func (s *adoptionStore) ByID(ctx context.Context, id ids.ID) (*RecordedRevision, error) {
	return ByIDIn(ctx, s.db, id)
}

// History is a project's revisions newest first, without their receipts:
// the list is light, the current revision carries the receipt.
func (s *adoptionStore) History(ctx context.Context, projectID ids.ID, limit int) ([]RecordedRevision, error) {
	rows, err := adoptionsql.New(s.db.SQL()).AdoptionHistory(ctx, adoptionsql.AdoptionHistoryParams{ProjectID: projectID, Limit: int32(limit)})
	out := make([]RecordedRevision, 0, len(rows))
	if err != nil {
		return []RecordedRevision{}, err
	}
	for _, r := range rows {
		ad, err := adoptionRecord(r)
		if err != nil {
			return []RecordedRevision{}, err
		}
		ad.Receipt = Receipt{}
		out = append(out, *ad)
	}
	return out, nil
}

// RecordIn writes the next revision inside the caller's transaction, which
// locks project identity before its aggregate: immutable revision, current pointer
// and receipt commit together.
// It is stamped at, the caller's clock (the forge's), by which the ways of
// working it adopted are later resolved as of that time.
func RecordIn(ctx context.Context, tx *database.Tx, projectID ids.ID, revision int64, kind AdoptionKind, reason string, receipt Receipt, by string, at time.Time) error {
	if !kind.Standing() {
		return ErrAdoptionKind
	}
	current, err := ensureCurrentForUpdateIn(ctx, tx, projectID)
	if err != nil {
		return err
	}
	prior, err := currentReceiptOptional(ctx, tx, projectID)
	if err != nil {
		return err
	}
	plan, err := current.prepareRecord(receipt, kind, reason, prior)
	if err != nil {
		return err
	}
	// This participant records an explicit revision. Reviewed edits choose reuse
	// in Adoptions.Advance before reaching persistence.
	plan.reuse = false
	next, event, err := current.Advance(plan, revision-1)
	if err != nil {
		return err
	}
	// Creation, not a change of adoption: revision 1 has no earlier
	// standing to recheck, and eval copies are throwaway. No review.
	return saveAdvanceIn(ctx, tx, current.Revision, next, plan, event, by, at, nil)
}

var ErrRevisionUnavailable = errors.New("recorded adoption revision is unavailable")

// Advance records the project's resolved union as its next revision, or
// returns the current one when nothing changed (clearing the project's
// drift note either way: a resolve is a review). With always, it records a
// revision even when the receipt is unchanged: the change is one the receipt
// does not show, such as a pack membership move. The project row is locked
// before the resolve, so a flip cannot interleave between what was read
// and what is written; the resolve reads through the same transaction.
func (s *adoptionStore) Advance(ctx context.Context, projectID ids.ID, kind AdoptionKind, reason, by string, always bool, resolve func(tx *database.Tx, prior *RecordedRevision, now time.Time) (Receipt, error)) (*RecordedRevision, error) {
	now := s.now()
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*RecordedRevision, error) {
		current, err := ensureCurrentForUpdateIn(ctx, tx, projectID)
		if err != nil {
			return nil, err
		}
		prior, err := currentReceiptOptional(ctx, tx, projectID)
		if err != nil {
			return nil, err
		}
		if err := current.requireReceipt(prior); err != nil {
			return nil, err
		}
		receipt, err := resolve(tx, prior, now)
		if err != nil {
			return nil, err
		}
		// resolve may set the current row's fields (a pull records its pack)
		// in this transaction; save from the row as it now stands, not the
		// copy read before, or the save would put the old pack back.
		if current, err = ensureCurrentForUpdateIn(ctx, tx, projectID); err != nil {
			return nil, err
		}
		plan, err := current.prepareRecord(receipt, kind, reason, prior)
		if err != nil {
			return nil, err
		}
		plan.reuse = plan.reuse && !always
		next, event, err := current.Advance(plan, current.Revision)
		if err != nil {
			return nil, err
		}
		if err := saveAdvanceIn(ctx, tx, current.Revision, next, plan, event, by, now, s.reviews); err != nil {
			return nil, err
		}
		if !event.Recorded {
			return prior, nil
		}
		return CurrentReceiptIn(ctx, tx, projectID)
	})
}

func currentReceiptOptional(ctx context.Context, q database.Queryer, projectID ids.ID) (*RecordedRevision, error) {
	prior, err := CurrentReceiptIn(ctx, q, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return prior, err
}

func adoptionRecord(r adoptionsql.ProjectAdoption) (*RecordedRevision, error) {
	kind, err := ParseAdoptionKind(r.Kind)
	if err != nil {
		return nil, err
	}
	ad := RecordedRevision{ID: r.ID, ProjectID: r.ProjectID, Revision: r.Revision, ChangeID: r.ChangeID, Kind: kind, Reason: r.Reason, Digest: r.Digest, CreatedBy: r.CreatedBy, CreatedByPrincipal: r.CreatedByPrincipal, CreatedAt: stamp.Time{NullTime: sql.NullTime{Time: r.CreatedAt, Valid: true}}}
	if err := json.Unmarshal(r.ReceiptJson, &ad.Receipt); err != nil {
		return nil, fmt.Errorf("adoption %d: invalid receipt: %w", ad.ID, err)
	}
	return &ad, nil
}

// ProjectAdoptionIn reads an admitted revision within its project fence.
func ProjectAdoptionIn(ctx context.Context, q database.Queryer, projectID ids.ID, id ids.ID) (*RecordedRevision, error) {
	if projectID.IsZero() {
		return nil, fmt.Errorf("project %d: %w", projectID, sql.ErrNoRows)
	}
	r, err := adoptionsql.New(q.SQL()).AdoptionByID(ctx, adoptionsql.AdoptionByIDParams{ID: id, ProjectID: projectID})
	if err != nil {
		return nil, err
	}
	return adoptionRecord(r)
}
