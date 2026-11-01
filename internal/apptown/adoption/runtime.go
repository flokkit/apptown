package adoption

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/fault"
)

// Source identifies an already selected receipt within its project. Runtime
// operations never replace a missing or damaged receipt with today's selection.
type Source struct {
	ProjectID ids.ID
	Revision  *RecordedRevision
}

func (source Source) receipt(subject string) (*RecordedRevision, error) {
	if source.Revision == nil {
		return nil, fmt.Errorf("project %d: no recorded adoption is available for %s", source.ProjectID, subject)
	}
	if source.Revision.ProjectID != source.ProjectID {
		return nil, fmt.Errorf("adoption revision %d belongs to project %d, not project %d", source.Revision.Revision, source.Revision.ProjectID, source.ProjectID)
	}
	return source.Revision, nil
}

// Resolved is the exact guidance that steered a runtime: the text every
// agent reads up front, the skills it loads on demand, and the hash of the
// whole bodies the imports pinned (rules and skills alike).
type Resolved struct {
	Text, Hash string
	Skills     []wow.Skill
	Imports    []Import
}

// Resolve compiles the supplied receipt. Live manifest previews remain the
// separate Compile operation so a console preview cannot silently become pinned.
func (s *Service) Resolve(ctx context.Context, source Source) (Resolved, error) {
	revision, err := source.receipt("guidance")
	if err != nil {
		return Resolved{}, err
	}
	value := Adoption{ProjectID: source.ProjectID, Revision: revision.Revision, recorded: revision}
	return value.resolve(ctx, s.deps.Ways)
}

// resolve is the adopted value's rule for turning its immutable pins into text.
// A metadata-only current value has no receipt and cannot infer one from history.
func (a Adoption) resolve(ctx context.Context, cat catalog) (Resolved, error) {
	revision, err := (Source{ProjectID: a.ProjectID, Revision: a.recorded}).receipt("guidance")
	if err != nil {
		return Resolved{}, err
	}
	result, err := compileGuidance(ctx, cat, revision.Receipt.importsOf(), fmt.Sprintf("revision %d: ", revision.Revision))
	return result, err
}

// compileGuidance joins the enabled imports' pinned bodies: each block's
// rules, with one line naming its skill when it has one, and the skills
// beside. The hash is over the whole bodies, as a block pins them, so a
// project with no skill keeps its text and hash, and the hash still
// witnesses each skill. Pinned versions are immutable, so the bodies need
// not come from a snapshot. where prefixes an error with the imports' source.
func compileGuidance(ctx context.Context, cat catalog, imports []Import, where string) (Resolved, error) {
	result := Resolved{Imports: imports}
	var parts, whole []string
	owner := map[string]string{} // skill name → the item carrying it
	for _, im := range imports {
		if im.Disabled {
			continue
		}
		if im.Version <= 0 {
			return result, fmt.Errorf("%s%s has no pinned version", where, im.Name)
		}
		if cat == nil {
			return result, errCatalogUnavailable
		}
		block, err := cat.Block(ctx, wow.BlockRef{ID: im.BlockID, Version: im.Version})
		if err != nil {
			return result, fmt.Errorf("%s%s v%d: %w", where, im.Name, im.Version, err)
		}
		heading := fmt.Sprintf("# %s v%d\n\n", im.Name, im.Version)
		body := strings.TrimSpace(block.Body)
		whole = append(whole, heading+body)
		rules, skill, err := wow.SplitSkill(im.Name, body)
		if err != nil {
			return result, fmt.Errorf("%s%s v%d: %w", where, im.Name, im.Version, err)
		}
		if skill != nil {
			// Two items whose names give the same skill name would write one
			// file: one how-to would silently stand in for the other.
			if other, taken := owner[skill.Name]; taken {
				return result, fmt.Errorf("%s%s and %s both carry a skill named %s; rename one of them", where, other, im.Name, skill.Name)
			}
			owner[skill.Name] = im.Name
			rules += "\n\n" + wow.SkillPointer(*skill)
			result.Skills = append(result.Skills, *skill)
		}
		parts = append(parts, heading+rules)
	}
	result.Text = strings.Join(parts, "\n\n---\n\n")
	sum := sha256.Sum256([]byte(strings.Join(whole, "\n\n---\n\n")))
	result.Hash = hex.EncodeToString(sum[:])
	return result, nil
}

