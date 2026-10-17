package wow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/wow/internal/wowsql"
	"flokkit.io/forge/internal/stamp"
	"flokkit.io/forge/internal/strs"
)

// The catalog: canned, versioned change definitions. A new entry and a new
// version are drafts until promoted; promotion is the claim "this has been
// run and watched" and needs a note naming the evidence, like every
// override. CatalogState is derived by CatalogStateOf.

type CatalogParam struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Default     string `json:"default,omitempty"`
}

type CatalogDef struct {
	Params []CatalogParam `json:"params,omitempty"`
	// Prompt template — {{name}} placeholders filled from params. Becomes
	// the change body its workflow's agent receives.
	Prompt   string `json:"prompt"`
	Workflow string `json:"workflow"`        // the workflow that runs it
	Title    string `json:"title,omitempty"` // change title template; default "<name>"
	Type     string `json:"type,omitempty"`  // change type; default "chore"
	// Model pin: wins over project slots. Part of the versioned definition.
	Model string `json:"model,omitempty"`
	// Whitelisted workflow knobs — effort, never gates.
	Thinking       string `json:"thinking,omitempty"`
	TimeoutMinutes int    `json:"timeoutMinutes,omitempty"`
}

type CatalogChange struct {
	ID              ids.ID `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	LatestVersion   int    `json:"latestVersion"`
	PromotedVersion int    `json:"promotedVersion"` // 0 = never promoted
	PromotedBy      string `json:"promotedBy,omitempty"`
	// PromotedByPrincipal is the promoter's principal; zero when not resolved to a person.
	PromotedByPrincipal ids.ID     `json:"promotedByPrincipal,omitzero"`
	PromotedAt          stamp.Time `json:"promotedAt,omitzero"`
	PromotionNote       string     `json:"promotionNote,omitempty"`
	CreatedAt           string     `json:"createdAt"`
	UpdatedAt           string     `json:"updatedAt"`
}

// State is the page's word for CatalogStateOf.
func (c *CatalogChange) State() CatalogState {
	return CatalogStateOf(c.LatestVersion, c.PromotedVersion)
}

var (
	ErrCatalogName    = fault.New(fault.Invalid, "name must look like uplift/go-version")
	ErrPromotionNote  = fault.New(fault.Invalid, "promotion needs a note naming the evaluation evidence — like every override")
	ErrCatalogMissing = errors.New("no catalog change named")
)

// Catalog is the supported API over catalog changes and their versions.
type Catalog struct {
	db    *database.DB
	clock func() time.Time // the forge's clock; nil is the real one
}

func (s *Catalog) now() time.Time { return clock.Or(s.clock)() }

func NewCatalog(db *database.DB) *Catalog { return &Catalog{db: db} }

func (s *Catalog) ByName(ctx context.Context, name string) (*CatalogChange, error) {
	r, err := wowsql.New(s.db.SQL()).CatalogByName(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("%w %q", ErrCatalogMissing, name)
	}
	c := catalogRecord(r)
	return &c, nil
}

// All lists the catalog by name.
func (s *Catalog) All(ctx context.Context) ([]CatalogChange, error) {
	rows, err := wowsql.New(s.db.SQL()).CatalogChanges(ctx)
	out := make([]CatalogChange, 0, len(rows))
	for _, r := range rows {
		out = append(out, catalogRecord(r))
	}
	return out, err
}

// Definition is one version's definition.
func (s *Catalog) Definition(ctx context.Context, id ids.ID, version int) (*CatalogDef, error) {
	r, err := wowsql.New(s.db.SQL()).CatalogVersion(ctx, wowsql.CatalogVersionParams{CatalogID: id, Version: int64(version)})
	if err != nil {
		return nil, err
	}
	var def CatalogDef
	if err := json.Unmarshal(r.DefinitionJson, &def); err != nil {
		return nil, err
	}
	return &def, nil
}

// Create records a new draft at version 1 with its receipt, in one
// transaction. The definition must already be valid.
func (s *Catalog) Create(ctx context.Context, name, description string, def CatalogDef, who string) (*CatalogChange, error) {
	now := s.now().UTC()
	if !ValidName(name) {
		return nil, fmt.Errorf("%w (%s)", ErrCatalogName, NameShape)
	}
	var change CatalogChange
	definition, err := strs.JSON(def)
	if err != nil {
		return nil, err
	}
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		q := wowsql.New(tx.SQL())
		r, err := q.InsertCatalogChange(ctx, wowsql.InsertCatalogChangeParams{ID: ids.New(now), Name: name, Description: description, At: now})
		if err != nil {
			return err
		}
		change = catalogRecord(r)
		if _, err := q.InsertCatalogVersion(ctx, wowsql.InsertCatalogVersionParams{ID: ids.New(now), CatalogID: r.ID, Version: 1, Definition: []byte(definition), At: now}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "catalog.created", name, map[string]any{"catalogId": r.ID})
	})
	if err != nil {
		return nil, err
	}
	return &change, nil
}

// Bump appends a version under the row lock (concurrent edits get distinct
// numbers); the version, the pointer and the receipt commit together. The
// new version is a draft until promoted.
func (s *Catalog) Bump(ctx context.Context, id ids.ID, name string, def CatalogDef, who string) error {
	now := s.now()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := wowsql.New(tx.SQL())
		r, err := q.CatalogByIDForUpdate(ctx, id)
		if err != nil {
			return err
		}
		return bumpCatalogIn(ctx, tx, r, def, who, now)
	})
}

// Promote promotes the latest version with the note that names its
// evidence; the promotion and its receipt commit together.
func (s *Catalog) Promote(ctx context.Context, name, note, who string) (*CatalogChange, error) {
	now := s.now().UTC()
	if strings.TrimSpace(note) == "" {
		return nil, ErrPromotionNote
	}
	err := s.db.InTx(ctx, func(tx *database.Tx) error {
		q := wowsql.New(tx.SQL())
		r, err := q.CatalogByNameForUpdate(ctx, name)
		if err != nil {
			return fmt.Errorf("%w %q", ErrCatalogMissing, name)
		}
		promoter, err := identity.ActorIn(ctx, tx, who)
		if err != nil {
			return err
		}
		if _, err := q.PromoteCatalogChange(ctx, wowsql.PromoteCatalogChangeParams{ID: r.ID, Version: r.LatestVersion, Actor: who, ActorPrincipal: promoter.Principal, ActorVia: promoter.Via, At: sql.NullTime{Time: now, Valid: true}, Note: note}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "catalog.promoted", name, map[string]any{"version": r.LatestVersion, "note": note})
	})
	if err != nil {
		return nil, err
	}
	return s.ByName(ctx, name)
}

func catalogRecord(r wowsql.CatalogChange) CatalogChange {
	return CatalogChange{ID: r.ID, Name: r.Name, Description: r.Description, LatestVersion: int(r.LatestVersion), PromotedVersion: int(r.PromotedVersion), PromotedBy: r.PromotedBy, PromotedByPrincipal: r.PromotedByPrincipal, PromotedAt: stamp.Time{NullTime: r.PromotedAt}, PromotionNote: r.PromotionNote, CreatedAt: r.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: r.UpdatedAt.Format(time.RFC3339Nano)}
}

// Validate is what a definition must have to be saved: the workflow that
// runs it and uniquely named params. An absent type is "chore".
func (d *CatalogDef) Validate() error {
	if strings.TrimSpace(d.Workflow) == "" {
		return fmt.Errorf("definition needs a workflow (the one that runs it)")
	}
	seen := map[string]bool{}
	for _, p := range d.Params {
		if strings.TrimSpace(p.Name) == "" {
			return fmt.Errorf("every param needs a name")
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate param %q", p.Name)
		}
		seen[p.Name] = true
	}
	if d.Type == "" {
		d.Type = "chore"
	}
	return nil
}

// Render fills {{name}} placeholders in one of the definition's templates.
// Unknown params are an error (a typo must not silently reach the agent);
// missing required params are an error; optional params fall back to
// their defaults.
func (d *CatalogDef) Render(tmpl string, given map[string]string) (string, error) {
	known := map[string]bool{}
	out := tmpl
	for _, p := range d.Params {
		known[p.Name] = true
		v, ok := given[p.Name]
		if !ok || v == "" {
			if p.Required {
				return "", fmt.Errorf("param %q is required", p.Name)
			}
			v = p.Default
		}
		out = strings.ReplaceAll(out, "{{"+p.Name+"}}", v)
	}
	for k := range given {
		if !known[k] {
			return "", fmt.Errorf("unknown param %q", k)
		}
	}
	if i := strings.Index(out, "{{"); i >= 0 {
		if j := strings.Index(out[i:], "}}"); j >= 0 {
			return "", fmt.Errorf("template placeholder %s has no param", out[i:i+j+2])
		}
	}
	return out, nil
}

// RunInputs is what a run started from the catalog carries: which
// definition at which version applied with which params, and the
// definition's effort knobs (model pin, thinking, timeout — never gates).
func (c *CatalogChange) RunInputs(def *CatalogDef, params map[string]string) map[string]any {
	knobs := map[string]any{
		"name":    fmt.Sprintf("%s@%d", c.Name, c.LatestVersion),
		"status":  c.State().String(),
		"applied": params,
	}
	if def.Model != "" {
		knobs["model"] = def.Model
	}
	if def.Thinking != "" {
		knobs["thinking"] = def.Thinking
	}
	if def.TimeoutMinutes > 0 {
		knobs["timeoutMinutes"] = def.TimeoutMinutes
	}
	return map[string]any{"catalog": knobs}
}

// CatalogKnobs reads the catalog's effort knobs back out of a run's
// inputs, nil when the run was not started from the catalog.
func CatalogKnobs(inputsJSON string) *CatalogDef {
	if inputsJSON == "" {
		return nil
	}
	var in struct {
		Catalog *struct {
			Model          string `json:"model"`
			Thinking       string `json:"thinking"`
			TimeoutMinutes int    `json:"timeoutMinutes"`
		} `json:"catalog"`
	}
	if json.Unmarshal([]byte(inputsJSON), &in) != nil || in.Catalog == nil {
		return nil
	}
	return &CatalogDef{Model: in.Catalog.Model, Thinking: in.Catalog.Thinking, TimeoutMinutes: in.Catalog.TimeoutMinutes}
}
