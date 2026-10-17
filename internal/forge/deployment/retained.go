package deployment

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/enum"
	"flokkit.io/forge/internal/fault"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/stamp"
)

// Retained resources (design/subsystems/deployment.md §2.6, §14.5): the forge
// never deletes what holds data. When a deployment is torn down, its managed
// need instances become retained resources: a record with an owner, an
// intent, the provider's identity and who tore the deployment down. So
// does what a teardown left at the target (deploy.TakenDown.Left): data
// where the target has no backups, and a delete that failed. A person
// settles the record released once it is deleted by hand.

type RetainedResource struct {
	ID         ids.ID         `json:"id"`
	ProjectID  ids.ID         `json:"projectId"`
	Project    string         `json:"project,omitempty"`
	Deployment string         `json:"deployment"`
	TargetID   ids.ID         `json:"targetId"`
	Target     string         `json:"target,omitempty"`
	Need       string         `json:"need"`
	Handle     map[string]any `json:"handle"`
	RetainedBy string         `json:"retainedBy"`
	// RetainedByPrincipal and SettledByPrincipal are those people's
	// principals; zero for the forge, or one not resolved to a person.
	RetainedByPrincipal ids.ID         `json:"retainedByPrincipal,omitzero"`
	RetainedAt          stamp.Time     `json:"retainedAt"`
	Disposition         RetentionState `json:"disposition"`
	Note                string         `json:"note,omitempty"`
	SettledBy           string         `json:"settledBy,omitempty"`
	SettledByPrincipal  ids.ID         `json:"settledByPrincipal,omitzero"`
	SettledAt           stamp.Time     `json:"settledAt,omitzero"`
	// HoldsData marks data; while it is open, the deployment's name on
	// its target is not given to a new deployment.
	HoldsData bool `json:"holdsData"`
}

// RetentionState is what became of a retained resource: still retained, or
// settled by a person as released (deleted at the provider). Kept (stays
// on purpose) is a word of rows settled under an earlier rule, when data
// left at a target could be kept indefinitely; a take-down now backs data
// up and deletes it, so nothing new is settled kept. Settling is one-way
// and happens once.
type RetentionState uint8

const (
	RetainedOpen RetentionState = iota + 1
	RetainedReleased
	RetainedKept
)

// retentionStateCodec spells each RetentionState in storage and JSON.
var retentionStateCodec = enum.Set[RetentionState]{
	Names:     []string{"retained", "released", "kept"},
	Invalid:   "invalid-retention-state",
	Err:       ErrRetentionState,
	ScanLabel: "value",
}

var (
	ErrRetentionState  = fault.New(fault.Unprocessable, "invalid retention state")
	ErrRetainedSettled = errors.New("retained resource is already settled")
	ErrRetainedMissing = fault.New(fault.NotFound, "no open retained resource")
)

func (s RetentionState) String() string { return retentionStateCodec.String(s) }

// Settle is the one rule: an open record becomes released, once its
// person has deleted it at the provider.
func (s RetentionState) Settle(to RetentionState) (RetentionState, error) {
	if s != RetainedOpen {
		return 0, fmt.Errorf("%w: it is %s", ErrRetainedSettled, s)
	}
	if to != RetainedReleased {
		return 0, fmt.Errorf("%w: a retained resource is settled released once it is deleted at the provider, not %s", ErrRetentionState, to)
	}
	return to, nil
}

func ParseRetentionState(text string) (RetentionState, error) { return retentionStateCodec.Parse(text) }

func (s RetentionState) Value() (driver.Value, error) { return retentionStateCodec.Value(s) }

func (s *RetentionState) Scan(value any) error { return retentionStateCodec.Scan(s, value) }

func (s RetentionState) MarshalJSON() ([]byte, error) { return retentionStateCodec.JSON(s) }

func (s *RetentionState) UnmarshalJSON(data []byte) error {
	return retentionStateCodec.ReadJSON(s, data)
}

// Retained reads the retained-resources ledger. Settlement goes through Service.
type Retained struct{ db *database.DB }

func NewRetained(db *database.DB) *Retained { return &Retained{db: db} }

