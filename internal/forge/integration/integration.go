package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/integration/internal/integrationsql"
)

// Binding is one row of integrations, its configuration as JSON text:
// capability adapters decode their own configuration, and doors redact
// what must not leave the engine.
type Binding struct {
	ID         ids.ID
	Name       string
	Capability string
	Provider   string
	ConfigJSON string
	Version    int
	Managed    bool // forge's own, re-synced from configuration; not a person's to edit
	UpdatedAt  string
}

var (
	ErrManaged  = fault.New(fault.Conflict, "a managed binding is forge's and re-syncs with its configuration; clone it under a new name")
	ErrAttached = fault.New(fault.Conflict, "projects still reference the binding; detach them first")
	ErrInUse    = fault.New(fault.Conflict, "projects still hold an integration of this kind; detach them first")
	// ErrNoBinding and ErrNoServiceKind are the absence of what a caller
	// named, classified here so callers never read sql.ErrNoRows.
	ErrNoBinding     = fault.New(fault.NotFound, "no binding is named")
	ErrNoServiceKind = fault.New(fault.NotFound, "no service kind has the capability")
)

// Bindings is the supported API over the instance's bindings.
type Bindings struct {
	db    *database.DB
	clock func() time.Time // AppTown's clock, which stamps versions; nil is the real one
	// committed runs once a save's binding has committed, before its
	// receipt: a test stops the caller there.
	committed func()
}

func New(db *database.DB) *Bindings { return &Bindings{db: db} }

func (s *Bindings) now() time.Time { return clock.Or(s.clock)().UTC() }

func bindingRecord(r integrationsql.Integration) Binding {
	return Binding{ID: r.ID, Name: r.Name, Capability: r.Capability, Provider: r.Provider, ConfigJSON: string(r.ConfigJson), Version: int(r.Version), Managed: r.Managed, UpdatedAt: r.UpdatedAt.Format(time.RFC3339Nano)}
}

func (s *Bindings) ByName(ctx context.Context, name string) (*Binding, error) {
	r, err := integrationsql.New(s.db.SQL()).BindingByName(ctx, name)
	if err != nil {
		return nil, err
	}
	b := bindingRecord(r)
	return &b, nil
}

// All lists the bindings by capability and name.
func (s *Bindings) All(ctx context.Context) ([]Binding, error) {
	rows, err := integrationsql.New(s.db.SQL()).Bindings(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Binding, 0, len(rows))
	for _, r := range rows {
		out = append(out, bindingRecord(r))
	}
	return out, nil
}

// Selected chooses a named binding, or the oldest of its capability.
func Selected(ctx context.Context, q database.Queryer, capability, name string) (Binding, error) {
	rows, err := integrationsql.New(q.SQL()).Bindings(ctx)
	if err != nil {
		return Binding{}, err
	}
	var selected *integrationsql.Integration
	for i := range rows {
		r := &rows[i]
		if r.Capability == capability && (name == "" || r.Name == name) && (selected == nil || r.ID.Compare(selected.ID) < 0) {
			selected = r
		}
	}
	if selected == nil {
		return Binding{}, sql.ErrNoRows
	}
	return bindingRecord(*selected), nil
}

// ForCapability is every binding of a capability, oldest first.
func ForCapability(ctx context.Context, q database.Queryer, capability string) ([]Binding, error) {
	rows, err := integrationsql.New(q.SQL()).Bindings(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID.Compare(rows[j].ID) < 0 })
	out := make([]Binding, 0)
	for _, r := range rows {
		if r.Capability == capability {
			out = append(out, bindingRecord(r))
		}
	}
	return out, nil
}

// WithProviders keeps, in order, the bindings whose provider is in
// providers: for secrets, the stores the secret owner says are available.
func WithProviders(bs []Binding, providers map[string]bool) []Binding {
	out := make([]Binding, 0, len(bs))
	for _, b := range bs {
		if providers[b.Provider] {
			out = append(out, b)
		}
	}
	return out
}

// Count is how many bindings carry a name for a capability (0 or 1).
func (s *Bindings) Count(ctx context.Context, name, capability string) (int, error) {
	b, err := s.ByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if b.Capability == capability {
		return 1, nil
	}
	return 0, nil
}