// CheckDeclaration is the authored data pinned by a block version. Conformance
// owns validation and evaluation; Adoption supplies the exact declaration.
type CheckDeclaration struct {
	Name        string   `json:"name"`
	Title       string   `json:"title,omitempty"`
	Expects     string   `json:"expects"`
	Severity    string   `json:"severity,omitempty"`
	Evaluator   string   `json:"evaluator,omitempty"`
	Sh          string   `json:"sh,omitempty"`
	Rubric      string   `json:"rubric,omitempty"`
	Probe       string   `json:"probe,omitempty"`
	Tool        string   `json:"tool,omitempty"`
	Scope       string   `json:"scope,omitempty"`
	Files       []string `json:"files,omitempty"`
	Occurrences string   `json:"occurrences,omitempty"`
	AppliesTo   []string `json:"appliesTo,omitempty"`
}
type DeclaredCheck struct {
	Block   string
	Version int
	Check   CheckDeclaration
}

func (s *Service) Checks(ctx context.Context, source Source) ([]DeclaredCheck, error) {
	revision, err := source.receipt("checks")
	if err != nil {
		return nil, err
	}
	var out []DeclaredCheck
	for _, b := range revision.Receipt.Guidance {
		if b.Disabled {
			continue
		}
		if b.Version <= 0 {
			return nil, fmt.Errorf("revision %d: %s has no pinned version", revision.Revision, b.Name)
		}
		if s.deps.Ways == nil {
			return nil, errCatalogUnavailable
		}
		version, err := s.deps.Ways.Block(ctx, wow.BlockRef{ID: b.BlockID, Version: b.Version})
		if err != nil {
			return nil, fmt.Errorf("revision %d: %s v%d: %w", revision.Revision, b.Name, b.Version, err)
		}
		checks, err := storedChecks(version.ChecksJSON)
		if err != nil {
			return nil, fmt.Errorf("revision %d: %s v%d: %w", revision.Revision, b.Name, b.Version, err)
		}
		for _, check := range checks {
			out = append(out, DeclaredCheck{Block: b.Name, Version: b.Version, Check: check})
		}
	}
	return out, nil
}

// ResolveBlock resolves imported skills at their exact pin and other resources
// at the receipt's time. Absence at that time never falls forward to the latest.
func (s *Service) ResolveBlock(ctx context.Context, source Source, name string) (string, int, error) {
	revision, err := source.receipt(fmt.Sprintf("block %q", name))
	if err != nil {
		return "", 0, err
	}
	if s.deps.Ways == nil {
		return "", 0, errCatalogUnavailable
	}
	block, err := s.deps.Ways.Block(ctx, wow.BlockRef{Name: name})
	if err != nil {
		return "", 0, fmt.Errorf("block %q not found", name)
	}
	ref := wow.BlockRef{ID: block.Block.ID}
	for _, b := range revision.Receipt.Guidance {
		if b.BlockID == ref.ID && !b.Disabled {
			if b.Version <= 0 {
				return "", 0, fmt.Errorf("revision %d: %s has no pinned version", revision.Revision, name)
			}
			ref.Version = b.Version
			break
		}
	}
	if ref.Version == 0 {
		ref.AsOf = revision.CreatedAt.Time
		if ref.AsOf.IsZero() {
			return "", 0, fmt.Errorf("block %q did not exist at adoption revision %d", name, revision.Revision)
		}
	}
	version, err := s.deps.Ways.Block(ctx, ref)
	if err != nil {
		if ref.Version == 0 {
			return "", 0, fmt.Errorf("block %q did not exist at adoption revision %d", name, revision.Revision)
		}
		return "", ref.Version, err
	}
	return version.Body, version.Version, nil
}

