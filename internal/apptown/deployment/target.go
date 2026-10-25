package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// A target is a reusable place AppTown can deploy to, reached through one
// provider (design/subsystems/deployment.md §2.2, §8). It exists as an offered
// place before any deployment is bound to it. This package owns its record,
// its lifecycle (TargetState) and its offers; AppTown holds the doors and the
// provider-specific checks.

const (
	KindDockerHost = "docker-host"
	KindPortainer  = "portainer"
	KindDOApp      = "do-app"
	KindS3Bucket   = "s3-bucket"

	OriginBuiltIn     = "built-in"
	OriginAttached    = "attached"
	OriginProvisioned = "provisioned"
)

// Target is one row of the targets table.
type Target struct {
	ConnectionID    ids.ID             `json:"connectionId,omitempty"`
	ProviderScope   json.RawMessage    `json:"providerScope,omitempty"`
	ID              ids.ID             `json:"id"`
	Name            string             `json:"name"`
	Kind            string             `json:"kind"`
	Provider        string             `json:"provider"` // integration name; "" for the built-in host
	Address         map[string]any     `json:"address"`
	Origin          string             `json:"origin"`
	Capabilities    TargetCapabilities `json:"capabilities"`
	Ingress         map[string]any     `json:"ingress"`
	NeedsMode       string             `json:"needsMode"`
	SecretsDelivery string             `json:"secretsDelivery"`
	Status          TargetState        `json:"status"`
	// SettingsRevision advances with every edit and with every sync that
	// changes a configuration fact; an edit sends the one it started from.
	SettingsRevision int64 `json:"settingsRevision"`
}

func (t *Target) String() string { return t.Name + " (" + t.Kind + ")" }

// TargetCapabilities is what a target can express (§2.2). A contract that
// asks for more is refused at the door with a sentence.
type TargetCapabilities struct {
	PersistentVolumes bool     `json:"persistentVolumes"` // §0.1a: a dataDir contract needs one
	Previews          bool     `json:"previews"`          // may previews bind here (§5, §13.3)
	OwnLiveness       bool     `json:"ownLiveness"`       // the target restarts and probes; the health controller steps back
	Rollout           []string `json:"rollout"`           // replicas, rolling, traffic, drain, rollback, zero-downtime
	Needs             []string `json:"needs"`             // need kinds it can fulfil in its needs mode
}

// Offer is one row of target_offers (§15.7): a target made available to a
// group, or to everyone ("*"), with caps.
type Offer struct {
	ID        ids.ID    `json:"id"`
	TargetID  ids.ID    `json:"targetId"`
	Group     string    `json:"group"`
	Caps      OfferCaps `json:"caps"`
	CreatedBy string    `json:"createdBy"`
	// CreatedByPrincipal is the offerer's principal; zero when not resolved to a person.
	CreatedByPrincipal ids.ID    `json:"createdByPrincipal,omitzero"`
	CreatedAt          time.Time `json:"createdAt"`
	Implicit           bool      `json:"implicit,omitempty"` // built-in targets: offered by construction, no row
}

// OfferCaps: what the offer permits. Empty means no limit.
type OfferCaps struct {
	MaxStanding int      `json:"maxStanding,omitempty"` // standing deployments per project on this target
	Audiences   []string `json:"audiences,omitempty"`   // team | organisation | partners | public
	Data        []string `json:"data,omitempty"`        // synthetic | non-production | production
}

var (
	ErrTargetMissing  = fault.New(fault.NotFound, "target does not exist")
	ErrTargetConflict = fault.New(fault.Conflict, "target operation refused")
	ErrOfferMissing   = fault.New(fault.NotFound, "offer does not exist")
)

// ConnectorRetirement is the one collaborator retirement needs from outside
// this domain: a cloud target made its own account connector, which goes when
// the last target on that account is retired. Integrations owns the connector.
type ConnectorRetirement interface {
	RetireUnusedConnector(ctx context.Context, tx *database.Tx, provider, targetName, who string, at time.Time) error
}

// Targets is the supported operation API for targets and offers. Storage is
// private; every write commits with its receipt.
//
// Lock order: an operation that reads a target row to decide how to change
// it (EditSettings, Sync, Retire) first takes the targets table lock
// (LockTargets), then the row. Taking the row first and the table's write
// lock later, while another holds the table lock and waits for the row, is a
// deadlock. Such changes are rare and short, so they simply queue.
type Targets struct {
	db         *database.DB
	connectors ConnectorRetirement
	implicit   []string         // targets offered to everyone by construction, by name
	clock      func() time.Time // AppTown's clock, which stamps targets and offers; nil is the real one
}

