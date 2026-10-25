package deployment

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// ProviderAssociationSelection confirms a specific observed destination. Expected
// is required when committing; it is compared before credentials leave AppTown.
type ProviderAssociationSelection struct {
	ConnectionID     ids.ID                       `json:"connectionId"`
	EndpointID       int64                        `json:"endpointId"`
	ExpectedRevision int64                        `json:"expectedRevision"`
	Expected         *portainer.Scope             `json:"expected,omitempty"`
	Resources        []portainer.StackAssociation `json:"resources,omitempty"`
}

// ProviderAssociation describes current evidence, never a historical ownership
// claim. Associating a target does not grant creation/deletion entitlement.
type ProviderAssociation struct {
	TargetID     ids.ID                       `json:"targetId"`
	ConnectionID ids.ID                       `json:"connectionId"`
	Revision     int64                        `json:"revision"`
	Scope        portainer.Scope              `json:"scope"`
	Resources    []portainer.StackAssociation `json:"resources"`
	Associated   bool                         `json:"associated"`
}

type associationSnapshot struct {
	row       deploymentsql.Deployment
	resources []portainer.StackAssociation
}

// InspectProviderAssociation observes an explicitly selected legacy destination.
// Existing verified targets always use their saved selection instead.
func (s *Service) InspectProviderAssociation(ctx context.Context, name string, in ProviderAssociationSelection) (ProviderAssociation, error) {
	return s.providerAssociation(ctx, name, in, false)
}

// AssociateProvider commits a fresh observation against the confirmed selection
// and revision. It does not write remote resources or lift restore holds.
func (s *Service) AssociateProvider(ctx context.Context, name string, in ProviderAssociationSelection) (ProviderAssociation, error) {
	return s.providerAssociation(ctx, name, in, true)
}

