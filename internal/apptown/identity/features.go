package identity

import (
	"context"
	"fmt"
	"strconv"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// Feature is an instance-wide area. Switches govern access, never workflow steps.
type Feature struct {
	Area    string `json:"area"`
	Name    string `json:"name"`
	Tier    string `json:"tier"`
	Enabled bool   `json:"enabled"`
	Covers  string `json:"covers"`
	Docs    string `json:"docs"`
}

// FeatureCatalogue is the closed inventory of product areas and their defaults.
// Callers get a copy; stored choices belong solely to instance settings.
func FeatureCatalogue() []Feature {
	return []Feature{
		{"projects", "Projects", "always", true, "Projects and repositories", "/docs/guides/new-project"},
		{"changes", "Changes", "always", true, "Changes, runs and adopted workflows", "/docs/guides/the-change"},
		{"landing", "Landing", "always", true, "Checks and acceptance required by the adopted workflow", "/docs/guides/changes-and-main"},
		{"releases", "Releases", "always", true, "Release history, promotion and rollback", "/docs/guides/container-lifecycle"},
		{"deployments", "Deployments", "always", true, "Deployments and Docker targets", "/docs/guides/deployment-targets"},
		{"people", "People and sign-in", "always", true, "Accounts, sign-in, tokens and authority", "/docs/guides/people-and-roles"},
		{"record", "The record", "always", true, "History, decisions, operations and evidence", "/docs/guides/changes-and-main"},
		{"setup", "Setup", "always", true, "Instance settings, credentials and Features", "/docs/guides/features"},
		{"conformance", "Conformance", "standard", true, "Reviews, verdicts and remediation", "/docs/guides/conformance"},
		{"backups", "Backups", "standard", true, "Backup copies and restoration", "/docs/guides/deployment-backups"},
		{"ways", "Ways of working", "always", true, "Packs, workflow, guidance and policy pages", "/docs/guides/packs-and-what-if"},
		{"chats", "Chats", "standard", true, "Interactive change chats", "/docs/guides/the-change"},
		{"usage", "Usage", "always", true, "Usage and disk pages", "/docs/guides/monitoring"},
		{"worker", "Worker", "standard", true, "Worker settings", "/docs/guides/split-install"},
		{"previews", "Previews", "always", true, "Change previews", "/docs/guides/how-apps-run"},
		{"models", "Models", "standard", true, "Model setup pages", "/docs/guides/runtime-environments"},
		{"updates", "Updates", "standard", true, "Update checks and settings", "/docs/guides/upgrading"},
		{"complaints", "Complaints", "always", true, "Reports about AppTown", "/docs/guides/features"},
		{"evals", "Evals", "beta", false, "Model and workflow evaluations", "/docs/guides/packs-and-what-if"},
		{"what-ifs", "What-ifs", "beta", false, "Proposed guidance comparisons", "/docs/guides/packs-and-what-if"},
		{"restore-rehearsals", "Restore rehearsals", "beta", false, "Rehearsal and restored-copy environments", "/docs/guides/deployment-backups"},
		{"conformance-estate", "Conformance estate view", "beta", false, "Estate conformance inventory", "/docs/guides/conformance"},
		{"app-platform", "App Platform targets", "beta", false, "DigitalOcean App Platform targets", "/docs/guides/deployment-targets"},
		{"portainer", "Portainer targets", "beta", false, "Portainer targets", "/docs/guides/deployment-targets"},
		{"bucket-site", "Bucket-site targets", "beta", false, "S3 bucket sites", "/docs/guides/deployment-targets"},
		{"mcp", "MCP for outside agents", "beta", false, "Outside agents using the MCP endpoint", "/docs/guides/connect-a-coding-agent"},
		{"memory", "Project memory", "beta", false, "Project notes and memory pages", "/docs/guides/project-notes"},
	}
}

func feature(area string) (Feature, error) {
	for _, f := range FeatureCatalogue() {
		if f.Area == area {
			return f, nil
		}
	}
	return Feature{}, fault.New(fault.Invalid, "unknown feature area: "+area)
}

func featureIn(ctx context.Context, q database.Queryer, area string) (Feature, error) {
	f, err := feature(area)
	if err != nil || f.Tier == "always" {
		return f, err
	}
	value, err := InstanceSettingIn(ctx, q, "feature."+area)
	if err != nil {
		return f, err
	}
	if value != "" {
		f.Enabled, err = strconv.ParseBool(value)
		if err != nil {
			return f, fmt.Errorf("invalid stored feature %s: %w", area, err)
		}
	}
	return f, nil
}

// Features reads the current choices. A failed read never becomes a default.
func (s *Service) Features(ctx context.Context) ([]Feature, error) {
	out := FeatureCatalogue()
	for i := range out {
		f, err := featureIn(ctx, s.db, out[i].Area)
		if err != nil {
			return nil, err
		}
		out[i] = f
	}
	return out, nil
}

// RequireFeature refuses access to a disabled area. Workflow executors do not
// call this: an adopted workflow retains all its declared steps.
func (s *Service) RequireFeature(ctx context.Context, area string) error {
	return RequireFeatureIn(ctx, s.db, area)
}

// RequireFeatureIn is the availability check for a command owner's database
// or transaction. The owner still checks its caller's authority before effects.
func RequireFeatureIn(ctx context.Context, q database.Queryer, area string) error {
	if area == "conformance-estate" || area == "what-ifs" {
		if err := RequireFeatureIn(ctx, q, "conformance"); err != nil {
			return err
		}
	}
	f, err := featureIn(ctx, q, area)
	if err != nil {
		return err
	}
	if !f.Enabled {
		return fault.New(fault.Forbidden, f.Name+" is turned off; an administrator can turn it on in Features")
	}
	return nil
}

// FeatureEnabledIn checks availability for fresh optional background work.
// Disabled is a normal no-work outcome; a failed settings read remains an error.
func FeatureEnabledIn(ctx context.Context, q database.Queryer, area string) (bool, error) {
	if area == "conformance-estate" || area == "what-ifs" {
		on, err := FeatureEnabledIn(ctx, q, "conformance")
		if err != nil || !on {
			return false, err
		}
	}
	f, err := featureIn(ctx, q, area)
	return f.Enabled, err
}

// SetFeature checks current administrator authority and commits the choice and
// receipt together. expected protects an editor from overwriting another edit.
func (s *Service) SetFeature(ctx context.Context, pr access.Principal, area string, expected, enabled bool) (Feature, error) {
	var out Feature
	err := s.db.InTx(ctx, func(tx *database.Tx) error {
		now := s.now()
		if err := s.requireAdministration(ctx, tx, pr, access.CapIdentityAuth, now); err != nil {
			return err
		}
		f, err := feature(area)
		if err != nil {
			return err
		}
		if f.Tier == "always" {
			return fault.New(fault.Invalid, f.Name+" is always on")
		}
		if err := queries(tx).LockInstanceSetting(ctx, "feature."+area); err != nil {
			return err
		}
		before, err := featureIn(ctx, tx, area)
		if err != nil {
			return err
		}
		if before.Enabled != expected {
			return fault.New(fault.Conflict, "Features changed; reload before saving")
		}
		if _, err := queries(tx).PutInstanceSetting(ctx, accesssql.PutInstanceSettingParams{Key: "feature." + area, Value: strconv.FormatBool(enabled)}); err != nil {
			return err
		}
		f.Enabled = enabled
		if err := audit.At(now).RecordIn(ctx, tx, pr.Name, ids.Nil, "feature.changed", area, map[string]any{"before": before.Enabled, "enabled": enabled, "principal": pr.ID}); err != nil {
			return err
		}
		out = f
		return nil
	})
	return out, err
}

// FeatureState reads an area's effective state without conflating an off choice
// with a failed settings read. Shared pages use it to omit optional sections.
func (s *Service) FeatureState(ctx context.Context, area string) (Feature, error) {
	return featureIn(ctx, s.db, area)
}