func (s *Targets) now() time.Time { return clock.Or(s.clock)().UTC() }

func NewTargets(db *database.DB, connectors ConnectorRetirement, offeredByConstruction ...string) *Targets {
	return &Targets{db: db, connectors: connectors, implicit: offeredByConstruction}
}

func targetRecord(r deploymentsql.Target, err error) (*Target, error) {
	if err != nil {
		return nil, err
	}
	t := &Target{ID: r.ID, Name: r.Name, Kind: r.Kind, Provider: r.Provider, ConnectionID: r.ConnectionID, Address: jsonMap(string(r.AddressJson)), Origin: r.Origin, Ingress: jsonMap(string(r.IngressJson)), NeedsMode: r.NeedsMode, SecretsDelivery: r.SecretsDelivery, SettingsRevision: r.SettingsRevision}
	if r.ProviderScopeJson != nil {
		t.ProviderScope = *r.ProviderScopeJson
	}
	if err := t.Status.Scan(r.Status); err != nil {
		return nil, err
	}
	// What a target can do decides what is asked of it: unreadable
	// capabilities are an error, never a target that claims none.
	if err := json.Unmarshal(r.CapabilitiesJson, &t.Capabilities); err != nil {
		return nil, fmt.Errorf("target %s's capabilities could not be read: %w", r.Name, err)
	}
	return t, nil
}

func jsonMap(s string) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

