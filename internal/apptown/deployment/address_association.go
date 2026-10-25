package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// AddressObservation is current provider evidence for a recorded deployment.
// Confirmation preserves the exact observed host spelling and certificate input;
// it grants no provider creation entitlement and never lifts a restore hold.
type AddressObservation struct {
	AddressSnapshot
	Scope          portainer.Scope            `json:"scope"`
	Stack          portainer.StackAssociation `json:"stack"`
	TargetRevision int64                      `json:"targetRevision"`
}

// InspectAddress requires configuration authority before reading the provider.
func (s *Service) InspectAddress(ctx context.Context, projectID, deploymentID ids.ID) (AddressObservation, error) {
	return s.associateAddress(ctx, projectID, deploymentID, nil)
}

// ConfirmAddress rereads the provider using one verified credential snapshot and
// records only the exact evidence the caller confirmed. It performs no remote write.
func (s *Service) ConfirmAddress(ctx context.Context, projectID, deploymentID ids.ID, expected AddressObservation) (AddressObservation, error) {
	return s.associateAddress(ctx, projectID, deploymentID, &expected)
}

func (s *Service) associateAddress(ctx context.Context, projectID, deploymentID ids.ID, expected *AddressObservation) (AddressObservation, error) {
	var out AddressObservation
	actor, err := s.addressAuthority(ctx, projectID, true)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	q := deploymentsql.New(s.db.SQL())
	dep, err := deploymentRecord(q.DeploymentByID(ctx, deploymentID))
	if err != nil {
		return out, err
	}
	if dep.ProjectID != projectID {
		return out, fault.New(fault.Invalid, "choose a deployment in this project")
	}
	target, err := targetRecord(q.TargetByID(ctx, dep.TargetID))
	if err != nil {
		return out, err
	}
	if err := addressTarget(target); err != nil {
		return out, err
	}
	if err := target.Status.Available(); err != nil {
		return out, err
	}
	row, err := q.DeploymentAddressSettings(ctx, dep.ID)
	if err != nil {
		return out, err
	}
	if expected != nil && (expected.DeploymentID != dep.ID || expected.TargetID != target.ID || expected.Revision != row.Revision || expected.TargetRevision != target.SettingsRevision) {
		return out, fault.New(fault.Conflict, "inspect and confirm the current address evidence")
	}
	name, _ := dep.Handle["stack"].(string)
	if name == "" {
		return out, fault.New(fault.Conflict, "this deployment has no recorded serving stack; resolve its retained resources before replacing its address inventory")
	}
	cfg, err := s.providers.PortainerConfig(ctx, target)
	if err != nil {
		return out, err
	}
	if expected != nil && expected.Scope != *cfg.Scope {
		return out, fault.New(fault.Conflict, "provider scope changed; inspect again")
	}
	stacks, err := portainer.ListStacks(ctx, cfg)
	if err != nil {
		return out, err
	}
	var stack portainer.Stack
	for _, candidate := range stacks {
		if candidate.Name != name || candidate.EndpointID != cfg.Scope.EndpointID {
			continue
		}
		if stack.ID != 0 {
			return out, fault.New(fault.Conflict, "recorded serving stack is ambiguous")
		}
		stack = candidate
	}
	if stack.ID == 0 {
		return out, fault.New(fault.Conflict, "recorded serving stack is unavailable; its old hostnames remain reserved")
	}
	saved, err := portainer.SavedAssociations(dep.Handle)
	if err != nil {
		return out, err
	}
	var legacy *portainer.StackAssociation
	for i := range saved {
		if saved[i].StackID == stack.ID {
			legacy = &saved[i]
			break
		}
	}
	ingress, err := portainer.ReadStackIngress(ctx, *cfg, stack, dep.ID, legacy)
	if err != nil {
		return out, err
	}
	out = AddressObservation{AddressSnapshot: AddressSnapshot{DeploymentID: dep.ID, TargetID: target.ID, Revision: row.Revision, Ingress: ingress}, Scope: *cfg.Scope, Stack: portainer.StackAssociation{DeploymentID: dep.ID, Scope: *cfg.Scope, StackID: stack.ID, StackName: stack.Name}, TargetRevision: target.SettingsRevision}
	if expected == nil {
		return out, nil
	}
	if !out.AddressSnapshot.same(expected.AddressSnapshot) || out.Stack != expected.Stack {
		return out, fault.New(fault.Conflict, "provider address evidence changed; inspect and confirm again")
	}
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		if _, err := releasesql.New(tx.SQL()).LockReleaseProject(ctx, projectID); err != nil {
			return err
		}
		if err := project.RefuseUnlessActiveIn(ctx, tx, projectID); err != nil {
			return err
		}
		tq := deploymentsql.New(tx.SQL())
		currentTarget, err := targetRecord(tq.TargetByNameForUpdate(ctx, target.Name))
		if err != nil {
			return err
		}
		if currentTarget.ID != target.ID || currentTarget.SettingsRevision != target.SettingsRevision || currentTarget.ConnectionID != target.ConnectionID || !jsonEqual(currentTarget.ProviderScope, target.ProviderScope) {
			return ErrTargetSettingsMoved
		}
		current, err := LockDeploymentIn(ctx, tx, dep.ID)
		if err != nil {
			return err
		}
		if current.TargetID != dep.TargetID || current.DesiredGeneration != dep.DesiredGeneration || !reflect.DeepEqual(current.Handle, dep.Handle) {
			return ErrPlacementChanged
		}
		locked, err := tq.DeploymentAddressSettingsForUpdate(ctx, dep.ID)
		if err != nil {
			return err
		}
		if locked.Revision != row.Revision {
			return fault.New(fault.Conflict, "deployment addresses changed; inspect again")
		}
		if locked.VerificationState == "ready" {
			existing, err := addressIngress(locked)
			if err != nil {
				return err
			}
			if !existing.Same(ingress) {
				return fault.New(fault.Conflict, "confirmed addresses differ from desired settings; reconcile the deployment before confirming again")
			}
			return nil
		}
		if locked.VerificationState != "legacy-unverified" {
			return errors.New("unknown deployment address state")
		}
		custom := ""
		if len(ingress.Hosts) > 1 {
			custom = ingress.Hosts[1]
		}
		if err := reserveAddressHosts(ctx, tq, dep.ID, ingress, custom, true); err != nil {
			return err
		}
		updated, err := tq.UpdateDeploymentAddressSettings(ctx, deploymentsql.UpdateDeploymentAddressSettingsParams{DeploymentID: dep.ID, ExpectedRevision: row.Revision, GeneratedHostname: ingress.Hosts[0], CustomHostname: custom, CertificateDomain: ingress.CertificateDomain})
		if err != nil {
			return err
		}
		out.Revision = updated.Revision
		return audit.At(s.now()).RecordIn(ctx, tx, actor.Principal.Name, projectID, "deployment.address-confirmed", dep.Name, out)
	})
	return out, err
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}