// retainNeedInstancesIn moves a deployment's managed need instances into
// the retained ledger inside the caller's transaction, naming each (need:
// name). Intent without an identity is kept too: nothing was made that we
// know of, but the intent is still worth a person's check. A retention that
// cannot be written fails the caller; a torn-down deployment never loses
// the record of what it left behind.
func retainNeedInstancesIn(ctx context.Context, tx *database.Tx, dep *Deployment, who string, deleted map[string]bool, at time.Time) ([]string, error) {
	retainer, err := identity.ActorIn(ctx, tx, who)
	if err != nil {
		return nil, err
	}
	insts, err := deploymentsql.New(tx.SQL()).NeedInstances(ctx, []ids.ID{dep.ID})
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, i := range insts {
		if i.Mode != "managed" || deleted[i.Need] {
			continue
		}
		h := jsonMap(string(i.HandleJson))
		name, _ := h["name"].(string)
		if name == "" {
			if req, ok := h["requested"].(map[string]any); ok {
				name, _ = req["name"].(string)
			}
		}
		if _, err := deploymentsql.New(tx.SQL()).InsertRetainedResource(ctx, deploymentsql.InsertRetainedResourceParams{
			ID:                  ids.New(at),
			ProjectID:           dep.ProjectID,
			Deployment:          dep.Name,
			TargetID:            dep.TargetID,
			Need:                i.Need,
			HandleJson:          i.HandleJson,
			RetainedBy:          who,
			RetainedByPrincipal: retainer.Principal,
			RetainedByVia:       retainer.Via,
			RetainedAt:          at.UTC(),
			Disposition:         RetainedOpen.String(),
			HoldsData:           true,
		}); err != nil {
			return nil, fmt.Errorf("retaining %s: %w", i.Need, err)
		}
		out = append(out, i.Need+": "+name)
	}
	return out, nil
}

// retainTakenDownIn records, inside the removal's transaction, what the
// teardown left at the target: data it keeps (no backup there) and anything
// it failed to delete, each with why. A person settles each.
func retainTakenDownIn(ctx context.Context, tx *database.Tx, dep *Deployment, who string, left []deploy.Left, at time.Time) ([]string, error) {
	q := deploymentsql.New(tx.SQL())
	retainer, err := identity.ActorIn(ctx, tx, who)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, l := range left {
		h := map[string]any{}
		for k, v := range l.Handle {
			h[k] = v
		}
		if l.Why != "" {
			h["why"] = l.Why
		}
		handle, err := json.Marshal(h)
		if err != nil {
			return nil, err
		}
		if _, err := q.InsertRetainedResource(ctx, deploymentsql.InsertRetainedResourceParams{ID: ids.New(at),
			ProjectID: dep.ProjectID, Deployment: dep.Name, TargetID: dep.TargetID, Need: l.What, HandleJson: handle,
			RetainedBy: who, RetainedByPrincipal: retainer.Principal, RetainedByVia: retainer.Via, RetainedAt: at.UTC(), Disposition: RetainedOpen.String(), HoldsData: l.Data,
		}); err != nil {
			return nil, fmt.Errorf("retaining %s: %w", l.What, err)
		}
		line := l.What
		if l.Why != "" {
			line += " (" + l.Why + ")"
		}
		out = append(out, line)
	}
	return out, nil
}

// RetainedPreviewKey is a preview's AI models key whose revocation failed:
// it is retained, naming the key, for a person to revoke by hand.
type RetainedPreviewKey struct {
	ProjectID ids.ID
	// Preview is the preview's name, which the row names as its deployment;
	// Name the key's name at OpenRouter.
	Preview, Name   string
	Connector, Hash string
	// Why is the revocation's failure; At when it was retained.
	Why string
	At  time.Time
}

// RecordPreviewKeyIn retains a preview's key in the caller's transaction,
// the one in which Preview forgets its own record of the key: either both
// happen or neither does, so a key is never off both lists.
func (s *Retained) RecordPreviewKeyIn(ctx context.Context, tx *database.Tx, k RetainedPreviewKey) error {
	handle := fmt.Sprintf(`{"name":%q,"kind":%q,"connector":%q,"hash":%q,"why":%q}`, k.Name, KindOpenRouterKey, k.Connector, k.Hash, k.Why)
	_, err := deploymentsql.New(tx.SQL()).InsertRetainedResource(ctx, deploymentsql.InsertRetainedResourceParams{ID: ids.New(k.At),
		ProjectID: k.ProjectID, Deployment: k.Preview, Need: "AI models key", HandleJson: []byte(handle),
		RetainedBy: "forge-system", RetainedAt: k.At, Disposition: RetainedOpen.String(),
	})
	return err
}

func retainedRecord(r deploymentsql.RetainedResource) (RetainedResource, error) {
	out := RetainedResource{
		ID:                  r.ID,
		ProjectID:           r.ProjectID,
		Deployment:          r.Deployment,
		TargetID:            r.TargetID,
		Need:                r.Need,
		Handle:              jsonMap(string(r.HandleJson)),
		RetainedBy:          r.RetainedBy,
		RetainedByPrincipal: r.RetainedByPrincipal,
		RetainedAt:          stamp.Time{NullTime: sql.NullTime{Time: r.RetainedAt, Valid: true}},
		Note:                r.Note,
		SettledBy:           r.SettledBy,
		SettledByPrincipal:  r.SettledByPrincipal,
		SettledAt:           stamp.Time{NullTime: r.SettledAt},
		HoldsData:           r.HoldsData,
	}
	if err := out.Disposition.Scan(r.Disposition); err != nil {
		return out, err
	}
	return out, nil
}