func jsonText(v any) string {
	if m, ok := v.(map[string]any); ok && m == nil {
		v = map[string]any{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (s *Targets) ByID(ctx context.Context, id ids.ID) (*Target, error) {
	return targetRecord(deploymentsql.New(s.db.SQL()).TargetByID(ctx, id))
}

// ByName reads a target; a name that names none is fault.NotFound.
func (s *Targets) ByName(ctx context.Context, name string) (*Target, error) {
	t, err := targetRecord(deploymentsql.New(s.db.SQL()).TargetByName(ctx, name))
	return t, database.Missing(err)
}

func (s *Targets) All(ctx context.Context) ([]*Target, error) {
	rows, err := deploymentsql.New(s.db.SQL()).Targets(ctx, nil)
	if err != nil {
		return nil, err
	}
	var out []*Target
	for _, r := range rows {
		t, err := targetRecord(r, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// Put records a target exactly as given, replacing what a record of that
// name held apart from its id, origin and a retirement: attaching a new
// target and test setup. A target AppTown derives from configuration
// refreshes through Sync instead, which keeps the administrator's settings.
func (s *Targets) Put(ctx context.Context, t *Target) (*Target, error) {
	if _, err := t.Status.Value(); err != nil {
		return nil, err
	}
	return targetRecord(deploymentsql.New(s.db.SQL()).PutTarget(ctx, putTargetParams(t, s.now())))
}

// Attachment is what a person's attach of a target records: what was
// attached (target.attached) and what the check before it answered
// (target.checked).
type Attachment struct {
	By               string
	Attached, Answer map[string]any
}

// receipts writes an attach's two receipts in the caller's transaction.
func (at Attachment) receipts(ctx context.Context, tx *database.Tx, now time.Time, name string) error {
	if err := audit.At(now).RecordIn(ctx, tx, at.By, ids.Nil, "target.attached", name, at.Attached); err != nil {
		return err
	}
	return audit.At(now).RecordIn(ctx, tx, at.By, ids.Nil, "target.checked", name, at.Answer)
}

// Attach records a target a person attached, with its receipts in the same
// transaction: where projects may deploy is not changed without its record.
func (s *Targets) Attach(ctx context.Context, t *Target, at Attachment) (*Target, error) {
	if _, err := t.Status.Value(); err != nil {
		return nil, err
	}
	now := s.now()
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Target, error) {
		saved, err := targetRecord(deploymentsql.New(tx.SQL()).PutTarget(ctx, putTargetParams(t, now)))
		if err != nil {
			return nil, err
		}
		return saved, at.receipts(ctx, tx, now, saved.Name)
	})
}

func putTargetParams(t *Target, now time.Time) deploymentsql.PutTargetParams {
	return deploymentsql.PutTargetParams{ID: ids.New(now),
		Name:             t.Name,
		Kind:             t.Kind,
		Provider:         t.Provider,
		AddressJson:      json.RawMessage(jsonText(t.Address)),
		Origin:           t.Origin,
		CapabilitiesJson: json.RawMessage(jsonText(t.Capabilities)),
		IngressJson:      json.RawMessage(jsonText(t.Ingress)),
		NeedsMode:        t.NeedsMode,
		SecretsDelivery:  t.SecretsDelivery,
		Status:           t.Status.String(),
		CreatedAt:        now,
		UpdatedAt:        now,
		RetiredState:     TargetRetired.String(),
	}
}

// Sync records a target AppTown derives from configuration — the built-in
// host at every start, an App Platform account's target from its connector,
// the intranet's Portainer from AppTown configuration. A new target is created
// as given. An existing one keeps the administrator's settings
// (Target.keepSettings) and takes only the configuration's facts; when one of
// those differs, the record, a settings revision and a target.refreshed
// receipt by who, with before and after, commit together, as an edit's would.
// An unchanged sync writes nothing. A retired target stays as it is. Status
// belongs to checks and retirement, so a sync never changes it.
func (s *Targets) Sync(ctx context.Context, t *Target, who string) (*Target, error) {
	return s.sync(ctx, t, who, nil)
}

// sync is Sync, with an attach's receipts (when not nil) in its transaction.
func (s *Targets) sync(ctx context.Context, t *Target, who string, attached *Attachment) (*Target, error) {
	now := s.now()
	if _, err := t.Status.Value(); err != nil {
		return nil, err
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Target, error) {
		saved, err := s.syncIn(ctx, tx, t, who, now)
		if err != nil || attached == nil {
			return saved, err
		}
		return saved, attached.receipts(ctx, tx, now, saved.Name)
	})
}

// syncIn is Sync's write in the caller's transaction.
func (s *Targets) syncIn(ctx context.Context, tx *database.Tx, t *Target, who string, now time.Time) (*Target, error) {
	q := deploymentsql.New(tx.SQL())
	// The targets lock order: table, then row (see Targets). The table
	// lock also keeps a create from racing another sync of the name.
	if err := q.LockTargets(ctx); err != nil {
		return nil, err
	}
	stored, err := targetRecord(q.TargetByNameForUpdate(ctx, t.Name))
	if errors.Is(err, sql.ErrNoRows) {
		return targetRecord(q.PutTarget(ctx, putTargetParams(t, now)))
	}
	if err != nil {
		return nil, err
	}
	if stored.Status == TargetRetired {
		return stored, nil
	}
	next := *t
	next.keepSettings(stored)
	before, after := configurationChanges(stored, &next)
	if len(after) == 0 {
		return stored, nil
	}
	saved, err := targetRecord(q.RefreshTarget(ctx, deploymentsql.RefreshTargetParams{
		ID: stored.ID, Kind: next.Kind, Provider: next.Provider, AddressJson: json.RawMessage(jsonText(next.Address)),
		CapabilitiesJson: json.RawMessage(jsonText(next.Capabilities)), IngressJson: json.RawMessage(jsonText(next.Ingress)),
		NeedsMode: next.NeedsMode, SecretsDelivery: next.SecretsDelivery, UpdatedAt: now,
	}))
	if err != nil {
		return nil, err
	}
	if err := audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "target.refreshed", saved.Name, map[string]any{"before": before, "after": after}); err != nil {
		return nil, err
	}
	return saved, nil
}

// configurationChanges names each part of the record a sync would change,
// with its stored and proposed values.
func configurationChanges(stored, next *Target) (before, after map[string]any) {
	before, after = map[string]any{}, map[string]any{}
	for _, part := range []struct {
		name     string
		was, now any
	}{
		{"kind", stored.Kind, next.Kind},
		{"provider", stored.Provider, next.Provider},
		{"address", stored.Address, next.Address},
		{"capabilities", stored.Capabilities, next.Capabilities},
		{"ingress", stored.Ingress, next.Ingress},
		{"needsMode", stored.NeedsMode, next.NeedsMode},
		{"secretsDelivery", stored.SecretsDelivery, next.SecretsDelivery},
	} {
		if jsonText(part.was) != jsonText(part.now) {
			before[part.name], after[part.name] = part.was, part.now
		}
	}
	return before, after
}

// LockAvailableTargetIn takes a shared lock on a target inside a caller's
// transaction and refuses one that is not available. Binding and offering
// take it before writing their own rows; retirement takes the target's
// update lock, so no new binding or offer can slip past its decision.
func LockAvailableTargetIn(ctx context.Context, tx *database.Tx, id ids.ID) error {
	t, err := targetRecord(deploymentsql.New(tx.SQL()).TargetForShare(ctx, id))
	if err != nil {
		return err
	}
	if err := t.Status.Available(); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrTargetConflict, t.Name, err)
	}
	return nil
}