// Upsert records a binding's configuration: version 1 with its version
// row for a new name, the next version when the configuration changed,
// nothing when it did not. The row and its version row commit together.
// It returns the version that stands.
func (s *Bindings) Upsert(ctx context.Context, name, capability, provider, configJSON string, managed bool) (int, error) {
	return s.upsertAt(ctx, s.now(), name, capability, provider, configJSON, managed)
}

// upsertAt is Upsert at the operation's time, now.
func (s *Bindings) upsertAt(ctx context.Context, now time.Time, name, capability, provider, configJSON string, managed bool) (int, error) {
	var version int
	err := s.db.InTx(ctx, func(tx *database.Tx) error {
		q := integrationsql.New(tx.SQL())
		current, err := q.BindingByNameForUpdate(ctx, name)
		if errors.Is(err, sql.ErrNoRows) {
			version = 1
			current, err = q.InsertBinding(ctx, integrationsql.InsertBindingParams{ID: ids.New(now), Name: name, Capability: capability, Provider: provider, ConfigJson: json.RawMessage(configJSON), Managed: managed, CreatedAt: now, UpdatedAt: now})
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			version = int(current.Version)
			if sameJSON(string(current.ConfigJson), configJSON) {
				return nil
			}
			version++
			current, err = q.SetBindingConfiguration(ctx, integrationsql.SetBindingConfigurationParams{ID: current.ID, ConfigJson: json.RawMessage(configJSON), Version: int64(version), UpdatedAt: now})
			if err != nil {
				return err
			}
		}
		_, err = q.InsertBindingVersion(ctx, integrationsql.InsertBindingVersionParams{ID: ids.New(now), IntegrationID: current.ID, Version: int64(version), ConfigJson: json.RawMessage(configJSON), CreatedAt: now})
		return err
	})
	return version, err
}

// Save records a person's binding with its receipt; a managed name, and a
// configuration its runtime could not read (ErrInvalidConfig), are refused.
func (s *Bindings) Save(ctx context.Context, name, capability, provider, configJSON, who string) (int, error) {
	if b, err := s.ByName(ctx, name); err == nil && b.Managed {
		return 0, fmt.Errorf("%q: %w", name, ErrManaged)
	}
	if err := validateConfig(capability, configJSON); err != nil {
		return 0, err
	}
	at := s.now()
	version, err := s.upsertAt(ctx, at, name, capability, provider, configJSON, false)
	if err != nil {
		return 0, err
	}
	if s.committed != nil {
		s.committed()
	}
	// The binding is committed: its receipt's whole transaction is written
	// though the caller has gone.
	settle := context.WithoutCancel(ctx)
	return version, s.db.InTx(settle, func(tx *database.Tx) error {
		return audit.At(at).RecordIn(settle, tx, who, ids.Nil, "integration.saved", name, map[string]any{"capability": capability, "provider": provider})
	})
}

// Delete removes a person's binding with its receipt: managed bindings
// are refused, and so is one that attached projects still reference (it
// is load-bearing for their provenance and env templates). Absence is
// ErrNoBinding.
func (s *Bindings) Delete(ctx context.Context, name, who string) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := integrationsql.New(tx.SQL())
		current, err := q.BindingByNameForUpdate(ctx, name)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w %q", ErrNoBinding, name)
		}
		if err != nil {
			return err
		}
		if current.Managed {
			return fmt.Errorf("%q: %w", name, ErrManaged)
		}
		linked, err := q.BindingAttachmentCount(ctx, name)
		if err != nil {
			return err
		}
		if linked > 0 {
			return fmt.Errorf("%d project integration(s) reference %q: %w", linked, name, ErrAttached)
		}
		if _, err := q.DeleteBinding(ctx, name); err != nil {
			return err
		}
		return audit.At(s.now()).RecordIn(ctx, tx, who, ids.Nil, "integration.deleted", name, nil)
	})
}

// StockCurrent is what the store holds for a seeded name: its canonical
// configuration and whether it is managed; nil when absent.
type Stock struct {
	Content string
	Managed bool
}

func (s *Bindings) StockCurrent(ctx context.Context, name string) (*Stock, error) {
	b, err := s.ByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Stock{Content: canonJSON(b.ConfigJSON), Managed: b.Managed}, nil
}