// OpenForInstance are the open retained resources that name a managed
// instance: data kept from a deployment that still lives in it.
func (s *Retained) OpenForInstance(ctx context.Context, instance string) ([]RetainedResource, error) {
	rows, err := deploymentsql.New(s.db.SQL()).OpenRetainedForInstance(ctx, deploymentsql.OpenRetainedForInstanceParams{OpenState: RetainedOpen.String(), Instance: instance})
	if err != nil {
		return nil, err
	}
	out := make([]RetainedResource, 0, len(rows))
	for _, r := range rows {
		rr, err := retainedRecord(r)
		if err != nil {
			return nil, err
		}
		out = append(out, rr)
	}
	return out, nil
}

func (s *Retained) list(ctx context.Context, in deploymentsql.RetainedResourcesParams) ([]RetainedResource, error) {
	rows, err := deploymentsql.New(s.db.SQL()).RetainedResources(ctx, in)
	out := []RetainedResource{}
	if err != nil {
		return []RetainedResource{}, err
	}
	for _, r := range rows {
		v, err := retainedRecord(r.RetainedResource)
		if err != nil {
			return []RetainedResource{}, err
		}
		v.Project = r.Project
		v.Target = r.Target
		out = append(out, v)
	}
	return out, nil
}
func (s *Retained) All(ctx context.Context) ([]RetainedResource, error) {
	return s.list(ctx, deploymentsql.RetainedResourcesParams{})
}
func (s *Retained) ForProject(ctx context.Context, projectID ids.ID) ([]RetainedResource, error) {
	return s.list(ctx, deploymentsql.RetainedResourcesParams{ProjectOnly: true, ProjectID: projectID})
}

// OpenForTarget is what is still held at a target from torn-down deployments.
func (s *Retained) OpenForTarget(ctx context.Context, targetID ids.ID) ([]RetainedResource, error) {
	return s.list(ctx, deploymentsql.RetainedResourcesParams{TargetOnly: true, TargetID: targetID, Disposition: RetainedOpen.String()})
}
func (s *Retained) CountOpen(ctx context.Context, targetID ids.ID) (int, error) {
	n, err := deploymentsql.New(s.db.SQL()).RetainedOpenCount(ctx, deploymentsql.RetainedOpenCountParams{TargetID: targetID, Disposition: RetainedOpen.String()})
	return int(n), err
}

type SettleRetained struct {
	ProjectID   ids.ID
	ResourceID  ids.ID
	Disposition RetentionState
	Note        string
}

// SettleRetained records an authorized person's account, not a provider action.
// The conditional transition and required receipt commit together, once only.
func (s *Service) SettleRetained(ctx context.Context, in SettleRetained) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.authority == nil {
		return errors.New("retained settlement is not configured")
	}
	actor, err := s.authority.Require(ctx, access.Permission{Capability: access.CapDeploymentTeardown, Resource: access.Resource{Type: "project", ID: in.ProjectID.String(), ProjectID: in.ProjectID}, Context: access.Context{Route: "deployment.settle-retained", Consequential: true}})
	if err != nil {
		return err
	}
	if actor.Principal.Name == "" {
		return errors.New("retained settlement caller required")
	}
	to, err := RetainedOpen.Settle(in.Disposition)
	if err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		at := s.now()
		settler, err := identity.ActorIn(ctx, tx, actor.Principal.Name)
		if err != nil {
			return err
		}
		res, err := deploymentsql.New(tx.SQL()).SettleRetainedResource(ctx, deploymentsql.SettleRetainedResourceParams{
			Disposition:        to.String(),
			Note:               in.Note,
			SettledBy:          actor.Principal.Name,
			SettledByPrincipal: settler.Principal,
			SettledByVia:       settler.Via,
			SettledAt:          sql.NullTime{Time: at.UTC(), Valid: true},
			ID:                 in.ResourceID,
			ProjectID:          in.ProjectID,
			OpenState:          RetainedOpen.String(),
		})
		if err != nil {
			return err
		}
		if res == 0 {
			return fmt.Errorf("%w: %d", ErrRetainedMissing, in.ResourceID)
		}
		if res != 1 {
			return errors.New("retained settlement did not update one record")
		}
		return audit.At(at).RecordIn(ctx, tx, actor.Principal.Name, in.ProjectID, "retained.settled", fmt.Sprint(in.ResourceID), map[string]any{"disposition": to, "note": in.Note})
	})
}
