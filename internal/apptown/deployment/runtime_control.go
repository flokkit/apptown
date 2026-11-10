package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

var errRuntimeBusy = fault.New(fault.Conflict, "the deployment is changing; wait for it to finish, or press Check again if it was interrupted")

func (e *Deployment) runtimeBusy() bool {
	return e.RuntimeState != "running" && e.RuntimeState != "stopped"
}

// runtimeControls says, per action (deploy, start, stop, restart), why it is
// refused; an empty reason means it is offered. This is the target contract,
// independent of credentials and observations; the listing adds the refusals
// admission would make now (a release in flight, a blocked promotion).
func runtimeControls(t *Target, d *Deployment) map[string]string {
	controls := map[string]string{"deploy": "", "start": "This target cannot control the app's containers", "stop": "This target cannot control the app's containers", "restart": "This target cannot control the app's containers"}
	if t != nil {
		switch t.Kind {
		case KindDockerHost, KindPortainer:
			controls = map[string]string{"deploy": "", "start": "", "stop": "", "restart": ""}
		case KindDOApp:
			controls = map[string]string{"deploy": "", "start": "App Platform cannot keep an app stopped", "stop": "App Platform cannot keep an app stopped", "restart": ""}
		case KindS3Bucket:
			for _, action := range []string{"start", "stop", "restart"} {
				controls[action] = "Bucket websites cannot be started, stopped or restarted"
			}
		}
	}
	// A failed replacement may have left application parts from different
	// releases. Runtime control must not start that partial replacement.
	if d.Readiness == ReadinessFailed {
		for _, action := range []string{"start", "restart"} {
			if controls[action] == "" {
				controls[action] = "The latest deploy failed. Deploy a release successfully before starting or restarting."
			}
		}
	}
	return controls
}

// refuseControl records why each action is refused, keeping a reason already
// given: the first refusal found is the one a person must resolve first.
func refuseControl(controls map[string]string, reason string, actions ...string) {
	for _, action := range actions {
		if controls[action] == "" {
			controls[action] = reason
		}
	}
}

// ControlRuntime checks operate authority before recording intent or contacting
// the provider. The project/deployment locks serialize admission with releases
// and take-downs. Provider I/O runs outside transactions. Interrupted effects
// retain their transition and error until CheckAgain observes their outcome.
func (s *Service) ControlRuntime(ctx context.Context, projectID ids.ID, name, action string) (*Deployment, error) {
	transition := map[string]string{"start": "starting", "stop": "stopping", "restart": "restarting"}[action]
	if transition == "" {
		return nil, fault.New(fault.Invalid, "choose start, stop or restart")
	}
	if s.authority == nil {
		return nil, access.ErrAuthorityUnavailable
	}
	permission := access.Permission{Capability: access.CapDeploymentOperate, Resource: access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}, Context: access.Context{Route: "deployment." + action, Consequential: true}}
	actor, err := s.authority.Require(ctx, permission)
	if err != nil {
		return nil, err
	}
	if actor.Principal.Name == "" {
		return nil, errors.New("runtime caller required")
	}
	selected, err := s.Deployments.ByName(ctx, projectID, name)
	if err != nil {
		return nil, database.Missing(err)
	}
	if _, busy := s.runtimeActs.LoadOrStore(selected.ID, struct{}{}); busy {
		return nil, errRuntimeBusy
	}
	defer s.runtimeActs.Delete(selected.ID)
	var dep *Deployment
	var target *Target
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		if err := LockProjectIn(ctx, tx, projectID); err != nil {
			return err
		}
		if err := project.RefuseUnlessActiveIn(ctx, tx, projectID); err != nil {
			return err
		}
		row, err := deploymentsql.New(tx.SQL()).DeploymentByName(ctx, deploymentsql.DeploymentByNameParams{ProjectID: projectID, Name: name})
		if err != nil {
			return database.Missing(err)
		}
		if row.Deployment.ID != selected.ID {
			return fault.New(fault.Conflict, "the deployment changed; reload before acting")
		}
		dep, err = lockDeploymentRecordIn(ctx, tx, row.Deployment.ID)
		if err != nil {
			return err
		}
		if !dep.Standing() {
			return fault.New(fault.Unprocessable, "only a standing deployment has runtime controls")
		}
		if err := dep.HeldAfterRestore(); err != nil {
			return err
		}
		target, err = s.Targets.ByID(ctx, dep.TargetID)
		if err != nil {
			return err
		}
		if reason := runtimeControls(target, dep)[action]; reason != "" {
			return fault.New(fault.Unprocessable, reason)
		}
		retry := dep.RuntimeState == transition && dep.RuntimeError != ""
		if (dep.runtimeBusy() && !retry) || (dep.Readiness == reconciling || dep.Readiness == ReadinessUncertain) {
			return errRuntimeBusy
		}
		if dep.ObservedGeneration == 0 {
			return fault.New(fault.Conflict, "deploy a release before using runtime controls")
		}
		if action == "start" && dep.RuntimeState != "stopped" && !retry {
			return fault.New(fault.Conflict, "the deployment is already running")
		}
		if action != "start" && dep.RuntimeState == "stopped" {
			return fault.New(fault.Conflict, "the deployment is stopped; start it first")
		}
		if err := takeDownHeldIn(ctx, tx, projectID, dep.ID); err != nil {
			return err
		}
		busy, err := releasesql.New(tx.SQL()).ReleaseInFlight(ctx, releasesql.ReleaseInFlightParams{ProjectID: projectID, DeploymentID: dep.ID, OpenState: Releasing.String()})
		if err != nil {
			return err
		}
		if busy {
			return errReleaseInFlight
		}
		_, err = deploymentsql.New(tx.SQL()).SetRuntimeState(ctx, deploymentsql.SetRuntimeStateParams{ID: dep.ID, State: transition, Actor: actor.Principal.Name, At: sql.NullTime{Time: s.now(), Valid: true}})
		if err != nil {
			return err
		}
		return audit.At(s.now()).RecordIn(ctx, tx, actor.Principal.Name, projectID, "deployment."+action+"-requested", name, map[string]any{"deploymentId": dep.ID, "generation": dep.ObservedGeneration, "principal": actor.Principal})
	})
	if err != nil {
		return nil, err
	}
	driver, err := s.providers.Driver(ctx, target, dep)
	if err == nil {
		controller, ok := driver.(deploy.RuntimeController)
		if !ok {
			err = errors.New("the target has no runtime controller")
		} else {
			// Resolve current authority again after credential and provider setup.
			if _, err = s.authority.Require(ctx, permission); err == nil {
				if err = NewHostSnapshots(s.db, s.now).BeforeAct(ctx, dep.TargetID); err == nil {
					err = controller.Runtime(ctx, dep.Placement(), action)
				}
			}
		}
	}
	state := "running"
	if action == "stop" {
		state = "stopped"
	}
	// Even cancellation must retain the result of an attempted provider effect.
	recordCtx := context.WithoutCancel(ctx)
	settleErr := s.db.InTx(recordCtx, func(tx *database.Tx) error {
		current, readErr := lockDeploymentRecordIn(recordCtx, tx, dep.ID)
		if readErr != nil {
			return readErr
		}
		if current.RuntimeState != transition {
			return errRuntimeBusy
		}
		detail := ""
		kind := "deployment." + action + "-completed"
		if err != nil {
			state = transition
			detail = err.Error()
			kind = "deployment." + action + "-unconfirmed"
		}
		_, writeErr := deploymentsql.New(tx.SQL()).SetRuntimeState(recordCtx, deploymentsql.SetRuntimeStateParams{ID: dep.ID, State: state, Actor: actor.Principal.Name, At: current.RuntimeAt.NullTime, Error: detail})
		if writeErr != nil {
			return writeErr
		}
		return audit.At(s.now()).RecordIn(recordCtx, tx, actor.Principal.Name, projectID, kind, name, map[string]any{"deploymentId": dep.ID, "generation": dep.ObservedGeneration, "error": detail})
	})
	s.InfrastructureChanged(dep.TargetID)
	if err != nil || settleErr != nil {
		return nil, errors.Join(err, settleErr)
	}
	return s.Deployments.ByID(recordCtx, dep.ID)
}

