package adoption

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/stamp"
)

type runtimeCatalog struct {
	blocks        map[int]wow.BlockObservation
	workflows     map[int]wow.WorkflowObservation
	blockReads    []wow.BlockRef
	workflowReads []wow.WorkflowRef
}

func (c *runtimeCatalog) Block(ctx context.Context, ref wow.BlockRef) (wow.BlockObservation, error) {
	c.blockReads = append(c.blockReads, ref)
	if ref.Version == 0 && ref.AsOf.IsZero() {
		return wow.BlockObservation{Block: wow.Block{ID: idstest.N(7), Name: "rules/plain", LatestVersion: 2}}, nil
	}
	version := ref.Version
	if !ref.AsOf.IsZero() {
		version = 1
	}
	if b, ok := c.blocks[version]; ok {
		return b, nil
	}
	return wow.BlockObservation{}, sql.ErrNoRows
}
func (c *runtimeCatalog) Workflow(ctx context.Context, ref wow.WorkflowRef) (wow.WorkflowObservation, error) {
	c.workflowReads = append(c.workflowReads, ref)
	if ref.MetadataOnly {
		return wow.WorkflowObservation{Name: ref.Name, Origin: "stock"}, nil
	}
	if w, ok := c.workflows[ref.Version]; ok {
		return w, nil
	}
	return wow.WorkflowObservation{}, sql.ErrNoRows
}
func (c *runtimeCatalog) Workflows(ctx context.Context) ([]wow.WorkflowEntry, error) { return nil, nil }
func (*runtimeCatalog) Withdrawn(wow.Kind, string, string) bool                      { return false }

func (c *runtimeCatalog) PackPublication(context.Context, string, int64) (wow.PackVersion, error) {
	return wow.PackVersion{}, sql.ErrNoRows
}

func TestRuntimeSourcesUseExactPinsAndRejectForeignReceipts(t *testing.T) {
	t.Parallel()
	cat := &runtimeCatalog{blocks: map[int]wow.BlockObservation{1: {Version: 1, Body: " old rule \n", ChecksJSON: `[{"name":"old-check","title":"Old title","expects":"old expectation","sh":"true","files":["README.md"]}]`}, 2: {Version: 2, Body: "new rule", ChecksJSON: `[{"name":"new-check"}]`}}, workflows: map[int]wow.WorkflowObservation{1: {Name: "build", Version: 1, YAML: "old workflow"}, 2: {Name: "build", Version: 2, YAML: "new workflow"}}}
	revision := &RecordedRevision{ProjectID: idstest.N(3), Revision: 11, Receipt: Receipt{Guidance: []ReceiptBlock{{Name: "rules/plain", BlockID: idstest.N(7), Version: 1}, {Name: "disabled", BlockID: idstest.N(9), Version: 99, Disabled: true}}, Workflows: []ReceiptWorkflow{{Name: "build", Version: 1}}}}
	s := New(nil, Dependencies{Ways: cat})
	source := Source{ProjectID: idstest.N(3), Revision: revision}
	resolved, err := s.Resolve(t.Context(), source)
	if err != nil || resolved.Text != "# rules/plain v1\n\nold rule" || resolved.Hash == "" || len(resolved.Imports) != 2 {
		t.Fatalf("pinned guidance: %+v %v", resolved, err)
	}
	checks, err := s.Checks(t.Context(), source)
	if err != nil || len(checks) != 1 || checks[0].Version != 1 || checks[0].Check.Name != "old-check" || checks[0].Check.Title != "Old title" || len(checks[0].Check.Files) != 1 {
		t.Fatalf("pinned declaration: %+v %v", checks, err)
	}
	if version, yaml, err := s.WorkflowVersion(t.Context(), source, "build"); err != nil || version != 1 || yaml != "old workflow" {
		t.Fatalf("pinned procedure: %d %q %v", version, yaml, err)
	}
	// A workflow the project has not adopted is refused in the asker's
	// words (apptown plan on a pack with no planning workflow).
	if _, _, err := s.WorkflowVersion(t.Context(), source, "plan-execute"); err == nil || err.Error() != `"plan-execute" is not one of this project's ways of working: Settings › Way of working lists the workflows its packs offer` || fault.KindOf(err) != fault.Unprocessable {
		t.Fatalf("a workflow the adoption does not have: %v", err)
	}
	for _, invalid := range []Source{{ProjectID: idstest.N(3)}, {ProjectID: idstest.N(4), Revision: revision}} {
		beforeBlocks, beforeWorkflows := len(cat.blockReads), len(cat.workflowReads)
		if _, err := s.Resolve(t.Context(), invalid); err == nil {
			t.Fatal("guidance accepted missing/foreign receipt")
		}
		if _, err := s.Checks(t.Context(), invalid); err == nil {
			t.Fatal("checks accepted missing/foreign receipt")
		}
		if _, _, err := s.WorkflowVersion(t.Context(), invalid, "build"); err == nil {
			t.Fatal("procedure accepted missing/foreign receipt")
		}
		if _, _, err := s.ResolveBlock(t.Context(), invalid, "rules/plain"); err == nil {
			t.Fatal("resource accepted missing/foreign receipt")
		}
		if len(cat.blockReads) != beforeBlocks || len(cat.workflowReads) != beforeWorkflows {
			t.Fatal("rejected scope reached catalog")
		}
	}
	delete(cat.blocks, 1)
	delete(cat.workflows, 1)
	if _, err := s.Resolve(t.Context(), source); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing guidance pin fell forward: %v", err)
	}
	if checks, err := s.Checks(t.Context(), source); checks != nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing check pin fell forward: %+v %v", checks, err)
	}
	if _, _, err := s.WorkflowVersion(t.Context(), source, "build"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing workflow pin fell forward: %v", err)
	}
}

