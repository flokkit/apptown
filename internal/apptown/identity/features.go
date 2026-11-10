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
	// Off is what turning a switchable area off does: what stops, what keeps
	// running, and what turning it on again brings back. Always-on areas
	// have none.
	Off string `json:"off,omitempty"`
	// Needs names the area this one works within: while that area is off,
	// this one is off too, whatever its own switch says, and its own
	// choice is kept for when that area is on again.
	Needs string `json:"needs,omitempty"`
}

// featureNeeds is each area that works within another.
var featureNeeds = map[string]string{
	"what-ifs":           "conformance",
	"conformance-estate": "conformance",
	"restore-rehearsals": "backups", // a rehearsal is a restore from a backup
}

// featureOff says, for each switchable area, what turning it off means.
var featureOff = map[string]string{
	"conformance":        "Hides the Conformance pages and refuses estate reviews, verdict confirmations and fixes from the web, the command line and outside agents. Adoption no longer starts reviews and no new fix items are made. Each change's own standards check still runs whenever its adopted workflow requires it. Past reviews and verdicts are kept and come back when it is turned on again; Pack upgrades and the estate view need it on too.",
	"backups":            "Pauses backups of your apps' data: no scheduled or after-release backups and no scheduled cloud copies. The Backups pages, Back up now and restores are refused, and restore rehearsals with them. Use it when your apps' data is backed up another way, such as filesystem snapshots. Existing backups are kept; turning it on again lets the next scheduled pass back up again. Work already started finishes. AppTown's own backups have their own switch.",
	"apptown-backup":     "Stops the daily dump of AppTown's own built-in database, kept in its data folder. Use it when AppTown itself is backed up another way, such as filesystem snapshots of its host. Dumps already taken are kept, and a whole-AppTown backup by hand (apptownd backup) still works. Turning it on again resumes the daily dump. Your apps' backups have their own switch.",
	"updates":            "AppTown stops checking for new versions and the Updates page is hidden. Use it for an install upgraded through its own tooling or with no internet access. Nothing installed changes; turning it on again resumes the checks.",
	"evals":              "Hides evals and starts no new eval runs. Runs already started finish and their results are kept.",
	"what-ifs":           "Hides Pack upgrades: the projects behind on a pack, and what-ifs that try a newer pack version on them before anyone adopts it. Adoption changes already opened from a what-if are ordinary changes and carry on. Needs Conformance on.",
	"restore-rehearsals": "Starts no new restore rehearsals or restored copies. Rehearsal places already made are still cleaned up, and past results are kept.",
	"conformance-estate": "Hides the estate-wide conformance inventory. Each project's own conformance pages are unaffected. Needs Conformance on.",
	"fly-machines":       "Fly.io can no longer be chosen or set up as a target. Deployments already on it keep running, and their history and restart stay available.",
	"app-platform":       "DigitalOcean App Platform can no longer be chosen or set up as a target. Deployments already on it keep running, and their history and start, stop and restart stay available.",
	"portainer":          "Portainer can no longer be chosen or set up as a target. Deployments already on it keep running, and their history and start, stop and restart stay available.",
	"bucket-site":        "S3 bucket sites can no longer be chosen or set up as targets. Sites already published keep serving, and their history stays available.",
	"mcp":                "Outside coding agents can no longer connect over MCP; their requests are refused. AppTown's own agents and chats are unaffected.",
	"memory":             "Hides project notes and memory pages. Decisions stay part of the record, and notes already in a repository stay there.",
}

