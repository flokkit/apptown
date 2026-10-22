package wow

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// WorkflowEntry is a library workflow at one version, parsed.
type WorkflowEntry struct {
	Name    string
	Version int
	YAML    string
	Origin  string // "stock" or "" (yours)
	Doc     *schema.Doc
	// Withdrawn: stock the running release no longer ships (withdrawn.go).
	Withdrawn bool
}

// OriginRef names what ran: stock/quick-change@10, lib/nightly-audit@2.
func (e WorkflowEntry) OriginRef() string {
	prefix := "lib"
	if e.Origin == "stock" {
		prefix = "stock"
	}
	return fmt.Sprintf("%s/%s@%d", prefix, e.Name, e.Version)
}

// StockSystemWorkflows are AppTown's own jobs an instance may stand in for.
var StockSystemWorkflows = []string{"land", "ship", "adopt-integration", "retire-integration"}

var (
	ErrStockWorkflow     = fault.New(fault.Conflict, "an AppTown-shipped (stock) workflow is read-only and re-syncs on release; author your own under a different name")
	ErrNotSystemWorkflow = fault.New(fault.Invalid, "not a system workflow")
	// ErrNoWorkflow is the absence of the library workflow a caller named.
	ErrNoWorkflow = fault.New(fault.NotFound, "no library workflow is named")
)

// workflowByNameIn reads the workflow a caller addressed; absence is
// ErrNoWorkflow.
func workflowByNameIn(ctx context.Context, q *wowsql.Queries, name string) (wowsql.LibWorkflow, error) {
	r, err := q.WorkflowByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("%w %q", ErrNoWorkflow, name)
	}
	return r, err
}

// Workflows is the supported API over the library and the instance's
// system bindings.
type Workflows struct {
	db    *database.DB
	clock func() time.Time // AppTown's clock; nil is the real one
}

func (s *Workflows) now() time.Time { return clock.Or(s.clock)() }

func NewWorkflows(db *database.DB) *Workflows { return &Workflows{db: db} }

// Version is one version's YAML (0 = the latest), through q.
func WorkflowVersionIn(ctx context.Context, q database.Queryer, name string, version int) (int, string, error) {
	r, err := wowsql.New(q.SQL()).WorkflowVersion(ctx, wowsql.WorkflowVersionParams{Name: name, Version: int64(version)})
	if err != nil {
		return 0, "", fmt.Errorf("library workflow %q version %d: %w", name, version, err)
	}
	return int(r.Version), r.Yaml, nil
}

func (s *Workflows) Version(ctx context.Context, name string, version int) (int, string, error) {
	return WorkflowVersionIn(ctx, s.db, name, version)
}

// Origin is a workflow's origin word; absence is sql.ErrNoRows.
func (s *Workflows) Origin(ctx context.Context, name string) (string, error) {
	r, err := wowsql.New(s.db.SQL()).WorkflowByName(ctx, name)
	return r.Origin, err
}

// Latest is every library workflow at its latest version, parsed; one
// with a missing publication or malformed declaration is left out. A failed
// catalog read is an error, never an empty selection.
func (s *Workflows) Latest(ctx context.Context) ([]WorkflowEntry, error) {
	rows, err := wowsql.New(s.db.SQL()).WorkflowCatalog(ctx)
	if err != nil {
		return nil, err
	}
	var out []WorkflowEntry
	for _, r := range rows {
		if !r.HasVersion {
			continue
		}
		if doc, err := schema.ParseWorkflowStored([]byte(r.Yaml)); err == nil {
			out = append(out, WorkflowEntry{Name: r.LibWorkflow.Name, Version: int(r.LibWorkflow.LatestVersion), Origin: r.LibWorkflow.Origin, YAML: r.Yaml, Doc: doc})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CustomNames is every workflow the institution authored.
func (s *Workflows) CustomNames(ctx context.Context) ([]string, error) {
	rows, err := wowsql.New(s.db.SQL()).Workflows(ctx)
	out := []string{}
	for _, r := range rows {
		if r.Origin != "stock" {
			out = append(out, r.Name)
		}
	}
	sort.Strings(out)
	return out, err
}

// Put authors or edits a custom workflow from its YAML: version 1 for a
// new name, else the next version under the row lock; the version, the
// pointer and the receipt commit together. A stock name is refused.
func (s *Workflows) Put(ctx context.Context, yamlSrc, who string) (name string, version int, err error) {
	now := s.now().UTC()
	if strings.TrimSpace(yamlSrc) == "" {
		return "", 0, errors.New("yaml required")
	}
	doc, err := schema.ParseWorkflow([]byte(yamlSrc))
	if err != nil {
		return "", 0, err
	}
	name, desc := doc.Metadata.Name, doc.Metadata.Description
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		q := wowsql.New(tx.SQL())
		r, err := q.WorkflowByNameForUpdate(ctx, name)
		created := errors.Is(err, sql.ErrNoRows)
		if created {
			r, err = q.InsertWorkflow(ctx, wowsql.InsertWorkflowParams{ID: ids.New(now), Name: name, Description: desc, At: now})
			if err != nil {
				return err
			}
			version = 1
		} else {
			if err != nil {
				return err
			}
			if r.Origin == "stock" {
				return fmt.Errorf("%q: %w", name, ErrStockWorkflow)
			}
			rows, err := q.WorkflowVersions(ctx, r.ID)
			if err != nil {
				return err
			}
			version = nextVersion(rows)
		}
		if _, err := q.InsertWorkflowVersion(ctx, wowsql.InsertWorkflowVersionParams{ID: ids.New(now), LibID: r.ID, Version: int64(version), Yaml: yamlSrc, At: now}); err != nil {
			return err
		}
		if created {
			return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "library.workflow.created", name, nil)
		}
		if _, err := q.AdvanceWorkflowVersion(ctx, wowsql.AdvanceWorkflowVersionParams{ID: r.ID, Version: int64(version), Description: desc, At: now}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "library.workflow.updated", name, map[string]any{"version": version})
	})
	return name, version, err
}

