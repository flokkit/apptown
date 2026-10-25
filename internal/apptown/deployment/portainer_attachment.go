package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// PortainerAttachment selects one connection and endpoint. Credential references
// are the RC7/RC8 attachment form; they materialize a visible connection atomically.
type PortainerAttachment struct {
	Name         string
	ConnectionID ids.ID
	EndpointID   int64
	Environment  string
	Domain       string
	URLFrom      string
	APIKeyFrom   string
	DryRun       bool
}

// AttachPortainerTarget owns authority, provider observation and atomic connection
// plus target creation. A failed check never leaves an orphan connection behind.
func (s *Service) AttachPortainerTarget(ctx context.Context, in PortainerAttachment) (*Target, error) {
	if s.authority == nil {
		return nil, access.ErrAuthorityUnavailable
	}
	actor, err := s.authority.Require(ctx, access.Permission{Capability: access.CapPlatformTargets, Resource: access.Resource{Type: "instance"}, Context: access.Context{Route: "deployment.attach-portainer", Consequential: !in.DryRun}})
	if err != nil {
		return nil, err
	}
	if actor.Principal.Name == "" {
		return nil, errors.New("target attachment caller required")
	}
	if !project.ValidSlug(in.Name) {
		return nil, fault.New(fault.Invalid, "target name must be a slug")
	}
	in.Domain, err = NormalizeTargetDomain(in.Domain)
	if err != nil {
		return nil, err
	}
	if !in.ConnectionID.IsZero() && (in.URLFrom != "" || in.APIKeyFrom != "") {
		return nil, fault.New(fault.Invalid, "choose a connection or explicit credential references, not both")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var binding *integration.Binding
	var cfg *portainer.Target
	if !in.ConnectionID.IsZero() {
		binding, err = s.env.integrations.Bindings.ByID(ctx, in.ConnectionID)
		if err != nil {
			return nil, err
		}
		cfg, err = s.env.integrations.PortainerConnection(ctx, in.ConnectionID)
		if err != nil {
			return nil, err
		}
	} else {
		if in.URLFrom == "" || in.APIKeyFrom == "" {
			return nil, fault.New(fault.Invalid, "a Portainer target needs a connection or URL and API-key secret references")
		}
		values, err := s.providers.secrets.WithContext(ctx)
		if err != nil {
			return nil, err
		}
		cfg = &portainer.Target{PortainerURL: values[in.URLFrom], PortainerKey: values[in.APIKeyFrom]}
		if cfg.PortainerURL == "" || cfg.PortainerKey == "" {
			return nil, fault.New(fault.Conflict, "Portainer credential references do not resolve")
		}
	}
	cfg.EnvName = in.Environment
	endpoint := in.EndpointID
	if endpoint == 0 {
		endpoint, err = portainer.EndpointID(ctx, cfg)
		if err != nil {
			return nil, fault.Wrap(fault.Unprocessable, err)
		}
	}
	scope, err := portainer.ObserveScope(ctx, *cfg, endpoint)
	if err != nil {
		return nil, fault.Wrap(fault.Unprocessable, err)
	}
	if in.Environment == "" {
		cfg.PortainerURL, cfg.Scope = scope.APIBase, &scope
		observed, err := portainer.ReadEnvironment(ctx, cfg)
		if err != nil {
			return nil, fault.Wrap(fault.Unprocessable, err)
		}
		in.Environment = strings.TrimSpace(observed.Name)
		if in.Environment == "" {
			return nil, fault.New(fault.Unprocessable, "Portainer endpoint has no usable display name")
		}
	}
	needs, err := s.env.needs.EmulatorKinds(ctx)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(scope)
	if err != nil {
		return nil, err
	}
	scopeJSON := json.RawMessage(encoded)
	at := s.now()
	target := &Target{ID: ids.New(at), Name: in.Name, Kind: KindPortainer, ConnectionID: in.ConnectionID, ProviderScope: scopeJSON, Address: map[string]any{AddressEnvironment: in.Environment, "endpoint": endpoint}, Origin: OriginAttached, Capabilities: StockCapabilities(KindPortainer, needs), Ingress: map[string]any{"binding": "traefik", "domain": in.Domain}, NeedsMode: "emulators", SecretsDelivery: "inject", Status: TargetReady}
	if in.DryRun {
		return target, nil
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Target, error) {
		if binding == nil {
			binding, err = integration.CreatePortainerConnectionIn(ctx, tx, "portainer-"+target.ID.String(), in.URLFrom, in.APIKeyFrom, actor.Principal.Name, at)
			if err != nil {
				return nil, err
			}
		} else if err := integration.MatchConnectionIn(ctx, tx, binding.ID, binding.Version); err != nil {
			return nil, err
		}
		row, err := deploymentsql.New(tx.SQL()).InsertProviderTarget(ctx, deploymentsql.InsertProviderTargetParams{ID: target.ID, Name: target.Name, Kind: target.Kind, Provider: binding.Name, ConnectionID: binding.ID, ProviderScopeJson: &scopeJSON, AddressJson: json.RawMessage(jsonText(target.Address)), Origin: target.Origin, CapabilitiesJson: json.RawMessage(jsonText(target.Capabilities)), IngressJson: json.RawMessage(jsonText(target.Ingress)), NeedsMode: target.NeedsMode, SecretsDelivery: target.SecretsDelivery, Status: target.Status.String(), At: at})
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fault.New(fault.Conflict, "a target with this name already exists")
		}
		saved, err := targetRecord(row, err)
		if err != nil {
			return nil, err
		}
		detail := fmt.Sprintf("Portainer endpoint %d verified", scope.EndpointID)
		if err := audit.At(at).RecordIn(ctx, tx, actor.Principal.Name, ids.Nil, "target.attached", target.Name, map[string]any{"kind": KindPortainer, "connectionId": binding.ID, "scope": scope}); err != nil {
			return nil, err
		}
		if err := audit.At(at).RecordIn(ctx, tx, actor.Principal.Name, ids.Nil, "target.checked", target.Name, map[string]any{"ok": true, "detail": detail}); err != nil {
			return nil, err
		}
		return saved, nil
	})
}