// checkRuntimeControl only observes an interrupted act. Restart is confirmed
// here as running, without claiming another stop/start occurred.
func (s *Service) checkRuntimeControl(ctx context.Context, dep *Deployment) (checkErr error) {
	if !dep.runtimeBusy() {
		return nil
	}
	if _, busy := s.runtimeActs.LoadOrStore(dep.ID, struct{}{}); busy {
		return errRuntimeBusy
	}
	defer s.runtimeActs.Delete(dep.ID)
	// A process may have stopped between recording intent and contacting the
	// provider. Retain a failed check so the same action can be retried.
	defer func() {
		if checkErr == nil {
			return
		}
		recordCtx := context.WithoutCancel(ctx)
		recordErr := s.db.InTx(recordCtx, func(tx *database.Tx) error {
			current, err := lockDeploymentRecordIn(recordCtx, tx, dep.ID)
			if err != nil {
				return err
			}
			if current.RuntimeState != dep.RuntimeState || !current.RuntimeAt.Time.Equal(dep.RuntimeAt.Time) {
				return errRuntimeBusy
			}
			_, err = deploymentsql.New(tx.SQL()).SetRuntimeState(recordCtx, deploymentsql.SetRuntimeStateParams{ID: dep.ID, State: dep.RuntimeState, Actor: dep.RuntimeActor, At: dep.RuntimeAt.NullTime, Error: checkErr.Error()})
			return err
		})
		checkErr = errors.Join(checkErr, recordErr)
	}()
	t, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return err
	}
	d, err := s.providers.Driver(ctx, t, dep)
	if err != nil {
		return err
	}
	pl := dep.Placement()
	pl.Stopped = dep.RuntimeState == "stopping"
	controller, ok := d.(deploy.RuntimeController)
	if !ok {
		return errors.New("the target has no runtime controller")
	}
	o, err := controller.ObserveRuntime(ctx, pl)
	if err != nil {
		return err
	}
	state := "running"
	if dep.RuntimeState == "stopping" {
		state = "stopped"
		if o.Rollout != "stopped" {
			return fmt.Errorf("the target has not confirmed the app stopped")
		}
	} else if o.Rollout != "active" {
		return fmt.Errorf("the target has not confirmed the app started")
	}
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		current, err := lockDeploymentRecordIn(ctx, tx, dep.ID)
		if err != nil {
			return err
		}
		if current.RuntimeState != dep.RuntimeState {
			return errRuntimeBusy
		}
		if _, err := deploymentsql.New(tx.SQL()).SetRuntimeState(ctx, deploymentsql.SetRuntimeStateParams{ID: dep.ID, State: state, Actor: dep.RuntimeActor, At: dep.RuntimeAt.NullTime}); err != nil {
			return err
		}
		return audit.At(s.now()).RecordIn(ctx, tx, dep.RuntimeActor, dep.ProjectID, "deployment.runtime-confirmed", dep.Name, map[string]any{"deploymentId": dep.ID, "state": state})
	})
}