// nextVersion follows every version, newest first, including an amendment
// of an older one that is not the latest.
func nextVersion(rows []wowsql.LibWorkflowVersion) int {
	if len(rows) == 0 {
		return 1
	}
	return int(rows[0].Version) + 1
}

// Delete removes a custom workflow and its versions with the receipt;
// stock is refused with ErrStockWorkflow, absence is ErrNoWorkflow.
// Project selections and receipts that pinned it are untouched.
func (s *Workflows) Delete(ctx context.Context, name, who string) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := wowsql.New(tx.SQL())
		r, err := workflowByNameIn(ctx, q, name)
		if err != nil {
			return err
		}
		if r.Origin == "stock" {
			return fmt.Errorf("%q: %w", name, ErrStockWorkflow)
		}
		if _, err := q.DeleteWorkflowVersions(ctx, r.ID); err != nil {
			return err
		}
		if _, err := q.DeleteWorkflow(ctx, r.ID); err != nil {
			return err
		}
		return audit.At(s.now()).RecordIn(ctx, tx, who, ids.Nil, "library.workflow.deleted", name, nil)
	})
}

// SystemFor is the instance's choice for a stock system workflow name, or
// the name itself.
func (s *Workflows) SystemFor(ctx context.Context, stockName string) string {
	r, err := wowsql.New(s.db.SQL()).SystemWorkflow(ctx, stockName)
	if err == nil && r.Name != "" {
		return r.Name
	}
	return stockName
}

// IsSystem is the library's word on whether a workflow is a system one.
func (s *Workflows) IsSystem(ctx context.Context, name string) bool {
	if _, y, err := s.Version(ctx, name, 0); err == nil {
		return declaresSystem(y)
	}
	return false
}

// systemDeclarations remembers, by the digest of a stored workflow's YAML,
// whether it says system: true. The answer is the text's alone, and a
// stored version's text never changes, so each text is parsed once per
// process however often packs are resolved against it.
var systemDeclarations sync.Map // [sha256.Size]byte -> bool

func declaresSystem(yamlSrc string) bool {
	key := sha256.Sum256([]byte(yamlSrc))
	if v, ok := systemDeclarations.Load(key); ok {
		return v.(bool)
	}
	system := false
	if d, err := schema.ParseWorkflowStored([]byte(yamlSrc)); err == nil {
		system = d.Metadata.System
	}
	systemDeclarations.Store(key, system)
	return system
}

func isStockSystem(stock string) bool {
	for _, s := range StockSystemWorkflows {
		if s == stock {
			return true
		}
	}
	return false
}

// BindSystem makes a custom workflow stand in for a stock system one. The
// stand-in must exist and say system: true; the receipt commits with it.
func (s *Workflows) BindSystem(ctx context.Context, stock, name, who string) error {
	now := s.now().UTC()
	if !isStockSystem(stock) {
		return fmt.Errorf("%q is %w", stock, ErrNotSystemWorkflow)
	}
	_, y, err := s.Version(ctx, name, 0)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w %q", ErrNoWorkflow, name)
	}
	if err != nil {
		return err
	}
	doc, err := schema.ParseWorkflowStored([]byte(y))
	if err != nil {
		return fault.Wrap(fault.Unprocessable, fmt.Errorf("%q is invalid: %w", name, err))
	}
	if !doc.Metadata.System {
		return fault.Wrap(fault.Unprocessable, fmt.Errorf("%q is not marked system: true — a workflow that stands in for %s must say so", name, stock))
	}
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		if _, err := wowsql.New(tx.SQL()).BindSystemWorkflow(ctx, wowsql.BindSystemWorkflowParams{StockName: stock, Name: name, At: now}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "system-workflow.bound", stock, map[string]any{"name": name})
	})
}

// ResetSystem restores the stock workflow for a system name.
func (s *Workflows) ResetSystem(ctx context.Context, stock, who string) error {
	if !isStockSystem(stock) {
		return fmt.Errorf("%q is %w", stock, ErrNotSystemWorkflow)
	}
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		if _, err := wowsql.New(tx.SQL()).ResetSystemWorkflow(ctx, stock); err != nil {
			return err
		}
		return audit.At(s.now()).RecordIn(ctx, tx, who, ids.Nil, "system-workflow.reset", stock, nil)
	})
}

// WorkflowListing is a workflow's current declaration for the library page.
// YAML is needed here: the page derives its role, system flag and step summary.
type WorkflowListing struct {
	Name          string
	Description   string
	LatestVersion int64
	Origin        string
	UpdatedAt     time.Time
	Yaml          string
}

type WorkflowCatalog struct {
	Workflows []WorkflowListing
}

// ReadWorkflowCatalogIn reads current workflow declarations and project selections in two bulk reads.
func ReadWorkflowCatalogIn(ctx context.Context, db database.Queryer) (WorkflowCatalog, error) {
	q := wowsql.New(db.SQL())
	var out WorkflowCatalog
	rows, err := q.WorkflowCatalog(ctx)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		l := r.LibWorkflow
		out.Workflows = append(out.Workflows, WorkflowListing{Name: l.Name, Description: l.Description, LatestVersion: l.LatestVersion, Origin: l.Origin, UpdatedAt: l.UpdatedAt, Yaml: r.Yaml})
	}
	return out, nil
}
