package awsecs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/deploy"
)

// Dependencies bind a driver to one deployment's credentials and owned records.
type Dependencies struct {
	Client        func(context.Context) (*API, Config, Estate, error)
	Installation  string
	Generation    int64
	Needs         deploy.NeedRecorder
	PostgresSetup string
	Credentials   func(context.Context) (map[string]string, error)
	Owner         deploy.Owner
	Owned         func(context.Context) ([]deploy.Owned, error)
	// CopyImage copies a catalogue image into the deployment's existing ECR
	// repository and verifies its digest. The repository credential stays here.
	CopyImage func(context.Context, deploy.Placement, string) (string, error)
	// Now is AppTown's clock (clock.Or when unset).
	Now func() time.Time
}

// Driver is the single-part Fargate adapter. Its data jobs use the same owned
// network, repository and log group as the deployment through its final copy.
type Driver struct {
	Dependencies
	PollInterval, JobTimeout time.Duration
}

func NewDriver(deps Dependencies) *Driver {
	deps.Now = clock.Or(deps.Now)
	return &Driver{Dependencies: deps, PollInterval: 5 * time.Second, JobTimeout: 30 * time.Minute}
}

func (d *Driver) name(p deploy.Placement) string {
	return "at-" + installationPart(d.Installation) + "-" + deploy.HostKey(p.ID)
}
func (d *Driver) path(p deploy.Placement) string {
	return "/apptown/" + installationPart(d.Installation) + "/" + deploy.HostKey(p.ID)
}
func (d *Driver) tags(p deploy.Placement) []ecstypes.Tag {
	return []ecstypes.Tag{{Key: aws.String("apptown:instance"), Value: aws.String(d.Installation)}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}
}

// copyScript deliberately matches Fly's file-then-upload sequence. A shell
// pipeline cannot establish the export's exit status and must not replace it.
func copyScript(command string, upload bool) string {
	script := "set -eu\napk add --no-cache curl >/dev/null\n"
	if upload {
		return script + "( " + command + "\n) > /tmp/apptown-copy\ncurl --fail --silent --show-error --upload-file /tmp/apptown-copy \"$APPTOWN_COPY_URL\"\n"
	}
	return script + "curl --fail --silent --show-error --output /tmp/apptown-copy \"$APPTOWN_COPY_URL\"\nprintf '%s  /tmp/apptown-copy\\n' \"$APPTOWN_COPY_SHA256\" | sha256sum -c - >/dev/null\n( " + command + "\n) < /tmp/apptown-copy\n"
}

// DataJob exports to a file before PUT, or verifies a downloaded copy before
// restore. Its one object URL and connection exist only in job-only SSM values.
func (d *Driver) DataJob(ctx context.Context, p deploy.Placement, image, command string, env map[string]string, objectURL string, upload bool) error {
	given := map[string]string{}
	for k, v := range env {
		given[k] = v
	}
	given["APPTOWN_COPY_URL"] = objectURL
	return d.runJob(ctx, p, image, copyScript(command, upload), given)
}

// ListDatabases uploads the set's inventory through the ordinary copy path.
func (d *Driver) ListDatabases(ctx context.Context, p deploy.Placement, image string, env map[string]string, objectURL string) error {
	values := make(map[string]string, len(env)+1)
	for k, v := range env {
		values[k] = v
	}
	values["APPTOWN_PROVIDER_OWNED"] = ProviderOwnedDatabase
	return d.DataJob(ctx, p, image, listingCommand(), values, objectURL, true)
}

// EmptyDatabaseProof uploads a repeated empty check as one item of the set.
func (d *Driver) EmptyDatabaseProof(ctx context.Context, p deploy.Placement, image string, env map[string]string, objectURL string) error {
	return d.DataJob(ctx, p, image, checkEmptyCommand(), env, objectURL, true)
}

// CheckEmptyDatabase observes only a task exit; it creates no result object.
// Restore uses it to refuse if a database recorded empty now contains tables.
func (d *Driver) CheckEmptyDatabase(ctx context.Context, p deploy.Placement, image string, env map[string]string) error {
	return d.runJob(ctx, p, image, checkEmptyCommand(), env)
}