func (s *Service) providerAssociation(ctx context.Context, name string, in ProviderAssociationSelection, commit bool) (ProviderAssociation, error) {
	var out ProviderAssociation
	if s.authority == nil {
		return out, access.ErrAuthorityUnavailable
	}
	actor, err := s.authority.Require(ctx, access.Permission{Capability: access.CapPlatformTargets, Resource: access.Resource{Type: "instance"}, Context: access.Context{Route: "deployment.provider-association", Consequential: commit}})
	if err != nil {
		return out, err
	}
	if commit && actor.Principal.Name == "" {
		return out, errors.New("provider association caller required")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	target, err := s.Targets.ByName(ctx, name)
	if err != nil {
		return out, err
	}
	if target.Kind != KindPortainer {
		return out, fault.New(fault.Invalid, "this target is not Portainer")
	}
	if err := target.Status.Available(); err != nil {
		return out, err
	}
	if commit && (in.Expected == nil || in.ExpectedRevision != target.SettingsRevision) {
		return out, fault.New(fault.Conflict, "inspect and confirm the current provider association first")
	}
	if !target.ConnectionID.IsZero() {
		if !in.ConnectionID.IsZero() && in.ConnectionID != target.ConnectionID {
			return out, fault.New(fault.Conflict, "this target already selects a different connection")
		}
		in.ConnectionID = target.ConnectionID
	}
	var cfg *portainer.Target
	var binding *integration.Binding
	var urlRef, keyRef string
	if !in.ConnectionID.IsZero() {
		binding, err = s.env.integrations.Bindings.ByID(ctx, in.ConnectionID)
		if err != nil {
			return out, err
		}
		cfg, err = s.env.integrations.PortainerConnection(ctx, in.ConnectionID)
	} else {
		urlRef, _ = target.Address["urlFrom"].(string)
		keyRef, _ = target.Address["apiKeyFrom"].(string)
		if urlRef == "" || keyRef == "" {
			return out, fault.New(fault.Conflict, "select a Portainer connection explicitly; this target has no complete saved credential references")
		}
		values, readErr := s.providers.secrets.WithContext(ctx)
		if readErr != nil {
			return out, readErr
		}
		cfg = &portainer.Target{PortainerURL: values[urlRef], PortainerKey: values[keyRef]}
		if cfg.PortainerURL == "" || cfg.PortainerKey == "" {
			return out, fault.New(fault.Conflict, "the saved Portainer credential references do not resolve")
		}
	}
	if err != nil {
		return out, err
	}
	cfg.EnvName, _ = target.Address[AddressEnvironment].(string)
	if len(target.ProviderScope) > 0 {
		var saved portainer.Scope
		if err := json.Unmarshal(target.ProviderScope, &saved); err != nil {
			return out, err
		}
		if in.EndpointID != 0 && in.EndpointID != saved.EndpointID {
			return out, fault.New(fault.Conflict, "a verified target cannot change endpoint; create a new target")
		}
		if commit && *in.Expected != saved {
			return out, fault.New(fault.Conflict, "a verified target cannot replace its provider scope")
		}
		if err := portainer.VerifyScope(ctx, *cfg, saved); err != nil {
			return out, err
		}
		out.Scope = saved
	} else if commit {
		if in.EndpointID != 0 && in.EndpointID != in.Expected.EndpointID {
			return out, fault.New(fault.Conflict, "confirmation selects a different endpoint")
		}
		if err := portainer.VerifyScope(ctx, *cfg, *in.Expected); err != nil {
			return out, err
		}
		out.Scope = *in.Expected
	} else {
		endpoint := in.EndpointID
		if endpoint == 0 {
			endpoint, err = portainer.EndpointID(ctx, cfg)
			if err != nil {
				return out, err
			}
		}
		out.Scope, err = portainer.ObserveScope(ctx, *cfg, endpoint)
		if err != nil {
			return out, err
		}
	}
	cfg.PortainerURL = out.Scope.APIBase
	cfg.Scope = &out.Scope
	retained, err := s.Retained.CountOpen(ctx, target.ID)
	if err != nil {
		return out, err
	}
	if retained > 0 {
		return out, fault.New(fault.Conflict, "resolve this target's retained resources before associating its provider; association cannot invent their historical scope")
	}
	snapshots, resources, err := s.observeProviderResources(ctx, target, *cfg, out.Scope)
	if err != nil {
		return out, err
	}
	out.TargetID, out.ConnectionID, out.Revision, out.Resources = target.ID, in.ConnectionID, target.SettingsRevision, resources
	out.Associated = len(target.ProviderScope) > 0
	if !commit {
		return out, nil
	}
	if !reflect.DeepEqual(in.Resources, resources) && !(len(in.Resources) == 0 && len(resources) == 0) {
		return out, fault.New(fault.Conflict, "provider resource observations changed; inspect and confirm again")
	}
	if len(target.ProviderScope) > 0 {
		out.Associated = true
		return out, nil
	}
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		q := deploymentsql.New(tx.SQL())
		if err := q.LockTargets(ctx); err != nil {
			return err
		}
		current, err := targetRecord(q.TargetByNameForUpdate(ctx, name))
		if err != nil {
			return err
		}
		if current.ID != target.ID || current.SettingsRevision != target.SettingsRevision || len(current.ProviderScope) > 0 {
			return fault.New(fault.Conflict, "target changed during provider observation")
		}
		if current.ConnectionID != target.ConnectionID || !reflect.DeepEqual(current.Address, target.Address) {
			return fault.New(fault.Conflict, "target connection changed during provider observation")
		}
		if binding == nil {
			binding, err = integration.CreatePortainerConnectionIn(ctx, tx, "portainer-"+target.ID.String(), urlRef, keyRef, actor.Principal.Name, s.now())
			if err != nil {
				return err
			}
		} else if err := integration.MatchConnectionIn(ctx, tx, binding.ID, binding.Version); err != nil {
			return err
		}
		currentRows, err := q.ProviderDeploymentsOnTarget(ctx, target.ID)
		if err != nil {
			return err
		}
		if len(currentRows) != len(snapshots) {
			return fault.New(fault.Conflict, "target deployments changed during provider observation")
		}
		for i, snapshot := range snapshots {
			if currentRows[i].ID != snapshot.row.ID {
				return fault.New(fault.Conflict, "target deployments changed during provider observation")
			}
			payload, err := json.Marshal(snapshot.resources)
			if err != nil {
				return err
			}
			n, err := q.SetProviderAssociationHandle(ctx, deploymentsql.SetProviderAssociationHandleParams{ID: snapshot.row.ID, TargetID: target.ID, Generation: snapshot.row.DesiredGeneration, ExpectedHandle: snapshot.row.HandleJson, Associations: payload})
			if err != nil {
				return err
			}
			if n != 1 {
				return fault.New(fault.Conflict, "deployment changed during provider observation")
			}
		}
		encodedScope, err := json.Marshal(out.Scope)
		if err != nil {
			return err
		}
		scopeJSON := json.RawMessage(encodedScope)
		saved, err := q.AssociateTargetProvider(ctx, deploymentsql.AssociateTargetProviderParams{ID: target.ID, ConnectionID: binding.ID, Provider: binding.Name, ExpectedRevision: target.SettingsRevision, ProviderScopeJson: &scopeJSON, At: s.now()})
		if err != nil {
			return err
		}
		out.ConnectionID, out.Revision = binding.ID, saved.SettingsRevision
		return audit.At(s.now()).RecordIn(ctx, tx, actor.Principal.Name, ids.Nil, "target.provider-associated", name, map[string]any{"connectionId": binding.ID, "scope": out.Scope, "resources": resources})
	})
	if err != nil {
		return ProviderAssociation{}, err
	}
	out.Associated = true
	return out, nil
}