// WorkflowVersion reads only the workflow version explicitly named by a receipt.
func (s *Service) WorkflowVersion(ctx context.Context, source Source, name string) (int, string, error) {
	revision, err := source.receipt(fmt.Sprintf("workflow %q", name))
	if err != nil {
		return 0, "", err
	}
	for _, w := range revision.Receipt.Workflows {
		if w.Name != name {
			continue
		}
		if w.Version <= 0 {
			return 0, "", fmt.Errorf("adopted workflow %s has no pinned version", name)
		}
		if s.deps.Ways == nil {
			return 0, "", errCatalogUnavailable
		}
		wf, err := s.deps.Ways.Workflow(ctx, wow.WorkflowRef{Name: name, Version: w.Version})
		if err != nil {
			return 0, "", fmt.Errorf("adopted workflow %s v%d is unavailable: %w", name, w.Version, err)
		}
		return w.Version, wf.YAML, nil
	}
	// Said for the person who asked for the workflow (apptown plan on a pack
	// with no planning workflow, or a lane not selected), not in the
	// receipt's terms.
	return 0, "", fault.New(fault.Unprocessable, fmt.Sprintf("%q is not one of this project's ways of working: Settings › Way of working lists the workflows its packs offer", name))
}

type WorkflowRequest struct {
	Source            Source
	Name, ProjectSlug string
}
type ResolvedWorkflow struct {
	Name, Kind, YAML, Origin string
	Version                  int
	Doc                      *schema.Doc
	System                   bool
}

// Workflow applies the project's live lane selection and installed policies to
// the procedure version its receipt pinned. Those admission rules stay explicit.
func (s *Service) Workflow(ctx context.Context, in WorkflowRequest) (*ResolvedWorkflow, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("no workflow named")
	}
	if _, err := in.Source.receipt(fmt.Sprintf("workflow %q", name)); err != nil {
		return nil, err
	}
	if s.deps.Ways == nil {
		return nil, errCatalogUnavailable
	}
	metadata, err := s.deps.Ways.Workflow(ctx, wow.WorkflowRef{Name: name, ResolveSystem: true, MetadataOnly: true})
	if err != nil {
		return nil, err
	}
	name = metadata.Name
	version, yaml, err := s.WorkflowVersion(ctx, in.Source, name)
	if err != nil {
		return nil, err
	}
	doc, err := schema.ParseWorkflowStored([]byte(yaml))
	if err != nil {
		return nil, fmt.Errorf("workflow %q is invalid: %w", name, err)
	}
	if !doc.Metadata.System {
		selected, err := s.LaneSelected(ctx, in.Source.ProjectID, name)
		if err != nil {
			return nil, err
		}
		if !selected {
			return nil, fault.New(fault.Conflict, fmt.Sprintf("%q is not one of %s's ways of working — add it under Settings › Way of working", name, in.ProjectSlug))
		}
	}
	violations, err := s.CheckWorkflow(ctx, s.db, in.Source.ProjectID, name, &doc.Spec)
	if err != nil {
		return nil, err
	}
	if len(violations) > 0 {
		return nil, fault.New(fault.Conflict, fmt.Sprintf("policy violation: %s", FormatViolations(violations)))
	}
	prefix := "lib"
	if metadata.Origin == "stock" {
		prefix = "stock"
	}
	return &ResolvedWorkflow{Name: name, Kind: doc.Spec.Kind, Version: version, YAML: yaml, Origin: fmt.Sprintf("%s/%s@%d", prefix, name, version), Doc: doc, System: doc.Metadata.System}, nil
}

// storedChecks reads a block version's stored checks. Every stored check
// states what it applies to (the upgrade's migration gave the ones saved
// before appliesTo existed [repository]); one that does not is refused,
// never judged under a default.
func storedChecks(raw string) ([]CheckDeclaration, error) {
	var checks []CheckDeclaration
	if err := json.Unmarshal([]byte(raw), &checks); err != nil {
		return nil, err
	}
	for _, c := range checks {
		if len(c.AppliesTo) == 0 {
			return nil, fmt.Errorf("check %s declares no appliesTo", c.Name)
		}
	}
	return checks, nil
}