func (d *Driver) runJob(ctx context.Context, p deploy.Placement, image, command string, env map[string]string) (err error) {
	if d.Client == nil || d.Owner == nil || d.Owned == nil || d.CopyImage == nil {
		return errors.New("AWS data job needs its account, image copier and owned records")
	}
	values := map[string]string{}
	for k, v := range env {
		values[k] = v
	}
	values["APPTOWN_JOB_COMMAND"] = command
	for key, value := range values {
		if len(value) > 4096 {
			return fmt.Errorf("job value %s exceeds SSM's 4 KB limit", key)
		}
	}
	owned, err := d.Owned(ctx)
	if err != nil {
		return err
	}
	var group, logGroup string
	for _, o := range owned {
		switch o.Kind {
		case "security-group":
			if !o.Data {
				group = o.ID
			}
		case "log-group":
			logGroup = o.Name
		}
	}
	if group == "" || logGroup == "" {
		return errors.New("AWS data job has no recorded deployment network or log group")
	}
	a, c, e, err := d.Client(ctx)
	if err != nil {
		return err
	}
	// Refuse before any image, parameter or task if the live network is foreign.
	if err := d.checkJobNetwork(ctx, a, p, group); err != nil {
		return err
	}
	image, err = d.CopyImage(ctx, p, image)
	if err != nil {
		return err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	op := hex.EncodeToString(random)
	var params []string
	var definition, task string
	stopped := true
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if !stopped && task == "" {
			err = errors.Join(err, errors.New("AWS data job outcome is unknown; its task definition and parameters were kept"))
			return
		}
		if !stopped && task != "" {
			_, stoperr := a.ECS.StopTask(cleanup, &ecs.StopTaskInput{Cluster: aws.String(c.Cluster), Task: aws.String(task), Reason: aws.String("AppTown data job ended")})
			if stoperr != nil {
				err = errors.Join(err, stoperr)
				return
			}
			_, waiterr := d.waitJob(cleanup, a, c.Cluster, task)
			if waiterr != nil {
				err = errors.Join(err, waiterr)
				return
			}
		}
		// A still-running task needs its parameters on a delayed start. Do not
		// delete them or its definition until STOPPED has been observed.
		if definition != "" {
			_, de := a.ECS.DeregisterTaskDefinition(cleanup, &ecs.DeregisterTaskDefinitionInput{TaskDefinition: aws.String(definition)})
			err = errors.Join(err, de)
			if de == nil {
				deleted, de := a.ECS.DeleteTaskDefinitions(cleanup, &ecs.DeleteTaskDefinitionsInput{TaskDefinitions: []string{definition}})
				err = errors.Join(err, de)
				if de == nil && len(deleted.Failures) > 0 {
					err = errors.Join(err, fmt.Errorf("job definition was not deleted: %v", deleted.Failures))
				}
			}
		}
		for len(params) > 0 {
			n := min(10, len(params))
			out, de := a.SSM.DeleteParameters(cleanup, &ssm.DeleteParametersInput{Names: params[:n]})
			err = errors.Join(err, de)
			if de == nil && len(out.InvalidParameters) > 0 {
				err = errors.Join(err, fmt.Errorf("job parameters could not be deleted: %v", out.InvalidParameters))
			}
			params = params[n:]
		}
	}()
	var secretRefs []ecstypes.Secret
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		name := d.path(p) + "/job/" + op + "/" + key
		_, err = a.SSM.PutParameter(ctx, &ssm.PutParameterInput{Name: aws.String(name), Value: aws.String(values[key]), Type: ssmtypes.ParameterTypeSecureString, Tags: []ssmtypes.Tag{{Key: aws.String("apptown:instance"), Value: aws.String(d.Installation)}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}})
		if err != nil {
			return err
		}
		params = append(params, name)
		if err = d.Owner.Created(context.WithoutCancel(ctx), deploy.Owned{Kind: "ssm-parameter", ID: "arn:aws:ssm:" + c.Region + ":" + c.Account + ":parameter" + name, Name: name}); err != nil {
			return err
		}
		secretRefs = append(secretRefs, ecstypes.Secret{Name: aws.String(key), ValueFrom: aws.String(name)})
	}
	reg, err := a.ECS.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{Family: aws.String(d.name(p) + "-job"), Cpu: aws.String("256"), Memory: aws.String("512"), NetworkMode: ecstypes.NetworkModeAwsvpc, RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate}, ExecutionRoleArn: aws.String(c.ExecutionRole), RuntimePlatform: &ecstypes.RuntimePlatform{CpuArchitecture: ecstypes.CPUArchitectureX8664, OperatingSystemFamily: ecstypes.OSFamilyLinux}, Tags: d.tags(p), ContainerDefinitions: []ecstypes.ContainerDefinition{{Name: aws.String("job"), Image: aws.String(image), Essential: aws.Bool(true), EntryPoint: []string{"/bin/sh", "-c"}, Command: []string{`exec /bin/sh -c "$APPTOWN_JOB_COMMAND"`}, Secrets: secretRefs, LogConfiguration: &ecstypes.LogConfiguration{LogDriver: ecstypes.LogDriverAwslogs, Options: map[string]string{"awslogs-group": logGroup, "awslogs-region": c.Region, "awslogs-stream-prefix": "job"}}}}})
	if err != nil {
		return err
	}
	if reg.TaskDefinition == nil || aws.ToString(reg.TaskDefinition.TaskDefinitionArn) == "" {
		return errors.New("AWS returned no job task definition")
	}
	definition = aws.ToString(reg.TaskDefinition.TaskDefinitionArn)
	if err = d.Owner.Created(context.WithoutCancel(ctx), deploy.Owned{Kind: "task-definition", ID: definition, Name: d.name(p) + "-job"}); err != nil {
		return err
	}
	public := ecstypes.AssignPublicIpDisabled
	if e.PublicIP {
		public = ecstypes.AssignPublicIpEnabled
	}
	stopped = false
	run, err := a.ECS.RunTask(ctx, &ecs.RunTaskInput{Cluster: aws.String(c.Cluster), TaskDefinition: aws.String(definition), ClientToken: aws.String(op), Count: aws.Int32(1), CapacityProviderStrategy: []ecstypes.CapacityProviderStrategyItem{{CapacityProvider: aws.String("FARGATE"), Weight: 1}}, NetworkConfiguration: &ecstypes.NetworkConfiguration{AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: c.TaskSubnets, SecurityGroups: []string{group}, AssignPublicIp: public}}, Tags: d.tags(p)})
	if err != nil {
		return err
	}
	if len(run.Failures) > 0 || len(run.Tasks) != 1 || aws.ToString(run.Tasks[0].TaskArn) == "" {
		return fmt.Errorf("AWS could not start the data job: %v", run.Failures)
	}
	task = aws.ToString(run.Tasks[0].TaskArn)
	stopped = false
	if err = d.Owner.Created(context.WithoutCancel(ctx), deploy.Owned{Kind: "ecs-task", ID: task, Name: "data job"}); err != nil {
		return err
	}
	wait, cancel := context.WithTimeout(ctx, d.JobTimeout)
	defer cancel()
	result, err := d.waitJob(wait, a, c.Cluster, task)
	if err != nil {
		return err
	}
	stopped = true
	if result.StopCode == ecstypes.TaskStopCodeTaskFailedToStart || strings.Contains(aws.ToString(result.StoppedReason), "OutOfMemoryError") || len(result.Containers) != 1 || result.Containers[0].ExitCode == nil || aws.ToInt32(result.Containers[0].ExitCode) != 0 {
		exit := "unknown"
		if len(result.Containers) > 0 && result.Containers[0].ExitCode != nil {
			exit = strconv.Itoa(int(*result.Containers[0].ExitCode))
		}
		return fmt.Errorf("AWS data job exited %s (%s): %s; log stream job/job/%s", exit, result.StopCode, aws.ToString(result.StoppedReason), task[strings.LastIndex(task, "/")+1:])
	}
	return nil
}

func (d *Driver) waitJob(ctx context.Context, a *API, cluster, task string) (ecstypes.Task, error) {
	for {
		got, err := a.ECS.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: aws.String(cluster), Tasks: []string{task}, Include: []ecstypes.TaskField{ecstypes.TaskFieldTags}})
		if err != nil {
			return ecstypes.Task{}, err
		}
		if len(got.Failures) > 0 || len(got.Tasks) != 1 {
			return ecstypes.Task{}, errors.New("AWS could not read the data job's exit")
		}
		if aws.ToString(got.Tasks[0].LastStatus) == "STOPPED" {
			return got.Tasks[0], nil
		}
		timer := time.NewTimer(d.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ecstypes.Task{}, ctx.Err()
		case <-timer.C:
		}
	}
}
