package awsecs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/trust"
)

func (d *Driver) Controls() deploy.Controls  { return deploy.Controls{Liveness: true, Rollout: true} }
func text(h map[string]any, k string) string { v, _ := h[k].(string); return v }
func (d *Driver) register(ctx context.Context, a *API, in ecs.RegisterTaskDefinitionInput) (string, error) {
	out, err := a.ECS.RegisterTaskDefinition(ctx, &in)
	if err != nil {
		return "", err
	}
	if out.TaskDefinition == nil || aws.ToString(out.TaskDefinition.TaskDefinitionArn) == "" {
		return "", errors.New("AWS returned no task definition")
	}
	arn := aws.ToString(out.TaskDefinition.TaskDefinitionArn)
	return arn, d.record(ctx, deploy.Owned{Kind: "task-definition", ID: arn, Name: aws.ToString(in.Family)})
}
func (d *Driver) Reconcile(ctx context.Context, p deploy.Placement, g int64, art deploy.Artifact, key string, log deploy.Logf, checkpoint deploy.Checkpoint) (deploy.Observation, error) {
	var pl releasePlan
	if art.Kind != "aws-ecs" {
		return deploy.Observation{}, errors.New("AWS needs an aws-ecs artifact")
	}
	if err := json.Unmarshal(art.Body, &pl); err != nil {
		return deploy.Observation{}, err
	}
	if pl.Generation != g {
		return deploy.Observation{}, errors.New("AWS artifact generation differs from the admitted generation")
	}
	a, c, _, err := d.Client(ctx)
	if err != nil {
		return deploy.Observation{}, err
	}
	definition, err := d.register(ctx, a, pl.Definition)
	if err != nil {
		return deploy.Observation{}, err
	}
	noted := map[string]any{"service": d.name(p), "definition": definition, "generation": g, "previous": text(p.Handle, "definition"), "target": pl.Target, "rule": pl.Rule, "url": pl.URL, "release": pl.Release, "ready": pl.Ready, "static": pl.Static, "artifactHash": art.Hash}
	if err = checkpoint(noted); err != nil {
		return deploy.Observation{}, err
	}
	for i, in := range pl.Init {
		def, err := d.register(ctx, a, in)
		if err != nil {
			return deploy.Observation{}, err
		}
		run, err := a.ECS.RunTask(ctx, &ecs.RunTaskInput{Cluster: aws.String(c.Cluster), TaskDefinition: aws.String(def), ClientToken: aws.String(deploy.ConfigHash(key + "/init/" + strconv.Itoa(i))), Count: aws.Int32(1), CapacityProviderStrategy: fargate(), NetworkConfiguration: pl.Network, Tags: d.tags(p)})
		if err != nil {
			return deploy.Observation{}, err
		}
		if len(run.Tasks) != 1 || len(run.Failures) > 0 {
			return deploy.Observation{}, errors.New("AWS could not start the init task")
		}
		task := aws.ToString(run.Tasks[0].TaskArn)
		if err = d.record(ctx, deploy.Owned{Kind: "ecs-task", ID: task, Name: aws.ToString(in.Family)}); err != nil {
			return deploy.Observation{}, err
		}
		wait, cancel := context.WithTimeout(ctx, d.JobTimeout)
		result, err := d.waitJob(wait, a, c.Cluster, task)
		cancel()
		if err != nil {
			return deploy.Observation{}, err
		}
		if err = jobExit(result); err != nil {
			return deploy.Observation{}, fmt.Errorf("init %s: %w", aws.ToString(in.Family), err)
		}
	}
	service, ok, err := d.recorded(ctx, "ecs-service")
	if err != nil {
		return deploy.Observation{}, err
	}
	if ok {
		ok, err = d.checkOwned(ctx, a, c, p, service)
		if err != nil {
			return deploy.Observation{}, err
		}
	}
	desired := int32(1)
	if p.Stopped {
		desired = 0
	}
	var deployments []ecstypes.Deployment
	if !ok {
		in := &ecs.CreateServiceInput{Cluster: aws.String(c.Cluster), ServiceName: aws.String(d.name(p)), TaskDefinition: aws.String(definition), ClientToken: aws.String(deploy.ConfigHash(key)), DesiredCount: aws.Int32(desired), CapacityProviderStrategy: fargate(), NetworkConfiguration: pl.Network, Tags: d.tags(p), PropagateTags: ecstypes.PropagateTagsService, DeploymentConfiguration: &ecstypes.DeploymentConfiguration{MinimumHealthyPercent: aws.Int32(100), MaximumPercent: aws.Int32(200), DeploymentCircuitBreaker: &ecstypes.DeploymentCircuitBreaker{Enable: true, Rollback: false}}}
		if pl.Target != "" {
			in.LoadBalancers = []ecstypes.LoadBalancer{{TargetGroupArn: aws.String(pl.Target), ContainerName: aws.String("app"), ContainerPort: pl.Definition.ContainerDefinitions[0].PortMappings[0].ContainerPort}}
		}
		out, re := a.ECS.CreateService(ctx, in)
		if re != nil {
			return deploy.Observation{}, re
		}
		if out.Service == nil {
			return deploy.Observation{}, errors.New("AWS returned no service")
		}
		service = deploy.Owned{Kind: "ecs-service", ID: aws.ToString(out.Service.ServiceArn), Name: d.name(p)}
		if err = d.record(ctx, service); err != nil {
			return deploy.Observation{}, err
		}
		deployments = out.Service.Deployments
	} else {
		out, re := a.ECS.UpdateService(ctx, &ecs.UpdateServiceInput{Cluster: aws.String(c.Cluster), Service: aws.String(service.ID), TaskDefinition: aws.String(definition), DesiredCount: aws.Int32(desired)})
		if re != nil {
			return deploy.Observation{}, re
		}
		if out.Service == nil {
			return deploy.Observation{}, errors.New("AWS returned no updated service")
		}
		deployments = out.Service.Deployments
	}
	noted["service"] = service.ID
	for _, dep := range deployments {
		if aws.ToString(dep.TaskDefinition) == definition && aws.ToString(dep.Status) == "PRIMARY" {
			noted["deployment"] = aws.ToString(dep.Id)
		}
	}
	if err = checkpoint(noted); err != nil {
		return deploy.Observation{}, err
	}
	return d.finish(ctx, p, g, noted, log)
}
func fargate() []ecstypes.CapacityProviderStrategyItem {
	return []ecstypes.CapacityProviderStrategyItem{{CapacityProvider: aws.String("FARGATE"), Weight: 1}}
}
func jobExit(t ecstypes.Task) error {
	if t.StopCode == ecstypes.TaskStopCodeTaskFailedToStart || len(t.Containers) != 1 || t.Containers[0].ExitCode == nil || aws.ToInt32(t.Containers[0].ExitCode) != 0 {
		return fmt.Errorf("task did not exit successfully (%s): %s; read its task log stream", t.StopCode, aws.ToString(t.StoppedReason))
	}
	return nil
}
func (d *Driver) Resume(ctx context.Context, p deploy.Placement, g int64, noted map[string]any, log deploy.Logf) (deploy.Observation, error) {
	if text(noted, "definition") == "" {
		return deploy.Observation{}, errors.New("AWS attempt has no checkpointed task definition; its outcome is unknown")
	}
	return d.finish(ctx, p, g, noted, log)
}
func (d *Driver) finish(ctx context.Context, p deploy.Placement, g int64, h map[string]any, log deploy.Logf) (deploy.Observation, error) {
	next := p
	next.Handle = h
	next.Generation = g
	next.URL = text(h, "url")
	out, err := d.converge(ctx, next)
	if err == nil && !p.Stopped {
		err = d.witness(ctx, h)
	}
	if err == nil {
		// Generation g serves and was witnessed. Pruning is cleanup: what it
		// could not delete stays on the owned list for take-down, and must
		// not turn a serving release into a failed one without a rollback.
		if err := d.pruneParameters(ctx, p, g); err != nil {
			log("older generations' parameters were kept for take-down: %v", err)
		}
		out.ArtifactHash = text(h, "artifactHash")
		out.HealthObservedAt = d.Now().UTC()
		return out, nil
	}
	failure := err
	// The admitted old revision, never the desired revision, is the rollback target.
	a, c, _, err := d.Client(ctx)
	if err != nil {
		return deploy.Observation{}, errors.Join(failure, err)
	}
	service, ok, err := d.recorded(ctx, "ecs-service")
	if err != nil || !ok {
		return deploy.Observation{}, errors.Join(failure, err)
	}
	if _, err = d.checkOwned(ctx, a, c, p, service); err != nil {
		return deploy.Observation{}, errors.Join(failure, err)
	}
	old := text(h, "previous")
	count := int32(1)
	if old == "" || p.Stopped {
		count = 0
	}
	in := &ecs.UpdateServiceInput{Cluster: aws.String(c.Cluster), Service: aws.String(service.ID), DesiredCount: aws.Int32(count)}
	if old != "" {
		in.TaskDefinition = aws.String(old)
	}
	if _, err = a.ECS.UpdateService(ctx, in); err != nil {
		return deploy.Observation{}, errors.Join(failure, fmt.Errorf("rollback: %w", err))
	}
	previous := next
	previous.Stopped = count == 0
	previous.Handle = map[string]any{"service": service.ID, "definition": old, "target": text(h, "target")}
	previous.Generation = p.Generation
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.JobTimeout)
	defer cancel()
	if _, err = d.converge(recovery, previous); err != nil {
		return deploy.Observation{}, errors.Join(failure, fmt.Errorf("rollback did not converge; no generation adopted: %w", err))
	}
	if old == "" {
		return deploy.Observation{}, fmt.Errorf("%w; nothing is serving", failure)
	}
	return deploy.Observation{}, fmt.Errorf("%w; the previous release is serving", failure)
}
func (d *Driver) converge(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
	ctx, cancel := context.WithTimeout(ctx, d.JobTimeout)
	defer cancel()
	for {
		out, err := d.Observe(ctx, p)
		if err != nil {
			return out, err
		}
		if out.Rollout == "failed" {
			return out, fmt.Errorf("AWS rollout failed: %v", out.Handle)
		}
		if (p.Stopped && out.Rollout == "stopped") || (!p.Stopped && out.Rollout == "active" && out.Health == "healthy") {
			return out, nil
		}
		timer := time.NewTimer(d.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return out, ctx.Err()
		case <-timer.C:
		}
	}
}
func (d *Driver) witness(ctx context.Context, h map[string]any) error {
	base := text(h, "url")
	if base == "" {
		return nil
	}
	path := text(h, "ready")
	static, _ := h["static"].(bool)
	if static {
		path = "/_apptown/release"
	}
	if path == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", base+path, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: trust.Transport(trust.AppTown)}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 400 {
		return fmt.Errorf("AWS front door answered %d", res.StatusCode)
	}
	if static {
		b, err := io.ReadAll(io.LimitReader(res.Body, 4096))
		if err != nil {
			return err
		}
		if !strings.Contains(string(b), text(h, "release")) {
			return errors.New("AWS front door does not serve the admitted release")
		}
	}
	return nil
}
func (d *Driver) Observe(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
	o := deploy.Observation{Health: "unknown", Rollout: "absent", URL: p.URL, Handle: p.Handle}
	a, c, _, err := d.Client(ctx)
	if err != nil {
		return o, err
	}
	name := text(p.Handle, "service")
	if name == "" {
		record, ok, err := d.recorded(ctx, "ecs-service")
		if err != nil {
			return o, err
		}
		if !ok {
			return o, nil
		}
		name = record.ID
	}
	out, err := a.ECS.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(c.Cluster), Services: []string{name}, Include: []ecstypes.ServiceField{ecstypes.ServiceFieldTags}})
	if err != nil {
		return o, err
	}
	if len(out.Services) == 0 {
		if len(out.Failures) == 1 && aws.ToString(out.Failures[0].Reason) == "MISSING" {
			return o, nil
		}
		return o, errors.New("AWS service could not be read")
	}
	service := out.Services[0]
	if strings.HasPrefix(name, "arn:") && aws.ToString(service.ServiceArn) != name {
		return o, errors.New("AWS returned another service")
	}
	if aws.ToString(service.Status) == "INACTIVE" {
		return o, nil
	}
	tags := map[string]string{}
	for _, t := range service.Tags {
		tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	if tags["apptown:instance"] != d.Installation || tags["apptown:deployment"] != p.ID.String() {
		return o, errors.New("AWS service no longer carries this deployment's tags")
	}
	var tasks []ecstypes.Task
	var token *string
	for {
		ls, err := a.ECS.ListTasks(ctx, &ecs.ListTasksInput{Cluster: aws.String(c.Cluster), ServiceName: service.ServiceName, DesiredStatus: ecstypes.DesiredStatusRunning, NextToken: token})
		if err != nil {
			return o, err
		}
		for i := 0; i < len(ls.TaskArns); i += 100 {
			ts, err := a.ECS.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: aws.String(c.Cluster), Tasks: ls.TaskArns[i:min(i+100, len(ls.TaskArns))]})
			if err != nil {
				return o, err
			}
			if len(ts.Failures) > 0 {
				return o, errors.New("AWS could not read every running task")
			}
			tasks = append(tasks, ts.Tasks...)
		}
		token = ls.NextToken
		if aws.ToString(token) == "" {
			break
		}
	}
	if service.DesiredCount == 0 {
		if p.Stopped && len(tasks) == 0 {
			o.Rollout = "stopped"
		} else {
			o.Rollout = "failed"
		}
		return o, nil
	}
	o.Rollout = "rolling"
	definition := text(p.Handle, "definition")
	same := len(tasks) > 0
	for _, t := range tasks {
		if aws.ToString(t.TaskDefinitionArn) != definition || aws.ToString(t.LastStatus) != "RUNNING" {
			same = false
		}
	}
	var primary *ecstypes.Deployment
	for i := range service.Deployments {
		if aws.ToString(service.Deployments[i].Status) == "PRIMARY" {
			primary = &service.Deployments[i]
		}
	}
	if primary == nil {
		return o, errors.New("AWS service has no primary deployment")
	}
	if primary.RolloutState == ecstypes.DeploymentRolloutStateFailed {
		o.Rollout = "failed"
		return o, nil
	}
	if primary.RolloutState != ecstypes.DeploymentRolloutStateCompleted {
		return o, nil
	}
	if !same || len(tasks) != int(service.DesiredCount) {
		o.Rollout = "failed"
		return o, nil
	}
	o.Rollout = "active"
	o.Health = "healthy"
	o.Generation = p.Generation
	o.Running = definition
	o.RuntimeToken = aws.ToString(primary.Id)
	target := text(p.Handle, "target")
	if target != "" {
		hs, err := a.ELB.DescribeTargetHealth(ctx, &elasticloadbalancingv2.DescribeTargetHealthInput{TargetGroupArn: aws.String(target)})
		if err != nil {
			return o, err
		}
		if len(hs.TargetHealthDescriptions) == 0 {
			o.Health = "unknown"
		}
		for _, t := range hs.TargetHealthDescriptions {
			if t.TargetHealth == nil {
				o.Health = "unknown"
				break
			}
			switch t.TargetHealth.State {
			case elbtypes.TargetHealthStateEnumHealthy:
			case elbtypes.TargetHealthStateEnumUnhealthy:
				o.Health = "unhealthy"
			default:
				o.Health = "unknown"
			}
		}
	}
	return o, nil
}
func (d *Driver) Runtime(ctx context.Context, p deploy.Placement, action string) error {
	a, c, _, err := d.Client(ctx)
	if err != nil {
		return err
	}
	o, ok, err := d.recorded(ctx, "ecs-service")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("AWS service is not recorded")
	}
	live, err := d.checkOwned(ctx, a, c, p, o)
	if err != nil {
		return err
	}
	if !live {
		return errors.New("AWS service is absent")
	}
	count := int32(1)
	if action == "stop" {
		count = 0
	} else if action != "start" && action != "restart" {
		return fmt.Errorf("unknown runtime action %s", action)
	}
	_, err = a.ECS.UpdateService(ctx, &ecs.UpdateServiceInput{Cluster: aws.String(c.Cluster), Service: aws.String(o.ID), DesiredCount: aws.Int32(count), ForceNewDeployment: action == "restart"})
	return err
}
func (d *Driver) ObserveRuntime(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
	return d.Observe(ctx, p)
}
func (d *Driver) RecordedRunning(p deploy.Placement, _ string) string {
	return text(p.Handle, "definition")
}
func (d *Driver) AdoptRunning(running string) map[string]any {
	if running == "" {
		return nil
	}
	return map[string]any{"definition": running}
}

