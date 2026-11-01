package adoption

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/wow"
)

var errPackApplication = errors.New("pack application must be prepared by Adoption and remain unchanged")

// PreparePack creates a complete installation from exact published pins:
// every pick's guidance, lanes, policies and extensions, none above another.
// Packs that disagree are the candidate's problems, and it is not adopted.
func (s *Service) PreparePack(ctx context.Context, projectID ids.ID, picks []PackPick) (*Candidate, error) {
	if len(picks) == 0 {
		return nil, errPackApplication
	}
	c, err := s.Prepare(ctx, projectID, Request{Packs: picks, Additions: []string{}})
	if err != nil {
		return nil, err
	}
	setup := wow.PackSetup{}
	sorted := slices.Clone(picks)
	slices.SortFunc(sorted, func(a, b PackPick) int { return strings.Compare(a.Pack, b.Pack) })
	for _, pick := range sorted {
		one, err := wow.PackSetupIn(ctx, s.db, pick.Pack, pick.Version)
		if err != nil {
			return nil, err
		}
		// A policy two packs both name is the same policy (Prepare refuses
		// two meanings under one name); it is installed once.
		for _, policy := range one.Policies {
			if !slices.ContainsFunc(setup.Policies, func(p wow.LibraryPolicy) bool { return p.Name == policy.Name }) {
				setup.Policies = append(setup.Policies, policy)
			}
		}
		setup.Extensions = append(setup.Extensions, one.Extensions...)
	}
	policies, err := ReadProjectPolicies(ctx, s.db, projectID)
	if err != nil {
		return nil, err
	}
	return preparePackInstallation(c, policies, setup)
}

func preparePackInstallation(c *Candidate, policies []Policy, setup wow.PackSetup) (*Candidate, error) {
	// Preserve policies the pack does not replace, including their exact configs.
	policies = slices.Clone(policies)
	for _, policy := range setup.Policies {
		replacement := Policy{Name: policy.Name, Description: policy.Description, Rule: policy.Rule, With: policy.With, Origin: policy.Origin}
		found := false
		for i := range policies {
			if policies[i].Name == policy.Name {
				policies[i], found = replacement, true
			}
		}
		if !found {
			policies = append(policies, replacement)
		}
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].Name < policies[j].Name })
	c.Receipt.Policies = []ReceiptPolicy{}
	for _, policy := range policies {
		config, err := json.Marshal(policy.With)
		if err != nil {
			return nil, err
		}
		c.Receipt.Policies = append(c.Receipt.Policies, ReceiptPolicy{Name: policy.Name, Rule: policy.Rule, ConfigHash: hashText(canonJSON(string(config))), Origin: policy.Origin})
	}
	c.Receipt.Extensions = slices.Clone(c.Receipt.Extensions)
	for _, extension := range setup.Extensions {
		if !slices.Contains(c.Receipt.Extensions, extension) {
			c.Receipt.Extensions = append(c.Receipt.Extensions, extension)
		}
	}
	sort.Strings(c.Receipt.Extensions)
	c.Digest = Digest(c.Receipt)
	c.Installation = Installation{Policies: policies, Extensions: slices.Clone(c.Receipt.Extensions)}
	seal, err := packApplicationSeal(*c)
	if err != nil {
		return nil, err
	}
	c.applicationSeal = seal
	return c, nil
}

// The seal is private provenance, not a transport credential. Recomputing its
// digest is possible only inside Adoption; callers may copy a prepared value but
// cannot alter its scope, installation, manifest or requirements unnoticed.
func packApplicationSeal(c Candidate) (string, error) {
	b, err := json.Marshal(struct {
		Candidate    Candidate
		ProjectID    ids.ID
		Installation Installation
	}{c, c.ProjectID, c.Installation})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}
func validatePackApplication(c Candidate) error {
	if c.applicationSeal == "" {
		return errPackApplication
	}
	seal, err := packApplicationSeal(c)
	if err != nil {
		return err
	}
	if seal != c.applicationSeal {
		return errPackApplication
	}
	return nil
}

// Selection has its own authority and durable target shape. Installation belongs
// to the prepared direct pack command, never a selection or serialized uplift.
func requireSelectionCandidate(c Candidate) error {
	if c.applicationSeal != "" || len(c.Installation.Policies) != 0 || len(c.Installation.Extensions) != 0 {
		return fmt.Errorf("installation requires the direct pack command: %w", errPackApplication)
	}
	return nil
}