func offerRecord(r deploymentsql.TargetOffer) (Offer, error) {
	o := Offer{ID: r.ID, TargetID: r.TargetID, Group: r.GroupName, CreatedBy: r.CreatedBy, CreatedByPrincipal: r.CreatedByPrincipal, CreatedAt: r.CreatedAt}
	err := json.Unmarshal(r.CapsJson, &o.Caps)
	return o, err
}

// Offers lists a target's offers, by group.
func (s *Targets) Offers(ctx context.Context, targetID ids.ID) ([]Offer, error) {
	return readOffers(ctx, deploymentsql.New(s.db.SQL()), targetID)
}
func readOffers(ctx context.Context, q *deploymentsql.Queries, targetID ids.ID) ([]Offer, error) {
	rows, err := q.OffersForTarget(ctx, targetID)
	if err != nil {
		return nil, err
	}
	out := make([]Offer, 0, len(rows))
	for _, r := range rows {
		o, err := offerRecord(r)
		if err != nil {
			return nil, fmt.Errorf("reading offer %d caps: %w", r.ID, err)
		}
		out = append(out, o)
	}
	return out, nil
}

// Offer makes a target available to a group with caps; offering again
// replaces the caps. Refused for a target that is not available.
func (s *Targets) Offer(ctx context.Context, t *Target, group string, caps OfferCaps, who string) error {
	now := s.now()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		if err := LockAvailableTargetIn(ctx, tx, t.ID); err != nil {
			return err
		}
		offerer, err := identity.ActorIn(ctx, tx, who)
		if err != nil {
			return err
		}
		if _, err := deploymentsql.New(tx.SQL()).PutOffer(ctx, deploymentsql.PutOfferParams{ID: ids.New(now), TargetID: t.ID, GroupName: group, CapsJson: json.RawMessage(jsonText(caps)), CreatedBy: who, CreatedByPrincipal: offerer.Principal, CreatedByVia: offerer.Via, CreatedAt: now}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "target.offered", t.Name, map[string]any{"group": group, "caps": caps})
	})
}

// RevokeOffer withdraws one offer and records who did it; the group it was
// to comes back for the caller's answer.
func (s *Targets) RevokeOffer(ctx context.Context, t *Target, offerID ids.ID, who string) (string, error) {
	var group string
	err := s.db.InTx(ctx, func(tx *database.Tx) error {
		if _, err := deploymentsql.New(tx.SQL()).TargetForShare(ctx, t.ID); err != nil {
			return err
		}
		r, err := deploymentsql.New(tx.SQL()).RevokeOffer(ctx, deploymentsql.RevokeOfferParams{ID: offerID, TargetID: t.ID})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrOfferMissing
			}
			return err
		}
		group = r.GroupName
		return audit.At(s.now()).RecordIn(ctx, tx, who, ids.Nil, "target.offer-withdrawn", t.Name, map[string]any{"group": group})
	})
	return group, err
}

// RecordCheck stores a check's answer as the state TargetState.Checked
// chose, with the answer as its receipt. The conditional write refuses a
// target retired since the caller read it.
func (s *Targets) RecordCheck(ctx context.Context, t *Target, answered bool, who string, payload any) (TargetState, error) {
	now := s.now()
	next, err := t.Status.Checked(answered)
	if err != nil {
		return 0, fmt.Errorf("%w: %s: %w", ErrTargetConflict, t.Name, err)
	}
	return next, s.db.InTx(ctx, func(tx *database.Tx) error {
		if _, err := deploymentsql.New(tx.SQL()).CheckTarget(ctx, deploymentsql.CheckTargetParams{Status: next.String(), UpdatedAt: now, ID: t.ID, RetiredState: TargetRetired.String()}); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: could not record the check for %s; it may have been retired", ErrTargetConflict, t.Name)
			}
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "target.checked", t.Name, payload)
	})
}