func (s *Service) observeProviderResources(ctx context.Context, target *Target, cfg portainer.Target, scope portainer.Scope) ([]associationSnapshot, []portainer.StackAssociation, error) {
	q := deploymentsql.New(s.db.SQL())
	rows, err := q.ProviderDeploymentsOnTarget(ctx, target.ID)
	if err != nil {
		return nil, nil, err
	}
	var snapshots []associationSnapshot
	var resources []portainer.StackAssociation
	var stacks []portainer.Stack
	if len(rows) > 0 {
		stacks, err = portainer.ListStacks(ctx, &cfg)
		if err != nil {
			return nil, nil, err
		}
	}
	slices.SortFunc(stacks, func(a, b portainer.Stack) int { return cmp.Compare(a.ID, b.ID) })
	for _, row := range rows {
		dep, err := deploymentRecord(row, nil)
		if err != nil {
			return nil, nil, err
		}
		owned, err := ownedIn(ctx, q, dep.ID)
		if err != nil {
			return nil, nil, err
		}
		wanted := map[int64]string{}
		for _, resource := range owned {
			if resource.Kind == "stack" {
				id, err := strconv.ParseInt(resource.ID, 10, 64)
				if err != nil {
					return nil, nil, errors.New("saved Portainer stack identity is malformed")
				}
				wanted[id] = resource.Name
			}
		}
		if name, _ := dep.Handle["stack"].(string); name != "" {
			found := false
			for _, stack := range stacks {
				if stack.Name == name && stack.EndpointID == scope.EndpointID {
					wanted[stack.ID] = name
					found = true
				}
			}
			if !found {
				return nil, nil, fmt.Errorf("deployment %s has an unresolved saved Portainer stack", dep.Name)
			}
		}
		snapshot := associationSnapshot{row: row}
		for _, stack := range stacks {
			name, ok := wanted[stack.ID]
			if !ok {
				continue
			}
			if stack.Name != name || stack.EndpointID != scope.EndpointID {
				return nil, nil, errors.New("saved Portainer stack resolves to a different resource")
			}
			association := portainer.StackAssociation{DeploymentID: dep.ID, Scope: scope, StackID: stack.ID, StackName: stack.Name}
			legacy := &association
			if len(target.ProviderScope) > 0 {
				legacy = nil
				saved, err := portainer.SavedAssociations(dep.Handle)
				if err != nil {
					return nil, nil, err
				}
				for i := range saved {
					if saved[i].StackID == stack.ID {
						legacy = &saved[i]
						break
					}
				}
			}
			if err := portainer.VerifyStackAssociation(ctx, cfg, stack, dep.ID, legacy); err != nil {
				return nil, nil, err
			}
			snapshot.resources = append(snapshot.resources, association)
			resources = append(resources, association)
			delete(wanted, stack.ID)
		}
		if len(wanted) > 0 {
			return nil, nil, fmt.Errorf("deployment %s has unresolved owned Portainer stack identities", dep.Name)
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, resources, nil
}