// StockInsert seeds a managed binding at version 1; StockUpdate cuts the
// next version of one. Each is one transaction.
func (s *Bindings) StockInsert(ctx context.Context, name, capability, provider, configJSON string) error {
	_, err := s.Upsert(ctx, name, capability, provider, configJSON, true)
	return err
}

func (s *Bindings) StockUpdate(ctx context.Context, name, configJSON string) (int, error) {
	var version int
	now := s.now()
	err := s.db.InTx(ctx, func(tx *database.Tx) error {
		q := integrationsql.New(tx.SQL())
		current, err := q.BindingByNameForUpdate(ctx, name)
		if err != nil {
			return err
		}
		version = int(current.Version) + 1
		_, err = q.SetBindingConfiguration(ctx, integrationsql.SetBindingConfigurationParams{ID: current.ID, ConfigJson: json.RawMessage(configJSON), Version: int64(version), UpdatedAt: now})
		if err != nil {
			return err
		}
		_, err = q.InsertBindingVersion(ctx, integrationsql.InsertBindingVersionParams{ID: ids.New(now), IntegrationID: current.ID, Version: int64(version), ConfigJson: json.RawMessage(configJSON), CreatedAt: now})
		return err
	})
	return version, err
}

// Attachment is one row of project_integrations with what its binding
// and its kind say, as stored text; AppTown decodes the kind.
type Attachment struct {
	Capability, Binding, ResourceJSON, ConfigJSON, Provenance string
	CreatedAt                                                 time.Time
	WireChangeID                                              ids.ID
	WireChangeNumber                                          int64 // within the project; 0 when none or gone
	WireState, WireTitle                                      string
	KindYAML                                                  string
}

// AttachedRows reads a project's attached integrations through q: attached
// rows only (SCM's private token cache has no attachment provenance),
// joined by capability so a stale binding name cannot select another
// kind, and kept when their connector is gone.
func AttachedRows(ctx context.Context, q database.Queryer, projectID ids.ID) ([]Attachment, error) {
	rows, err := integrationsql.New(q.SQL()).AttachedIntegrations(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]Attachment, 0, len(rows))
	for _, r := range rows {
		out = append(out, Attachment{Capability: r.Capability, Binding: r.Binding, ResourceJSON: string(r.ResourceJson), ConfigJSON: string(r.ConfigJson), Provenance: r.Provenance, CreatedAt: r.CreatedAt, WireChangeID: r.ChangeID, WireChangeNumber: r.ChangeNumber, WireState: r.ChangeState, WireTitle: r.ChangeTitle, KindYAML: r.KindYaml})
	}
	return out, err
}

// Resource is the resource a project attached for a capability, as JSON.
func Resource(ctx context.Context, q database.Queryer, projectID ids.ID, capability string) (string, error) {
	r, err := integrationsql.New(q.SQL()).ProjectIntegration(ctx, integrationsql.ProjectIntegrationParams{ProjectID: projectID, Capability: capability})
	return string(r.ResourceJson), err
}

// ServiceKind is one runtime-defined kind, as its YAML.
type ServiceKind struct {
	Capability, YAML, UpdatedAt string
}

// ServiceKinds is the supported API over service kinds.
type ServiceKinds struct {
	db    *database.DB
	clock func() time.Time // AppTown's clock; nil is the real one
}

func NewServiceKinds(db *database.DB) *ServiceKinds { return &ServiceKinds{db: db} }

func (s *ServiceKinds) All(ctx context.Context) ([]ServiceKind, error) {
	rows, err := integrationsql.New(s.db.SQL()).ServiceKinds(ctx)
	out := make([]ServiceKind, 0, len(rows))
	for _, r := range rows {
		out = append(out, ServiceKind{Capability: r.Capability, YAML: r.SpecYaml, UpdatedAt: r.UpdatedAt.Format(time.RFC3339Nano)})
	}
	return out, err
}

func (s *ServiceKinds) ByCapability(ctx context.Context, capability string) (string, error) {
	r, err := integrationsql.New(s.db.SQL()).ServiceKindByCapability(ctx, capability)
	return r.SpecYaml, err
}

