package adoption

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/strs"
)

// validateCandidateIn checks the frozen manifest against exact publications and
// the selections that will exist after installation. It never rebuilds against
// today's latest versions or trusts presentation summaries as evidence.
func (s *Service) validateCandidateIn(ctx context.Context, q database.Queryer, projectID ids.ID, c Candidate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if projectID.IsZero() || c.ProjectID != projectID {
		return fmt.Errorf("candidate belongs to another project")
	}
	if len(c.Problems) != 0 {
		return fmt.Errorf("%w: %s", ErrNotAdoptable, strings.Join(c.Problems, "; "))
	}
	if c.Digest == "" || c.Digest != Digest(c.Receipt) {
		return fmt.Errorf("candidate digest does not match its receipt")
	}
	if s.deps.Ways == nil {
		return ErrCatalogUnavailable
	}
	blocks := make(map[string]ReceiptBlock, len(c.Receipt.Guidance))
	blockIDs := make(map[ids.ID]bool, len(c.Receipt.Guidance))
	for position, block := range c.Receipt.Guidance {
		if block.Position != position || block.BlockID.IsZero() || block.Version <= 0 || block.Name == "" {
			return fmt.Errorf("invalid candidate guidance position or identity")
		}
		if _, duplicate := blocks[block.Name]; duplicate || blockIDs[block.BlockID] {
			return fmt.Errorf("candidate repeats guidance %q", block.Name)
		}
		observed, err := s.deps.Ways.Block(ctx, wow.BlockRef{ID: block.BlockID, Version: block.Version})
		if err != nil {
			return fmt.Errorf("candidate guidance %s v%d: %w", block.Name, block.Version, err)
		}
		if observed.Block.ID != block.BlockID || observed.Block.Name != block.Name || observed.Version != block.Version {
			return fmt.Errorf("candidate guidance %q does not match its recorded identity", block.Name)
		}
		blocks[block.Name], blockIDs[block.BlockID] = block, true
	}
	// Contradictions are catalog block metadata, not versioned body contents.
	// Reuse preparation's rule over these exact block identities; a caller
	// cannot make an incompatible enabled pair acceptable by clearing Problems.
	conflicts, err := (candidateBuilder{ways: s.deps.Ways}).declaredConflicts(ctx, c.Receipt.Guidance)
	if err != nil {
		return err
	}
	if len(conflicts) != 0 {
		return fmt.Errorf("%w: %s — both would be in force", ErrNotAdoptable, strings.Join(conflicts, "; "))
	}
	workflows := make(map[string]ReceiptWorkflow, len(c.Receipt.Workflows))
	for _, workflow := range c.Receipt.Workflows {
		if workflow.Name == "" || workflow.Version <= 0 {
			return fmt.Errorf("invalid candidate workflow identity")
		}
		if _, duplicate := workflows[workflow.Name]; duplicate {
			return fmt.Errorf("candidate repeats workflow %q", workflow.Name)
		}
		observed, err := s.deps.Ways.Workflow(ctx, wow.WorkflowRef{Name: workflow.Name, Version: workflow.Version})
		if err != nil {
			return fmt.Errorf("candidate workflow %s v%d: %w", workflow.Name, workflow.Version, err)
		}
		doc, err := schema.ParseWorkflowStored([]byte(observed.YAML))
		if err != nil {
			return fmt.Errorf("candidate workflow %s: %w", workflow.Name, err)
		}
		if observed.Name != workflow.Name || observed.Version != workflow.Version || HashText(observed.YAML) != workflow.YAMLHash || doc.Metadata.System != workflow.System {
			return fmt.Errorf("candidate workflow %q does not match its exact publication", workflow.Name)
		}
		workflows[workflow.Name] = workflow
	}
	// A pack the instance no longer offers keeps the projects already on
	// it, at any of its versions, and takes no new one. Every way into a
	// project's pack membership settles here: applying a pack, adopting a
	// selection and recording an uplift's target.
	current, err := ProjectPacksIn(ctx, q, projectID)
	if err != nil {
		return err
	}
	onPack := map[string]bool{}
	for _, m := range current {
		onPack[m.Pack] = true
	}
	for _, pick := range c.Packs {
		if onPack[pick.Pack] {
			continue
		}
		p, err := wow.PackByNameIn(ctx, q, pick.Pack)
		if err != nil {
			return fmt.Errorf("candidate pack %q: %w", pick.Pack, err)
		}
		if !p.Enabled && !p.Platform() {
			return fmt.Errorf("%w: %s is no longer offered, so a project not already on it cannot take it", ErrPackNotOffered, pick.Pack)
		}
	}
	if c.CopyOf != nil {
		if err := s.validateCopyIn(ctx, q, projectID, c); err != nil {
			return err
		}
		return s.validateInstallationIn(ctx, q, projectID, c)
	}
	seenPacks := map[string]bool{}
	selectedPack := ""
	for _, pick := range c.Packs {
		if pick.Pack == "" || pick.Version <= 0 || seenPacks[pick.Pack] {
			return fmt.Errorf("invalid or duplicate candidate pack %q", pick.Pack)
		}
		seenPacks[pick.Pack] = true
		if selectedPack == "" || selectedPack == PlatformPack {
			selectedPack = pick.Pack
		}
		publication, err := wow.PackVersionIn(ctx, q, pick.Pack, pick.Version)
		if err != nil {
			return err
		}
		published, err := parseCandidatePublication(pick.Pack, pick.Version, publication)
		if err != nil {
			return err
		}
		for _, name := range published.Spec.Guidance {
			version, pinned := published.Pins.Blocks[name]
			block, present := blocks[name]
			if !pinned || !present || block.Disabled || block.Version != version {
				return fmt.Errorf("candidate does not carry %s v%d guidance pin %q", pick.Pack, pick.Version, name)
			}
		}
		for _, name := range published.Spec.Workflows {
			version, pinned := published.Pins.Workflows[name]
			workflow, present := workflows[name]
			// Language packs name system workflows, but the platform pack owns
			// their pins. The exact receipt publication was checked above.
			if !pinned && !published.Spec.Platform && present && workflow.System {
				continue
			}
			if !pinned || !present || workflow.Version != version {
				return fmt.Errorf("candidate does not carry %s v%d workflow pin %q", pick.Pack, pick.Version, name)
			}
		}
	}
	if c.Receipt.Pack != selectedPack {
		return fmt.Errorf("candidate selected pack does not match its memberships")
	}
	if err := s.validateInstallationIn(ctx, q, projectID, c); err != nil {
		return err
	}
	return nil
}

