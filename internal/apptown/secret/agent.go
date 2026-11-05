package secret

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/secret/internal/secretsql"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
)

const sharedPreviewValue = "This value is used by a deployment. Give previews their own value to let agents see it"

// PreviewOwned checks storage scope and all projects' non-preview Infisical
// references, never plaintext. Callers must supply the current references.
func (b Binding) PreviewOwned(slug string, nonPreviewRefs []string) bool {
	if b.Store == StoreInfisical {
		return b.Deployment == "preview" && !slices.Contains(nonPreviewRefs, b.Ref)
	}
	return b.Deployment == "preview" && b.Store == StoreInternal && b.Ref == Scope(slug, "preview", "")
}

func (a *Service) requireBinding(ctx context.Context, projectID ids.ID) (string, error) {
	if a.authority == nil {
		return "", access.ErrAuthorityUnavailable
	}
	caller, err := a.authority.Require(ctx, access.Permission{Capability: access.CapSecretBind, Resource: access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}})
	return caller.Receipt, err
}

// SetAgentVisible authorizes secret:bind itself. The flag and its receipt commit
// together; only preview-owned bindings may be enabled. Disabling affects future launches.
func (a *Service) SetAgentVisible(ctx context.Context, projectID ids.ID, deployment, name string, visible bool) error {
	who, err := a.requireBinding(ctx, projectID)
	if err != nil {
		return err
	}
	if deployment != "preview" {
		return fault.New(fault.Invalid, "Only preview secrets can be given to agents")
	}
	slug, err := a.projectSlug(ctx, projectID)
	if err != nil {
		return err
	}
	tx, err := a.Bindings.begin(ctx, projectID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := queries(tx).BindingByName(ctx, secretsql.BindingByNameParams{ProjectID: projectID, Deployment: deployment, Name: name})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBindingMissing
	}
	if err != nil {
		return err
	}
	refs, err := queries(tx).NonPreviewReferences(ctx)
	if err != nil {
		return err
	}
	if visible && !binding(row).PreviewOwned(slug, refs) {
		return fault.New(fault.Conflict, sharedPreviewValue)
	}
	actor, err := identity.ActorIn(ctx, tx, who)
	if err != nil {
		return err
	}
	at := a.now()
	if err := queries(tx).SetAgentVisible(ctx, secretsql.SetAgentVisibleParams{ProjectID: projectID, Deployment: deployment, Name: name, AgentVisible: visible, UpdatedAt: at, UpdatedBy: who, UpdatedByPrincipal: actor.Principal, UpdatedByVia: actor.Via}); err != nil {
		return err
	}
	if err := audit.At(at).RecordIn(ctx, tx, who, projectID, "secret.agent-visibility", name, map[string]any{"deployment": deployment, "agentVisible": visible}); err != nil {
		return err
	}
	return tx.Commit()
}

// SeparatePreview gives a shared generated binding a fresh preview value. Main's
// reference and stored versions are untouched. Typed values use BindStored instead.
func (a *Service) SeparatePreview(ctx context.Context, projectID ids.ID, name string) error {
	who, err := a.requireBinding(ctx, projectID)
	if err != nil {
		return err
	}
	slug, err := a.projectSlug(ctx, projectID)
	if err != nil {
		return err
	}
	tx, err := a.Bindings.begin(ctx, projectID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := queries(tx).BindingByName(ctx, secretsql.BindingByNameParams{ProjectID: projectID, Deployment: "preview", Name: name})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBindingMissing
	}
	if err != nil {
		return err
	}
	b := binding(row)
	if !b.Generated || b.PreviewOwned(slug, nil) {
		return fault.New(fault.Conflict, "This secret does not need a separate generated preview value")
	}
	value, err := GenerateValue()
	if err != nil {
		return err
	}
	sealed, err := a.Vault.seal(ctx, value)
	if err != nil {
		return err
	}
	b.Ref = Scope(slug, "preview", "")
	version, err := a.Vault.putIn(ctx, tx, b.Ref, name, sealed, who)
	if err != nil {
		return err
	}
	b.Store, b.Version, b.UpdatedBy = StoreInternal, fmt.Sprint(version), who
	if err := a.Bindings.putIn(ctx, tx, projectID, b); err != nil {
		return err
	}
	return tx.Commit()
}

// AgentLaunch identifies a workflow invocation or chat wake. Review and project
// sessions are excluded before any secret resolution.
type AgentLaunch struct {
	Kind       string `json:"kind"`
	ChangeID   ids.ID `json:"changeId"`
	RunID      ids.ID `json:"runId,omitzero"`
	StepID     ids.ID `json:"stepId,omitzero"`
	Attempt    int64  `json:"attempt,omitempty"`
	Invocation string `json:"invocation,omitempty"`
	Session    string `json:"session,omitempty"`
}