// Save creates or replaces a kind with its receipt (versionless: the
// YAML is small and integrations pin nothing from it).
func (s *ServiceKinds) Save(ctx context.Context, capability, yamlSrc, title, who string) error {
	// The owner holds the definition rules, whoever calls: a value marked for
	// browsers must never come from a secret.
	kind, err := ParseDefinition(yamlSrc)
	if err != nil {
		return fault.Wrap(fault.Unprocessable, err)
	}
	if kind.Capability != capability {
		return fault.New(fault.Unprocessable, "the definition names capability "+kind.Capability+", not "+capability)
	}
	now := clock.Or(s.clock)().UTC()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		if _, err := integrationsql.New(tx.SQL()).SaveServiceKind(ctx, integrationsql.SaveServiceKindParams{Capability: capability, SpecYaml: yamlSrc, CreatedAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "service-kind.saved", capability, map[string]any{"title": title})
	})
}

// Delete removes a kind with its receipt; refused while any project holds
// an integration of the kind; absence is ErrNoServiceKind. The delete's
// count decides absence here: the kind row is the one the caller named,
// and no row deleted means there was none, not an unrecorded write.
func (s *ServiceKinds) Delete(ctx context.Context, capability, who string) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := integrationsql.New(tx.SQL())
		inUse, err := q.KindAttachmentCount(ctx, capability)
		if err != nil {
			return err
		}
		if inUse > 0 {
			return fmt.Errorf("%d project(s) still have a %s integration: %w", inUse, capability, ErrInUse)
		}
		n, err := q.DeleteServiceKind(ctx, capability)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w %q", ErrNoServiceKind, capability)
		}
		return audit.At(clock.Or(s.clock)()).RecordIn(ctx, tx, who, ids.Nil, "service-kind.deleted", capability, nil)
	})
}

func canonJSON(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func sameJSON(a, b string) bool { return canonJSON(a) == canonJSON(b) }

// Provenance says how a project's attachment came to be: entered by a
// person, provisioned by AppTown at the provider, or "" for an
// engine-internal row (the SCM token cache) that is not an attachment.
type Provenance string

const (
	Entered     Provenance = "entered"
	Provisioned Provenance = "provisioned"
)

var ErrNotAttached = errors.New("the project has no such integration")

// Attachments is the supported API over a project's attachments.
type Attachments struct {
	db    *database.DB
	clock func() time.Time // AppTown's clock; nil is the real one
}

func NewAttachments(db *database.DB) *Attachments { return &Attachments{db: db} }

// Attach records (or replaces) a project's attachment of a binding for a
// capability with its resource and provenance, and the
// integration.attached receipt, in one transaction.
func (s *Attachments) Attach(ctx context.Context, projectID ids.ID, capability, binding, resourceJSON string, provenance Provenance, who string, receipt map[string]any) error {
	if provenance == "" {
		return errors.New("an attachment needs a provenance")
	}
	if err := refusesIntegrations(ctx, s.db, projectID); err != nil {
		return err
	}
	at := clock.Or(s.clock)()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		if _, err := integrationsql.New(tx.SQL()).AttachIntegration(ctx, integrationsql.AttachIntegrationParams{ProjectID: projectID, Capability: capability, Binding: binding, ResourceJson: json.RawMessage(resourceJSON), Provenance: string(provenance), CreatedAt: at.UTC()}); err != nil {
			return err
		}
		payload := map[string]any{"connector": binding, "provenance": string(provenance)}
		for k, v := range receipt {
			payload[k] = v
		}
		return audit.At(at).RecordIn(ctx, tx, who, projectID, "integration.attached", capability, payload)
	})
}

// Remember keeps an engine-internal row (no provenance): a resource the
// AppTown holds for a project, replaced whole when it changes.
func (s *Attachments) Remember(ctx context.Context, projectID ids.ID, capability, binding, resourceJSON string) error {
	if err := refusesIntegrations(ctx, s.db, projectID); err != nil {
		return err
	}
	_, err := integrationsql.New(s.db.SQL()).RememberIntegration(ctx, integrationsql.RememberIntegrationParams{ProjectID: projectID, Capability: capability, Binding: binding, ResourceJson: json.RawMessage(resourceJSON), CreatedAt: clock.Or(s.clock)().UTC()})
	return err
}

