package workflow

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/flokkit/apptown/internal/access"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// Read-only labels of the recorded revision; authority still uses its definition.
type standingRuleIdentity = struct {
	Kind  string `json:"kind"`
	Scope string `json:"scope"`
}

func standingRuleIdentityIn(ctx context.Context, db database.Queryer, authority access.Authorizer, rule StandingRevision) (*standingRuleIdentity, error) {
	parts := []string{}
	c := rule.Choices
	if c.Dependencies != "off" {
		level := "patch and minor"
		if c.Dependencies == "aggressive" {
			level = "including major"
		}
		parts = append(parts, "Dependencies "+level)
	}
	for _, item := range []struct {
		on   bool
		name string
	}{{c.SecurityFixes, "security fixes"}, {c.BaseImages, "base images"}, {c.ToolchainPatches, "toolchain patches"}, {c.Packs, "packs"}} {
		if item.on {
			parts = append(parts, item.name)
		}
	}
	kind := strings.Join(parts, ", ")
	if kind == "" {
		kind = "Turned off"
	}
	out := &standingRuleIdentity{Kind: kind, Scope: "all projects"}
	return out, nil
}

// Enrich only the response, preserving all stored evidence fields verbatim.
func standingEvidenceIdentityIn(ctx context.Context, db database.Queryer, authority access.Authorizer, step *Step) error {
	if step.Type != "approval" || len(step.Evidence) == 0 {
		return nil
	}
	var evidence map[string]json.RawMessage
	if err := json.Unmarshal(step.Evidence, &evidence); err != nil {
		return err
	}
	for _, key := range []string{"decisions", "previousDecisions"} {
		raw, ok := evidence[key]
		if !ok {
			continue
		}
		var votes []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &votes); err != nil {
			return err
		}
		for _, vote := range votes {
			raw, ok := vote["standing"]
			if !ok || string(raw) == "null" {
				continue
			}
			var voteAuthority standingVote
			if err := json.Unmarshal(raw, &voteAuthority); err != nil {
				return err
			}
			row, err := workflowsql.New(db.SQL()).StandingRevision(ctx, workflowsql.StandingRevisionParams{RuleID: voteAuthority.RuleID, Revision: voteAuthority.Revision})
			if err != nil {
				return err
			}
			rule, err := readStandingRevision(row)
			if err != nil {
				return err
			}
			identity, err := standingRuleIdentityIn(ctx, db, authority, rule)
			if err != nil {
				return err
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				return err
			}
			fields["rule"], err = json.Marshal(identity)
			if err != nil {
				return err
			}
			vote["standing"], err = json.Marshal(fields)
			if err != nil {
				return err
			}
		}
		var err error
		evidence[key], err = json.Marshal(votes)
		if err != nil {
			return err
		}
	}
	raw, err := json.Marshal(evidence)
	if err == nil {
		step.Evidence = raw
	}
	return err
}