// AgentDelivery separates resolution from the container creation that gives values.
type AgentDelivery struct {
	Env        []string               `json:"-"`
	Profile    string                 `json:"profile"`
	Secrets    []docker.SecretReceipt `json:"secrets"`
	References map[string]string      `json:"references,omitempty"`
	NotGiven   map[string]string      `json:"notGiven,omitempty"`
}

// Digest identifies only the names, profile, stores and actual versions given.
func (d AgentDelivery) Digest() string {
	raw, _ := json.Marshal(struct {
		Profile    string
		Secrets    []docker.SecretReceipt
		References map[string]string
	}{d.Profile, d.Secrets, d.References})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// recordAgentDelivery names the launch and, for chats, the container actually created.
func (a *Service) recordAgentDelivery(ctx context.Context, projectID ids.ID, launch AgentLaunch, d AgentDelivery, container string) error {
	if len(d.Secrets) == 0 && len(d.NotGiven) == 0 {
		return nil
	}
	return audit.At(a.now()).Record(ctx, a.Vault.db, "apptown-system", projectID, "secret.agent-delivery", ids.New(a.now()).String(), map[string]any{
		"launch": launch, "profile": d.Profile, "secrets": d.Secrets, "notGiven": d.NotGiven, "container": container, "references": d.References,
	})
}

func (a *Service) AgentEnv(ctx context.Context, projectID ids.ID, ws string, launch AgentLaunch) ([]string, error) {
	d, err := a.PrepareAgentEnv(ctx, projectID, ws, launch)
	if err != nil {
		return nil, err
	}
	if err := a.recordAgentDelivery(ctx, projectID, launch, d, ""); err != nil {
		return nil, err
	}
	return d.Env, nil
}

// PrepareAgentEnv resolves without recording delivery. Resolution failures omit
// the affected values; evidence failures still prevent delivery.
func (a *Service) PrepareAgentEnv(ctx context.Context, projectID ids.ID, ws string, launch AgentLaunch) (d AgentDelivery, err error) {
	if launch.ChangeID.IsZero() || (launch.Kind != "agent" && launch.Kind != "change-chat") {
		return d, nil
	}
	bindings, err := a.agentBindings(ctx, projectID)
	if err != nil || len(bindings) == 0 {
		return d, err
	}
	defer func() {
		if err != nil {
			d.omitAll(bindings, err)
			err = nil
		}
	}()
	c, err := docker.ReadWorkspaceContract(ws)
	if err != nil && err != docker.ErrNoContract {
		return d, err
	}
	specPath := ""
	if c != nil {
		specPath = c.SecretSpecPath
	}
	spec, present, err := ReadSpec(ws, specPath)
	if err != nil {
		return d, err
	}
	if !present {
		return d, ErrNotStored
	}
	err = a.agentEnv(ctx, projectID, spec, c, bindings, &d)
	return d, err
}

func (d *AgentDelivery) omit(name, reason string) {
	if d.NotGiven == nil {
		d.NotGiven = map[string]string{}
	}
	d.NotGiven[name] = "not given: " + reason
}
func (d *AgentDelivery) omitAll(bindings map[string]Binding, err error) {
	for name := range bindings {
		d.omit(name, agentResolutionReason(err))
	}
}

// Provider and parser errors can contain source text or credentials. Record a
// classification rather than copying their messages into a permanent record.
func agentResolutionReason(err error) string {
	if errors.Is(err, ErrNotStored) {
		return "value is not stored"
	}
	var unavailable *UnavailableError
	if errors.As(err, &unavailable) {
		return unavailable.store + " store could not be read"
	}
	return "could not resolve the preview declaration or profile"
}

func (a *Service) agentEnv(ctx context.Context, projectID ids.ID, spec *spec, c *appcontract.Contract, bindings map[string]Binding, d *AgentDelivery) error {
	chosen, _, err := a.DeploymentProfile(ctx, projectID, "preview", spec, c)
	if err != nil {
		return err
	}
	d.Profile = SpecProfile(spec, chosen)
	var inf *secretResolution
	var infErr error
	for _, s := range spec.SecretsIn(d.Profile) {
		b, ok := bindings[s.Name]
		if !ok {
			continue
		}
		var value, version string
		var err error
		if b.Store == StoreInfisical {
			if inf == nil {
				inf = &secretResolution{}
				file, readErr := a.readFileSecrets()
				if readErr != nil {
					infErr = readErr
				} else {
					*inf, infErr = a.infisicalResolution(ctx, infisicalConfig(file))
				}
			}
			value, ok, err = inf.Values[b.Ref], inf.Values[b.Ref] != "", infErr
			if err != nil {
				d.omit(s.Name, "Infisical store could not be read")
				continue
			}
			if err == nil && ok && inf.Versions[b.Ref] <= 0 {
				d.omit(s.Name, "Infisical did not supply a version")
				continue
			}
			version = fmt.Sprint(inf.Versions[b.Ref])
		} else {
			var unused map[string]string
			value, version, ok, err = a.boundValue(ctx, "preview", b, s.Name, &unused)
		}
		if err != nil {
			d.omit(s.Name, agentResolutionReason(err))
			continue
		}
		if !ok {
			d.omit(s.Name, "value is not stored")
			continue
		}
		if d.References == nil {
			d.References = map[string]string{}
		}
		d.References[s.Name] = b.Ref
		d.Env = append(d.Env, s.Name+"="+value)
		d.Secrets = append(d.Secrets, docker.SecretReceipt{Name: s.Name, Store: b.Store, Version: version, Generated: b.Generated})
	}
	sort.Slice(d.Secrets, func(i, j int) bool { return d.Secrets[i].Name < d.Secrets[j].Name })
	return nil
}

// AgentEnvAt reads the worker launch's exact commit, never a mutable checkout.
func (a *Service) AgentEnvAt(ctx context.Context, projectID ids.ID, repo, commit string, launch AgentLaunch) (env []string, err error) {
	if launch.ChangeID.IsZero() || launch.Kind != "agent" {
		return nil, nil
	}
	bindings, err := a.agentBindings(ctx, projectID)
	if err != nil || len(bindings) == 0 {
		return nil, err
	}
	d := AgentDelivery{}
	defer func() {
		if err != nil {
			d.omitAll(bindings, err)
			env = nil
			err = nil
		}
		if recordErr := a.recordAgentDelivery(ctx, projectID, launch, d, ""); recordErr != nil {
			env = nil
			err = recordErr
		}
	}()
	raw, err := docker.ContractFileAt(ctx, repo, commit)
	if err != nil && err != docker.ErrNoContract {
		return nil, err
	}
	var c *appcontract.Contract
	if raw != nil {
		c, err = appcontract.Parse(raw)
		if err != nil {
			return nil, err
		}
	}
	path := SpecFile
	if c != nil && c.SecretSpecPath != "" {
		path = c.SecretSpecPath
	}
	if err := appcontract.CheckSpecPath(path); err != nil {
		return nil, err
	}
	entry, err := gitexec.HubOutput(ctx, repo, nil, "ls-tree", "--full-tree", commit, "--", path)
	if err != nil {
		return nil, err
	}
	if len(entry) == 0 {
		return nil, ErrNotStored
	}
	fields := strings.Fields(string(entry))
	if len(fields) < 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return nil, fault.New(fault.Invalid, "The secret declaration must be a regular file")
	}
	raw, err = gitexec.HubOutput(ctx, repo, nil, "cat-file", "blob", fields[2])
	if err != nil {
		return nil, err
	}
	spec, err := ParseSpec(raw)
	if err != nil {
		return nil, err
	}
	if err := a.agentEnv(ctx, projectID, spec, c, bindings, &d); err != nil {
		return nil, err
	}
	return d.Env, nil
}

// Select before reading source: projects without disclosure keep their ordinary
// agent launch behavior, including agents asked to repair invalid declarations.
func (a *Service) agentBindings(ctx context.Context, projectID ids.ID) (map[string]Binding, error) {
	slug, err := a.projectSlug(ctx, projectID)
	if err != nil {
		return nil, err
	}
	bound, err := a.Bindings.For(ctx, projectID, "preview")
	if err != nil {
		return nil, err
	}
	refs, err := a.NonPreviewReferences(ctx)
	if err != nil {
		return nil, err
	}
	selected := map[string]Binding{}
	for name, b := range bound {
		if b.AgentVisible && b.PreviewOwned(slug, refs) {
			selected[name] = b
		}
	}
	return selected, nil
}

// NonPreviewReferences lists the Infisical keys used outside previews across all projects.
func (a *Service) NonPreviewReferences(ctx context.Context) ([]string, error) {
	return queries(a.Vault.db).NonPreviewReferences(ctx)
}

func (a *Service) projectSlug(ctx context.Context, id ids.ID) (string, error) {
	if a.projects == nil {
		return "", errors.New("project scope is unavailable")
	}
	return a.projects.Slug(ctx, id)
}
