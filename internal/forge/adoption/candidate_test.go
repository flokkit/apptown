package adoption

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

type candidateCatalogFake struct {
	blocks           map[ids.ID]wow.Block
	versions         map[ids.ID]map[int]wow.BlockObservation
	packs            map[string]wow.PackVersion
	workflows        []wow.WorkflowEntry
	workflowVersions map[string]map[int]string
}

func (f candidateCatalogFake) Block(ctx context.Context, ref wow.BlockRef) (wow.BlockObservation, error) {
	id := ref.ID
	if id.IsZero() {
		for key, b := range f.blocks {
			if b.Name == ref.Name {
				id = key
			}
		}
	}
	b, ok := f.blocks[id]
	if !ok {
		return wow.BlockObservation{}, sql.ErrNoRows
	}
	if ref.Version == 0 {
		return wow.BlockObservation{Block: b}, nil
	}
	v, ok := f.versions[id][ref.Version]
	if !ok {
		return v, sql.ErrNoRows
	}
	v.Block = b
	return v, nil
}
func (f candidateCatalogFake) Workflow(ctx context.Context, ref wow.WorkflowRef) (wow.WorkflowObservation, error) {
	y, ok := f.workflowVersions[ref.Name][ref.Version]
	if !ok {
		return wow.WorkflowObservation{}, sql.ErrNoRows
	}
	return wow.WorkflowObservation{Name: ref.Name, Version: ref.Version, YAML: y}, nil
}
func (f candidateCatalogFake) Workflows(ctx context.Context) ([]wow.WorkflowEntry, error) {
	return f.workflows, nil
}
func (candidateCatalogFake) Withdrawn(wow.Kind, string, string) bool { return false }

func (f candidateCatalogFake) PackPublication(ctx context.Context, name string, version int64) (wow.PackVersion, error) {
	p, ok := f.packs[name]
	if !ok || version != 1 {
		return p, sql.ErrNoRows
	}
	return p, nil
}

func TestCandidatePreparationPreservesOmissionsAndExplicitPins(t *testing.T) {
	t.Parallel()
	cat := candidateCatalogFake{
		blocks: map[ids.ID]wow.Block{idstest.N(1): {ID: idstest.N(1), Name: "house", LatestVersion: 2}, idstest.N(2): {ID: idstest.N(2), Name: "own", LatestVersion: 2}},
		versions: map[ids.ID]map[int]wow.BlockObservation{
			idstest.N(1): {1: {Version: 1, ChecksJSON: `[{"name":"safe","severity":"high"}]`}},
			idstest.N(2): {1: {Version: 1, ChecksJSON: `[]`}, 2: {Version: 2, ChecksJSON: `[{"name":"new","title":"New check","expects":"works"}]`}},
		},
		packs: map[string]wow.PackVersion{"house": {SpecJSON: `{"guidance":["house"],"requires":["scanner"]}`, PinsJSON: `{"blocks":{"house":1}}`}},
	}
	current := &RecordedRevision{ProjectID: idstest.N(7), Revision: 3, Receipt: Receipt{Guidance: []ReceiptBlock{{BlockID: idstest.N(1), Name: "house", Version: 1}, {BlockID: idstest.N(2), Name: "own", Version: 1}}}}
	memberships := []ProjectPackRecord{{Pack: "house", Version: 1, SpecJSON: `{"guidance":["house"]}`}}
	builder := candidateBuilder{ways: cat}
	kept, err := builder.prepare(t.Context(), idstest.N(7), current, memberships, Request{})
	if err != nil || kept.ExpectedRevision != 3 || len(kept.Packs) != 1 || !slices.Equal(kept.Additions, []string{"own"}) || kept.Receipt.Guidance[1].Version != 1 {
		t.Fatalf("omitted selection moved: %+v %v", kept, err)
	}
	if !slices.Equal(kept.RequiredCapabilities, []string{"scanner"}) {
		t.Fatalf("lost published requirement: %v", kept.RequiredCapabilities)
	}
	req := Request{Packs: []PackPick{}, Additions: []string{"own@2"}}
	moved, err := builder.prepare(t.Context(), idstest.N(7), current, memberships, req)
	if err != nil || len(moved.Packs) != 0 || !slices.Equal(moved.Additions, []string{"own"}) || !slices.Equal(req.Additions, []string{"own@2"}) {
		t.Fatalf("explicit selection or caller input changed: %+v req=%+v %v", moved, req, err)
	}
	if len(moved.NewChecks) != 1 || moved.NewChecks[0].Check != "new" || !slices.Equal(moved.Weakens, []string{"house"}) {
		t.Fatalf("lost acceptance or weakening: %+v", moved)
	}
	if current.Receipt.Guidance[1].Version != 1 {
		t.Fatal("preparation mutated current receipt")
	}
}

func TestCandidatePreparationRefusesUnreadableChecksAndMissingPinnedWorkflow(t *testing.T) {
	t.Parallel()
	cat := candidateCatalogFake{
		blocks:   map[ids.ID]wow.Block{idstest.N(1): {ID: idstest.N(1), Name: "gate", LatestVersion: 1}},
		versions: map[ids.ID]map[int]wow.BlockObservation{idstest.N(1): {1: {Version: 1, ChecksJSON: `{"bad":"shape"}`}}},
	}
	current := &RecordedRevision{ProjectID: idstest.N(7), Revision: 1, Receipt: Receipt{}}
	if _, err := (candidateBuilder{ways: cat}).prepare(t.Context(), idstest.N(7), current, nil, Request{Additions: []string{"gate"}}); err == nil {
		t.Fatal("unreadable checks produced a viable candidate")
	}
	cat.workflows = []wow.WorkflowEntry{{Name: "land", Version: 2, YAML: "new", Doc: &schema.Doc{Metadata: schema.Metadata{System: true}}}}
	cat.workflowVersions = map[string]map[int]string{"land": {2: "new"}}
	current.Receipt.Workflows = []ReceiptWorkflow{{Name: "land", Version: 1, System: true}}
	if _, err := (candidateBuilder{ways: cat}).prepare(t.Context(), idstest.N(7), current, nil, Request{}); err == nil || !strings.Contains(err.Error(), "adopted workflow land v1 is unavailable") || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("historical workflow silently advanced: %v", err)
	}
}

func TestCandidateRequirementsSurviveTargetEncoding(t *testing.T) {
	t.Parallel()
	before := Candidate{ProjectID: idstest.N(7), RequiredCapabilities: []string{"scanner"}}
	body, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	var after Candidate
	if err := json.Unmarshal(body, &after); err != nil {
		t.Fatal(err)
	}
	if !after.ProjectID.IsZero() || !slices.Equal(after.RequiredCapabilities, before.RequiredCapabilities) {
		t.Fatalf("target encoding lost requirements or invented scope: %s", body)
	}
}