// Retire takes a target out of service: refused for the built-in host and
// while any standing deployment is bound to it (they are named), otherwise
// the state, the withdrawal of every offer, the receipt and the release of
// an unused cloud connector commit together. Retiring a retired target is
// already done and changes nothing.
func (s *Targets) Retire(ctx context.Context, name, who string) error {
	now := s.now()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		// Serialize target mutations so two sibling retirements cannot each
		// retain the last cloud connector, and creation cannot race its
		// unused-provider check. Rare and short.
		if err := deploymentsql.New(tx.SQL()).LockTargets(ctx); err != nil {
			return err
		}
		t, err := targetRecord(deploymentsql.New(tx.SQL()).TargetByNameForUpdate(ctx, name))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTargetMissing
		}
		if err != nil {
			return err
		}
		bound, err := deploymentsql.New(tx.SQL()).BoundTargetDeployments(ctx, deploymentsql.BoundTargetDeploymentsParams{TargetID: t.ID, Kind: Standing.String()})
		if err != nil {
			return err
		}
		boundNames, _ := bound.(string)
		next, err := t.Retire(boundNames)
		if errors.Is(err, ErrTargetRetired) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := deploymentsql.New(tx.SQL()).RetireTarget(ctx, deploymentsql.RetireTargetParams{Status: next.String(), UpdatedAt: now, ID: t.ID}); err != nil {
			return err
		}
		counts, err := deploymentsql.New(tx.SQL()).RemoveTargetOffers(ctx, t.ID)
		if err != nil {
			return err
		}
		selected, removed := counts.SelectedCount, counts.RemovedCount
		if selected != removed {
			return fmt.Errorf("target %s offers were not removed", name)
		}
		if err := audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "target.retired", name, map[string]any{"kind": t.Kind}); err != nil {
			return err
		}
		if t.Kind == KindDOApp && t.Provider != "" && s.connectors != nil {
			// LockTargets excludes every INSERT/UPDATE/DELETE until commit,
			// so this observation remains true while the connector is removed.
			used, err := deploymentsql.New(tx.SQL()).ProviderHasLiveTargets(ctx, deploymentsql.ProviderHasLiveTargetsParams{Provider: t.Provider, Status: TargetRetired.String()})
			if err != nil {
				return err
			}
			if !used {
				referenced, err := deploymentsql.New(tx.SQL()).ConnectorHasRetainedIdentity(ctx, t.Provider)
				if err != nil {
					return err
				}
				if referenced {
					return nil
				}
				return s.connectors.RetireUnusedConnector(ctx, tx, t.Provider, name, who, now)
			}
		}
		return nil
	})
}

// ErrTargetKeepsBackups is a target whose deployments AppTown does not
// back up: a scheduled backup there is skipped, not failed.
var ErrTargetKeepsBackups = errors.New("it keeps its own backups; AppTown backs up what it holds on a Docker host or a Portainer environment it talks to")

// DataAccess is how AppTown reaches the data of a target's deployments
// for its own copies: on a Docker host it talks to; in a Portainer
// environment, through Portainer's Docker proxy (Providers.PortainerDataScope);
// on a managed platform, through the provider and the target's copy runner;
// or not at all, on a target that holds no data (a bucket website). Data
// work asks this, not the target's kind.
type DataAccess uint8

const (
	DataAccessNone DataAccess = iota
	DataOnDockerHost
	DataThroughPortainer
	DataOnManagedPlatform
)

// DataAccess is how AppTown reaches this target's deployments' data.
func (t *Target) DataAccess() DataAccess {
	switch t.Kind {
	case KindDockerHost:
		return DataOnDockerHost
	case KindPortainer:
		return DataThroughPortainer
	case KindDOApp:
		return DataOnManagedPlatform
	}
	return DataAccessNone
}

// HoldsBackups says whether AppTown backs up what stands on this target
// as sets of its own: what it holds on a Docker host it talks to, or in a
// Portainer environment; a cloud target is copied by operations of its own
// (ErrTargetKeepsBackups).
func (t *Target) HoldsBackups() error {
	if a := t.DataAccess(); a != DataOnDockerHost && a != DataThroughPortainer {
		return fmt.Errorf("a %s target: %w", t.Kind, ErrTargetKeepsBackups)
	}
	return nil
}
