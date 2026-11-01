package adoption

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/strs"

	"github.com/flokkit/apptown/internal/apptown/adoption/internal/adoptionsql"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// Policy is one row of policies: a project's copy of a rule.
type Policy struct {
	ID          ids.ID         `json:"id"`
	ProjectID   ids.ID         `json:"-"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Rule        string         `json:"rule"`
	With        map[string]any `json:"with"`
	Origin      string         `json:"origin,omitempty"`
	CreatedAt   string         `json:"createdAt"`
}

// Violation is one rule a workflow breaks, in words.
type Violation struct {
	Policy   string `json:"policy"`
	Workflow string `json:"workflow"`
	Detail   string `json:"detail"`
}

// evaluation is a policy with its capability facts frozen for one set of
// judgements, so many workflows do not reread the same instance binding.
type evaluation struct {
	Name, Rule, Reading string
	Requirement         wow.Requirement
	Bound               bool
}

// PrepareEvaluations checks each policy and freezes the capability facts
// once. It reads through q, so a caller's transaction sees its own facts.
func PrepareEvaluations(ctx context.Context, q database.Queryer, policies []Policy) ([]evaluation, error) {
	return prepareEvaluations(ctx, q, policies, integrationCapabilities{})
}

// Existing requireCapabilityBound policies describe instance bindings, not
// project attachments. Keep their observation distinct from adoption requirements.
type policyCapabilityObserver interface {
	CapabilitiesForShareIn(context.Context, database.Queryer) ([]string, error)
}

type integrationCapabilities struct{}

func (integrationCapabilities) CapabilitiesForShareIn(ctx context.Context, q database.Queryer) ([]string, error) {
	return integration.CapabilitiesForShareIn(ctx, q)
}
func prepareEvaluations(ctx context.Context, q database.Queryer, policies []Policy, capabilities policyCapabilityObserver) ([]evaluation, error) {
	evaluations := make([]evaluation, 0, len(policies))
	needsBindings := false
	for _, policy := range policies {
		requirement, err := checkPolicy(&policy)
		if err != nil {
			return nil, err
		}
		evaluations = append(evaluations, evaluation{Name: policy.Name, Rule: policy.Rule, Reading: wow.Reading(policy.Rule, policy.With), Requirement: requirement})
		needsBindings = needsBindings || policy.Rule == "requireCapabilityBound"
	}
	if needsBindings {
		if capabilities == nil {
			return nil, fmt.Errorf("capability observation is unavailable")
		}
		attached, err := capabilities.CapabilitiesForShareIn(ctx, q)
		if err != nil {
			return nil, err
		}
		bound := make(map[string]bool, len(attached))
		for _, capability := range attached {
			bound[capability] = true
		}
		for i := range evaluations {
			evaluations[i].Bound = bound[evaluations[i].Requirement.Capability]
		}
	}
	return evaluations, nil
}

// ReadProjectPolicies is a project's policies, checked as they are read,
// with their options' numbers kept exact.
func ReadProjectPolicies(ctx context.Context, q database.Queryer, projectID ids.ID) ([]Policy, error) {
	rows, err := adoptionsql.New(q.SQL()).ProjectPoliciesForShare(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var out []Policy
	for _, r := range rows {
		p, err := policyRecord(r)
		if err != nil {
			return nil, err
		}
		if _, err := checkPolicy(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// checkWorkflow judges one workflow definition against a project's policies.
func checkWorkflow(ctx context.Context, q database.Queryer, projectID ids.ID, wfName string, spec *schema.Spec, capabilities policyCapabilityObserver) ([]Violation, error) {
	policies, err := ReadProjectPolicies(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	evaluations, err := prepareEvaluations(ctx, q, policies, capabilities)
	if err != nil {
		return nil, err
	}
	var violations []Violation
	for _, p := range evaluations {
		if detail := p.Detail(spec); detail != "" {
			violations = append(violations, Violation{Policy: p.Name, Workflow: wfName, Detail: detail})
		}
	}
	return violations, nil
}

// projectConformance judges every selected and system workflow in one
// checked projection, rather than rereading lane selection per workflow
// and silently omitting read failures.
func projectConformance(ctx context.Context, q database.Queryer, projectID ids.ID, capabilities policyCapabilityObserver) ([]Violation, error) {
	if _, err := adoptionsql.New(q.SQL()).ProjectIdentity(ctx, projectID); err != nil {
		return nil, fmt.Errorf("project %d: %w", projectID, err)
	}
	workflows, err := adoptionsql.New(q.SQL()).WorkflowsForProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	policies, err := ReadProjectPolicies(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	evaluations, err := prepareEvaluations(ctx, q, policies, capabilities)
	if err != nil {
		return nil, err
	}
	var violations []Violation
	for _, workflow := range workflows {
		doc, err := schema.ParseWorkflowStored([]byte(workflow.Yaml))
		if err != nil {
			return nil, fmt.Errorf("policy workflow %q: %w", workflow.LibWorkflow.Name, err)
		}
		if !doc.Metadata.System && !workflow.Selected {
			continue
		}
		for _, policy := range evaluations {
			if detail := policy.Detail(&doc.Spec); detail != "" {
				violations = append(violations, Violation{Policy: policy.Name, Workflow: workflow.LibWorkflow.Name, Detail: detail})
			}
		}
	}
	return violations, nil
}

// FormatViolations joins violations into one sentence.
func FormatViolations(vs []Violation) string {
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = fmt.Sprintf("policy %q: workflow %q %s", v.Policy, v.Workflow, v.Detail)
	}
	return strings.Join(parts, "; ")
}

// install upserts a project's policy by name, validated first, with its
// receipt in the same transaction.
func (s *policyStore) install(ctx context.Context, projectID ids.ID, name, description, rule string, with map[string]any, origin, who string) error {
	if _, err := wow.CheckPolicyAuthored(name, rule, with); err != nil {
		return err
	}
	now := clock.Or(s.clock)()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		if err := InstallPolicyIn(ctx, tx, projectID, name, description, rule, with, origin, now); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, projectID, "policy.installed", name, map[string]any{"rule": rule, "with": with})
	})
}

// InstallPolicyIn upserts a project's policy inside the caller's
// transaction (a pack apply installs several under one receipt), at the
// caller's clock.
func InstallPolicyIn(ctx context.Context, tx *database.Tx, projectID ids.ID, name, description, rule string, with map[string]any, origin string, at time.Time) error {
	config, err := strs.JSON(with)
	if err != nil {
		return fmt.Errorf("policy %q: %w", name, err)
	}
	n, err := adoptionsql.New(tx.SQL()).PutProjectPolicy(ctx, adoptionsql.PutProjectPolicyParams{ID: ids.New(at.UTC()), ProjectID: projectID, Name: name, Description: description, Rule: rule, Config: []byte(config), Origin: origin, At: at.UTC()})
	if err != nil {
		return err
	}

	if n != 1 {
		return fmt.Errorf("policy %q was not recorded", name)
	}
	return nil
}

// policyReceiptLinesIn is a project's policies as an adoption receipt records
// them: the name, the rule, the hash of the stored options' canonical
// text, the origin. The hash is over the stored text exactly as before
// revisions existed, so digests stay stable.
func policyReceiptLinesIn(ctx context.Context, q database.Queryer, projectID ids.ID) ([]ReceiptPolicy, error) {
	rows, err := adoptionsql.New(q.SQL()).ProjectPolicies(ctx, projectID)
	out := make([]ReceiptPolicy, 0, len(rows))
	for _, r := range rows {
		out = append(out, ReceiptPolicy{Name: r.Name, Rule: r.Rule, Origin: r.Origin, ConfigHash: hashText(canonJSON(string(r.ConfigJson)))})
	}
	return out, err
}

// errNoProjectPolicy is the absence of the project policy a caller named by
// id, so callers never need the store's sql.ErrNoRows to answer it.
var errNoProjectPolicy = fault.New(fault.NotFound, "no project policy has id")

// remove deletes a project's policy by id with its receipt: the audited
// escape hatch. An unknown id is ErrNoProjectPolicy.
func (s *policyStore) remove(ctx context.Context, id ids.ID, who string) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := adoptionsql.New(tx.SQL())
		p, err := q.PolicyByID(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w %d", errNoProjectPolicy, id)
		}
		if err != nil {
			return err
		}
		if _, err := q.DeleteProjectPolicy(ctx, id); err != nil {
			return err
		}
		return audit.At(clock.Or(s.clock)()).RecordIn(ctx, tx, who, p.ProjectID, "policy.removed", p.Name, nil)
	})
}

// policyAdoption is a project holding a policy of a given name.
type policyAdoption struct {
	Policy      string
	ProjectID   ids.ID
	ProjectSlug string
	ProjectName string
}

// adoptions is every (policy name, project) pair, for usage counts.
func (s *policyStore) adoptions(ctx context.Context) ([]policyAdoption, error) {
	rows, err := adoptionsql.New(s.db.SQL()).PolicyAdoptions(ctx)
	var out []policyAdoption
	for _, r := range rows {
		out = append(out, policyAdoption{Policy: r.Name, ProjectID: r.ProjectID, ProjectSlug: r.Slug, ProjectName: r.ProjectName})
	}
	return out, err
}

func policyRecord(r adoptionsql.Policy) (Policy, error) {
	p := Policy{ID: r.ID, ProjectID: r.ProjectID, Name: r.Name, Description: r.Description, Rule: r.Rule, Origin: r.Origin, CreatedAt: r.CreatedAt.Format(time.RFC3339Nano)}
	decoder := json.NewDecoder(bytes.NewReader(r.ConfigJson))
	decoder.UseNumber()
	if err := decoder.Decode(&p.With); err != nil {
		return p, fmt.Errorf("policy %q has invalid options: %w", p.Name, err)
	}
	return p, nil
}

// checkPolicy validates an installed declaration using the catalog's one rule.
func checkPolicy(p *Policy) (wow.Requirement, error) { return wow.CheckPolicy(p.Name, p.Rule, p.With) }

type policyStore struct {
	db    *database.DB
	clock func() time.Time // AppTown's clock; nil is the real one
}

func newPolicies(db *database.DB) *policyStore { return &policyStore{db: db} }

// Detail is the violation sentence for one workflow, or "".
func (e evaluation) Detail(spec *schema.Spec) string { return e.Requirement.Evaluate(spec, e.Bound) }
