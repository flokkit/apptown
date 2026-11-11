package awsecs_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/awsecs"
	"github.com/flokkit/apptown/internal/deploy/awsecs/awsfake"
	"github.com/flokkit/apptown/internal/ids"
)

type jobOwner struct{ items []deploy.Owned }

func (o *jobOwner) Created(_ context.Context, v deploy.Owned) error {
	o.items = append(o.items, v)
	return nil
}

// Task definitions may reference credentials only through job-scoped parameters;
// absent/nonzero exit is a failed job even if a PUT object happens to exist.
func TestDataJobsRequireObservedExitAndKeepCredentialsOutOfDefinitions(t *testing.T) {
	for _, state := range []string{"clean exit", "definition delete refused", "failed exit", "missing exit", "out of memory", "never started", "lost launch answer", "foreign network", "oversized value"} {
		t.Run(state, func(t *testing.T) {
			a := &awsfake.Account{}
			p := deploy.Placement{ID: ids.New(time.Now())}
			owner := &jobOwner{}
			cfg := awsecs.Config{Region: "ap-southeast-2", Account: "222222222222", Cluster: "existing", ExecutionRole: "execution-role", TaskSubnets: []string{"subnet-a", "subnet-b"}}
			parameters := map[string]string{}
			copied := false
			a.DescribeSecurityGroupsFunc = func(context.Context, *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error) {
				instance := "installation-one"
				if state == "foreign network" {
					instance = "foreign"
				}
				return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{{GroupId: aws.String("sg-app"), Tags: []ec2types.Tag{{Key: aws.String("apptown:instance"), Value: aws.String(instance)}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}}}}, nil
			}
			a.PutParameterFunc = func(_ context.Context, in *ssm.PutParameterInput) (*ssm.PutParameterOutput, error) {
				if in.Type != "SecureString" || aws.ToBool(in.Overwrite) {
					t.Fatal("job parameter is not an immutable secure value")
				}
				parameters[aws.ToString(in.Name)] = aws.ToString(in.Value)
				return &ssm.PutParameterOutput{}, nil
			}
			a.DeleteParametersFunc = func(_ context.Context, in *ssm.DeleteParametersInput) (*ssm.DeleteParametersOutput, error) {
				for _, n := range in.Names {
					delete(parameters, n)
				}
				return &ssm.DeleteParametersOutput{DeletedParameters: in.Names}, nil
			}
			a.RegisterTaskDefinitionFunc = func(_ context.Context, in *ecs.RegisterTaskDefinitionInput) (*ecs.RegisterTaskDefinitionOutput, error) {
				raw, _ := json.Marshal(in)
				if strings.Contains(string(raw), "password-value") || strings.Contains(string(raw), "https://copy.example") || strings.Contains(string(raw), "pg_dump") {
					t.Fatalf("credential/command in definition: %s", raw)
				}
				if in.TaskRoleArn != nil {
					t.Fatal("job received an AWS task role")
				}
				for _, ref := range in.ContainerDefinitions[0].Secrets {
					if _, ok := parameters[aws.ToString(ref.ValueFrom)]; !ok {
						t.Fatal("job was registered before its parameters")
					}
				}
				return &ecs.RegisterTaskDefinitionOutput{TaskDefinition: &ecstypes.TaskDefinition{TaskDefinitionArn: aws.String("job-definition:1")}}, nil
			}
			a.RunTaskFunc = func(_ context.Context, in *ecs.RunTaskInput) (*ecs.RunTaskOutput, error) {
				if aws.ToString(in.ClientToken) == "" || len(in.CapacityProviderStrategy) != 1 || aws.ToString(in.CapacityProviderStrategy[0].CapacityProvider) != "FARGATE" {
					t.Fatal("job inherited capacity-provider defaults")
				}
				if state == "lost launch answer" {
					return nil, errors.New("answer lost")
				}
				return &ecs.RunTaskOutput{Tasks: []ecstypes.Task{{TaskArn: aws.String("arn:task/job")}}}, nil
			}
			a.DescribeTasksFunc = func(context.Context, *ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
				exit := aws.Int32(0)
				if state == "failed exit" {
					exit = aws.Int32(7)
				}
				if state == "missing exit" {
					exit = nil
				}
				task := ecstypes.Task{TaskArn: aws.String("arn:task/job"), LastStatus: aws.String("STOPPED"), Containers: []ecstypes.Container{{ExitCode: exit}}}
				if state == "out of memory" {
					task.StoppedReason = aws.String("OutOfMemoryError")
				}
				if state == "never started" {
					task.StopCode = ecstypes.TaskStopCodeTaskFailedToStart
					task.StoppedReason = aws.String("CannotPullContainerError")
				}
				return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{task}}, nil
			}
			a.DeregisterTaskDefinitionFunc = func(context.Context, *ecs.DeregisterTaskDefinitionInput) (*ecs.DeregisterTaskDefinitionOutput, error) {
				return &ecs.DeregisterTaskDefinitionOutput{}, nil
			}
			a.DeleteTaskDefinitionsFunc = func(context.Context, *ecs.DeleteTaskDefinitionsInput) (*ecs.DeleteTaskDefinitionsOutput, error) {
				if state == "definition delete refused" {
					return &ecs.DeleteTaskDefinitionsOutput{Failures: []ecstypes.Failure{{Arn: aws.String("job-definition:1"), Reason: aws.String("still in use")}}}, nil
				}
				return &ecs.DeleteTaskDefinitionsOutput{}, nil
			}
			driver := awsecs.NewDriver(awsecs.Dependencies{Installation: "installation-one", Owner: owner, Owned: func(context.Context) ([]deploy.Owned, error) {
				return []deploy.Owned{{Kind: "security-group", ID: "sg-app"}, {Kind: "log-group", ID: "log-arn", Name: "/apptown/log"}}, nil
			}, Client: func(context.Context) (*awsecs.API, awsecs.Config, awsecs.Estate, error) {
				return a.API(), cfg, awsecs.Estate{}, nil
			}, CopyImage: func(context.Context, deploy.Placement, string) (string, error) {
				copied = true
				return "ecr@sha256:abc", nil
			}})
			env := map[string]string{"PGPASSWORD": "password-value"}
			if state == "oversized value" {
				env["PGPASSWORD"] = strings.Repeat("x", 4097)
			}
			err := driver.DataJob(t.Context(), p, "postgres:16-alpine", "pg_dump -Fc", env, "https://copy.example/one", true)
			if state == "clean exit" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("job falsely succeeded")
			}
			if state == "lost launch answer" {
				if len(parameters) == 0 {
					t.Fatal("deleted credentials before knowing whether the task stopped")
				}
				for _, c := range a.Calls {
					if c == "DeregisterTaskDefinition" {
						t.Fatal("deregistered an unknown job")
					}
				}
			} else if len(parameters) != 0 {
				t.Fatal("job credentials survived cleanup")
			}
			if (state == "foreign network" || state == "oversized value") && copied {
				t.Fatal("refusal followed an image-copy effect")
			}
		})
	}
}