func (s *Service) validateInstallationIn(ctx context.Context, q database.Queryer, projectID ids.ID, c Candidate) error {
	policies, err := ReadProjectPolicies(ctx, q, projectID)
	if err != nil {
		return err
	}
	expected := make(map[string]ReceiptPolicy, len(policies)+len(c.Installation.Policies))
	for _, policy := range policies {
		if expected[policy.Name], err = policyReceipt(policy); err != nil {
			return err
		}
	}
	installed := map[string]bool{}
	for _, policy := range c.Installation.Policies {
		if policy.Name == "" || installed[policy.Name] {
			return fmt.Errorf("invalid or duplicate installed policy %q", policy.Name)
		}
		if _, err := CheckPolicy(&policy); err != nil {
			return err
		}
		installed[policy.Name] = true
		if expected[policy.Name], err = policyReceipt(policy); err != nil {
			return err
		}
	}
	if len(c.Receipt.Policies) != len(expected) {
		return fmt.Errorf("candidate policy receipt does not match installed policies")
	}
	for _, policy := range c.Receipt.Policies {
		if want, ok := expected[policy.Name]; !ok || want != policy {
			return fmt.Errorf("candidate policy %q does not match its installed rule and configuration", policy.Name)
		}
		delete(expected, policy.Name)
	}
	extensions, err := enabledExtensionsIn(ctx, q, projectID)
	if err != nil {
		return err
	}
	extensions = append(extensions, c.Installation.Extensions...)
	slices.Sort(extensions)
	extensions = slices.Compact(extensions)
	got := slices.Clone(c.Receipt.Extensions)
	slices.Sort(got)
	if !slices.Equal(got, extensions) {
		return fmt.Errorf("candidate extension receipt does not match enabled extensions")
	}
	return nil
}

func policyReceipt(policy Policy) (ReceiptPolicy, error) {
	config, err := strs.JSON(policy.With)
	if err != nil {
		return ReceiptPolicy{}, fmt.Errorf("policy %q: %w", policy.Name, err)
	}
	return ReceiptPolicy{Name: policy.Name, Rule: policy.Rule, ConfigHash: HashText(canonJSON(config)), Origin: policy.Origin}, nil
}