// FeatureCatalogue is the closed inventory of product areas and their defaults.
// Callers get a copy; stored choices belong solely to instance settings.
func FeatureCatalogue() []Feature {
	out := []Feature{
		{"projects", "Projects", "always", true, "Projects and repositories", "/docs/guides/new-project", "", ""},
		{"changes", "Changes", "always", true, "Changes, runs and adopted workflows", "/docs/guides/the-change", "", ""},
		{"landing", "Landing", "always", true, "Checks and acceptance required by the adopted workflow", "/docs/guides/changes-and-main", "", ""},
		{"releases", "Releases", "always", true, "Release history, promotion and rollback", "/docs/guides/container-lifecycle", "", ""},
		{"deployments", "Deployments", "always", true, "Deployments and Docker targets", "/docs/guides/deployment-targets", "", ""},
		{"people", "People and sign-in", "always", true, "Accounts, sign-in, tokens and authority", "/docs/guides/people-and-roles", "", ""},
		{"record", "The record", "always", true, "History, decisions, operations and evidence", "/docs/guides/changes-and-main", "", ""},
		{"setup", "Setup", "always", true, "Instance settings, credentials and Features", "/docs/guides/features", "", ""},
		{"conformance", "Conformance", "standard", true, "Estate reviews, verdicts and fixes; each change's standards check runs either way", "/docs/guides/conformance", "", ""},
		{"backups", "Deployment backups", "standard", true, "Backups and restores of your apps' data", "/docs/guides/deployment-backups", "", ""},
		{"apptown-backup", "AppTown's own backups", "standard", true, "The daily dump of AppTown's own database", "/docs/guides/apptown-backups", "", ""},
		{"ways", "Ways of working", "always", true, "Packs, workflow, guidance and policy pages", "/docs/guides/packs-and-what-if", "", ""},
		{"chats", "Chats", "always", true, "Interactive change chats", "/docs/guides/the-change", "", ""},
		{"usage", "Usage", "always", true, "Usage and disk pages", "/docs/guides/monitoring", "", ""},
		{"worker", "Worker", "always", true, "Worker settings", "/docs/guides/split-install", "", ""},
		{"previews", "Previews", "always", true, "Change previews", "/docs/guides/how-apps-run", "", ""},
		{"models", "Models", "always", true, "Model setup pages", "/docs/guides/runtime-environments", "", ""},
		{"updates", "Update checks", "standard", true, "Checking for new AppTown versions, and the Updates page", "/docs/guides/upgrading", "", ""},
		{"complaints", "Complaints", "always", true, "Reports about AppTown", "/docs/guides/features", "", ""},
		{"evals", "Evals", "beta", false, "Model and workflow evaluations", "/docs/guides/packs-and-what-if", "", ""},
		{"what-ifs", "Pack upgrades", "beta", false, "Projects behind on a pack, and trying a newer version first", "/docs/guides/packs-and-what-if", "", ""},
		{"restore-rehearsals", "Restore rehearsals", "beta", false, "Rehearsal and restored-copy environments", "/docs/guides/deployment-backups", "", ""},
		{"conformance-estate", "Conformance estate view", "beta", false, "Estate conformance inventory", "/docs/guides/conformance", "", ""},
		{"app-platform", "App Platform targets", "beta", false, "DigitalOcean App Platform targets", "/docs/guides/deployment-targets", "", ""},
		{"fly-machines", "Fly.io targets", "beta", false, "Fly.io Machines targets", "/docs/guides/deployment-targets", "", ""},
		{"portainer", "Portainer targets", "beta", false, "Portainer targets", "/docs/guides/deployment-targets", "", ""},
		{"bucket-site", "Bucket-site targets", "beta", false, "S3 bucket sites", "/docs/guides/deployment-targets", "", ""},
		{"mcp", "MCP for outside agents", "beta", false, "Outside agents using the MCP endpoint", "/docs/guides/connect-a-coding-agent", "", ""},
		{"memory", "Project memory", "beta", false, "Project notes and memory pages", "/docs/guides/project-notes", "", ""},
	}
	for i := range out {
		out[i].Off = featureOff[out[i].Area]
		out[i].Needs = featureNeeds[out[i].Area]
	}
	return out
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
	f, err := featureIn(ctx, q, area)
	if err != nil {
		return err
	}
	if f.Needs != "" {
		if err := RequireFeatureIn(ctx, q, f.Needs); err != nil {
			return err
		}
	}
	if !f.Enabled {
		return fault.New(fault.Forbidden, f.Name+" is turned off; an administrator can turn it on in Features")
	}
	return nil
}

// FeatureEnabledIn checks availability for fresh optional background work.
// Disabled is a normal no-work outcome; a failed settings read remains an error.
func FeatureEnabledIn(ctx context.Context, q database.Queryer, area string) (bool, error) {
	f, err := featureIn(ctx, q, area)
	if err != nil || !f.Enabled || f.Needs == "" {
		return f.Enabled, err
	}
	return FeatureEnabledIn(ctx, q, f.Needs)
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