// Detach removes a project's attachment; the provider-side resource is
// never deprovisioned here. Nothing attached is ErrNotAttached.
func (s *Attachments) Detach(ctx context.Context, projectID ids.ID, capability string) error {
	n, err := integrationsql.New(s.db.SQL()).DetachIntegration(ctx, integrationsql.DetachIntegrationParams{ProjectID: projectID, Capability: capability})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotAttached
	}
	return nil
}

// AttachedProject is a project that attaches a binding.
type AttachedProject struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Capability string `json:"capability"`
}

// ProjectsByBinding is, for each binding name, the projects that attach it.
// Engine-internal rows are not attachments, and an attachment naming a
// binding that no longer exists belongs to none.
func (s *Attachments) ProjectsByBinding(ctx context.Context) (map[string][]AttachedProject, error) {
	rows, err := integrationsql.New(s.db.SQL()).BindingUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]AttachedProject{}
	for _, r := range rows {
		out[r.Binding] = append(out[r.Binding], AttachedProject{Slug: r.Slug, Name: r.ProjectName, Capability: r.Capability})
	}
	return out, nil
}

// IsAttached reports whether a project has an attachment for a capability.
func (s *Attachments) IsAttached(ctx context.Context, projectID ids.ID, capability string) (bool, error) {
	_, _, err := s.Attached(ctx, projectID, capability)
	if errors.Is(err, ErrNotAttached) {
		return false, nil
	}
	return err == nil, err
}

// Attached is a project's attachment for a capability. Engine-internal rows
// have no provenance and are not attachments.
func (s *Attachments) Attached(ctx context.Context, projectID ids.ID, capability string) (binding, resourceJSON string, err error) {
	r, err := integrationsql.New(s.db.SQL()).ProjectIntegration(ctx, integrationsql.ProjectIntegrationParams{ProjectID: projectID, Capability: capability})
	if errors.Is(err, sql.ErrNoRows) || (err == nil && r.Provenance == "") {
		return "", "", ErrNotAttached
	}
	return r.Binding, string(r.ResourceJson), err
}

// WireOpenError refuses a second wiring Change while the attachment's current
// one is still live. Only an abandoned wiring (or none) may be replaced: a retry
// after a lost response must find the work it already admitted, not add more.
type WireOpenError struct {
	Capability   string
	ChangeID     ids.ID
	ChangeNumber int64 // within the project; 0 when it no longer reads
	State        string
}

func (e *WireOpenError) Error() string {
	change := fmt.Sprintf("change %d", e.ChangeNumber)
	if e.ChangeNumber == 0 {
		change = fmt.Sprintf("change %s", e.ChangeID)
	}
	return fmt.Sprintf("%s already wires %s into the app (%s); open it, or abandon it before starting another", change, e.Capability, e.State)
}
func (e *WireOpenError) FaultKind() fault.Kind { return fault.Conflict }

// LinkWireIn records changeID as the Change that wires an attachment into the
// app, inside the caller's admission transaction. It locks the attachment row,
// refuses with *WireOpenError while a linked Change is live (not abandoned),
// and answers a not-found fault when the project has no such attachment. The
// caller must roll back its admission on any error; this writes nothing else.
func LinkWireIn(ctx context.Context, tx *database.Tx, projectID ids.ID, capability string, changeID ids.ID) error {
	q := integrationsql.New(tx.SQL())
	current, err := q.AttachmentWireForUpdate(ctx, integrationsql.AttachmentWireForUpdateParams{ProjectID: projectID, Capability: capability})
	if errors.Is(err, sql.ErrNoRows) {
		return fault.Wrap(fault.NotFound, fmt.Errorf("%s is not attached to this project", capability))
	}
	if err != nil {
		return err
	}
	// A link to a Change that no longer reads (state "") is no live work. The
	// state names are Work's; this owner compares only the one it may replace.
	if !current.WireChangeID.IsZero() && current.ChangeState != "" && current.ChangeState != "abandoned" {
		return &WireOpenError{Capability: capability, ChangeID: current.WireChangeID, ChangeNumber: current.ChangeNumber, State: current.ChangeState}
	}
	n, err := q.LinkIntegrationWire(ctx, integrationsql.LinkIntegrationWireParams{ProjectID: projectID, Capability: capability, WireChangeID: changeID})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("the wiring link for %s was not recorded", capability)
	}
	return nil
}