// StopWriters follows the driver-run copy contract used by Fly. A deployment
// whose first release failed before its service was made (an init step that
// exits non-zero) still holds its database: nothing writes, so there is
// nothing to stop, and its take-down and copies go on.
func (d *Driver) StopWriters(ctx context.Context, p deploy.Placement, stopping func(string) error, started func(string)) (func(context.Context) []string, int, error) {
	a, c, _, err := d.Client(ctx)
	if err != nil {
		return nil, 0, err
	}
	service, ok, err := d.recorded(ctx, "ecs-service")
	if err == nil && ok {
		ok, err = d.checkOwned(ctx, a, c, p, service)
	}
	if err != nil {
		return nil, 0, err
	}
	if !ok {
		return func(context.Context) []string { return nil }, 0, nil
	}
	if stopping != nil {
		if err := stopping(d.name(p)); err != nil {
			return nil, 0, err
		}
	}
	resume := func(ctx context.Context) []string {
		if p.Stopped {
			return nil
		}
		if err := d.Runtime(ctx, p, "start"); err != nil {
			return []string{err.Error()}
		}
		if _, err := d.converge(ctx, p); err != nil {
			return []string{err.Error()}
		}
		if started != nil {
			started(d.name(p))
		}
		return nil
	}
	if err := d.Runtime(ctx, p, "stop"); err != nil {
		return resume, 0, err
	}
	stopped := p
	stopped.Stopped = true
	if _, err := d.converge(ctx, stopped); err != nil {
		return resume, 0, err
	}
	return resume, 1, nil
}