func TestUnimportedResourcesResolveAtReceiptTime(t *testing.T) {
	t.Parallel()
	at := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	cat := &runtimeCatalog{blocks: map[int]wow.BlockObservation{1: {Version: 1, Body: "old resource"}, 2: {Version: 2, Body: "new resource"}}}
	revision := &RecordedRevision{ProjectID: idstest.N(3), Revision: 8, CreatedAt: stamp.Time{NullTime: sql.NullTime{Time: at, Valid: true}}}
	s := New(nil, Dependencies{Ways: cat})
	if body, version, err := s.ResolveBlock(t.Context(), Source{ProjectID: idstest.N(3), Revision: revision}, "rules/plain"); err != nil || version != 1 || body != "old resource" {
		t.Fatalf("historical resource: %q %d %v", body, version, err)
	}
	if got := cat.blockReads[len(cat.blockReads)-1]; got.AsOf != at || got.Version != 0 {
		t.Fatalf("resource did not use receipt time: %+v", got)
	}
	delete(cat.blocks, 1)
	if _, _, err := s.ResolveBlock(t.Context(), Source{ProjectID: idstest.N(3), Revision: revision}, "rules/plain"); err == nil || !strings.Contains(err.Error(), "did not exist") {
		t.Fatalf("absent historical resource fell forward: %v", err)
	}
}

func TestRuntimeEmptyAndInvalidPinsDoNotResolveLatest(t *testing.T) {
	t.Parallel()
	s := New(nil, Dependencies{})
	source := Source{ProjectID: idstest.N(1), Revision: &RecordedRevision{ProjectID: idstest.N(1), Revision: 1, Receipt: EmptyReceipt()}}
	if resolved, err := s.Resolve(t.Context(), source); err != nil || resolved.Text != "" || resolved.Hash == "" {
		t.Fatalf("empty receipt needs no catalog: %+v %v", resolved, err)
	}
	cat := &runtimeCatalog{}
	s = New(nil, Dependencies{Ways: cat})
	source.Revision.Receipt.Guidance = []ReceiptBlock{{Name: "missing-pin", BlockID: idstest.N(7)}}
	source.Revision.Receipt.Workflows = []ReceiptWorkflow{{Name: "missing-pin"}}
	if _, err := s.Resolve(t.Context(), source); err == nil {
		t.Fatal("zero guidance pin selected metadata as contents")
	}
	if _, err := s.Checks(t.Context(), source); err == nil {
		t.Fatal("zero check pin selected metadata as contents")
	}
	if _, _, err := s.WorkflowVersion(t.Context(), source, "missing-pin"); err == nil {
		t.Fatal("zero workflow pin selected latest")
	}
	if len(cat.blockReads) != 0 || len(cat.workflowReads) != 0 {
		t.Fatal("invalid pins reached catalog")
	}
}
