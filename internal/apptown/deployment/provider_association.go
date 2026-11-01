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

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/ids"
)

// intranetConnection is the configured intranet's Portainer connection.
const intranetConnection = "portainer-deploy"

// observeIntranetScope records the configured intranet target's provider
// scope the first time a start finds it without one, as attachment does
// before it commits (deploy-targets.md, "Portainer connection identity"). It
// observes the endpoint the target's environment names, the daemon behind
// it and each existing deployment's stack, and commits scope, connection and
// stack associations with a receipt to AppTown itself. The association
// records current evidence, never creation or deletion entitlement, and does
// not lift restore holds. A failure leaves the target without a scope, so
// releases to it refuse; the next start observes again.
func (s *Service) observeIntranetScope(ctx context.Context, target *Target, who string) error {
	if target.Kind != KindPortainer || target.Status == TargetRetired || len(target.ProviderScope) > 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var binding *integration.Binding
	var err error
	if target.ConnectionID.IsZero() {
		binding, err = s.env.integrations.Bindings.ByName(ctx, intranetConnection)
	} else {
		binding, err = s.env.integrations.Bindings.ByID(ctx, target.ConnectionID)
	}
	if err != nil {
		return err
	}
	cfg, err := s.env.integrations.PortainerConnection(ctx, binding.ID)
	if err != nil {
		return err
	}
	cfg.EnvName, _ = target.Address[addressEnvironment].(string)
	endpoint, err := portainer.EndpointID(ctx, cfg)
	if err != nil {
		return err
	}
	scope, err := portainer.ObserveScope(ctx, *cfg, endpoint)
	if err != nil {
		return err
	}
	cfg.PortainerURL = scope.APIBase
	cfg.Scope = &scope
	retained, err := s.Retained.CountOpen(ctx, target.ID)
	if err != nil {
		return err
	}
	if retained > 0 {
		return errors.New("its retained resources are unresolved; association cannot invent their historical scope")
	}
	snapshots, resources, err := s.observeProviderResources(ctx, target, *cfg, scope)
	if err != nil {
		return err
	}
	at := s.now()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := deploymentsql.New(tx.SQL())
		if err := q.LockTargets(ctx); err != nil {
			return err
		}
		current, err := targetRecord(q.TargetByNameForUpdate(ctx, target.Name))
		if err != nil {
			return err
		}
		if current.ID != target.ID || current.SettingsRevision != target.SettingsRevision || len(current.ProviderScope) > 0 || !reflect.DeepEqual(current.Address, target.Address) {
			return errors.New("the target changed during provider observation")
		}
		if err := integration.MatchConnectionIn(ctx, tx, binding.ID, binding.Version); err != nil {
			return err
		}
		currentRows, err := q.ProviderDeploymentsOnTarget(ctx, target.ID)
		if err != nil {
			return err
		}
		if len(currentRows) != len(snapshots) {
			return errors.New("the target's deployments changed during provider observation")
		}
		for i, snapshot := range snapshots {
			if currentRows[i].ID != snapshot.row.ID {
				return errors.New("the target's deployments changed during provider observation")
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
				return errors.New("a deployment changed during provider observation")
			}
		}
		encoded, err := json.Marshal(scope)
		if err != nil {
			return err
		}
		scopeJSON := json.RawMessage(encoded)
		if _, err := q.AssociateTargetProvider(ctx, deploymentsql.AssociateTargetProviderParams{ID: target.ID, ConnectionID: binding.ID, Provider: binding.Name, ExpectedRevision: target.SettingsRevision, ProviderScopeJson: &scopeJSON, At: at}); err != nil {
			return err
		}
		return audit.At(at).RecordIn(ctx, tx, who, ids.Nil, "target.provider-associated", target.Name, map[string]any{"connectionId": binding.ID, "scope": scope, "resources": resources})
	})
}

type associationSnapshot struct {
	row       deploymentsql.Deployment
	resources []portainer.StackAssociation
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
