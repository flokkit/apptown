package awsecs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/awsecs"
	"github.com/flokkit/apptown/internal/deploy/awsecs/awsfake"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// The interface fake advances actual running revisions separately from desired
// revisions. Init failures cannot update the service; lost answers resume from
// the checkpoint; rollback cannot adopt a revision while an old task survives.
func TestReleaseWaitsForInitAndRunningRevisionAndResumesLostAnswer(t *testing.T) {
	for _, mode := range []string{"release", "init failed", "lost update", "old task", "first failed", "stopped"} {
		t.Run(mode, func(t *testing.T) {
			p := deploy.Placement{ID: idstest.N(71), Generation: 1, Handle: map[string]any{"service": "service", "definition": "old"}, Stopped: mode == "stopped"}
			owner := &jobOwner{items: []deploy.Owned{{Kind: "ecs-service", ID: "service", Name: "service"}}}
			if mode == "first failed" {
				owner.items = nil
				p.Handle = nil
				p.Generation = 0
			}
			a := &awsfake.Account{}
			revision, desired, sequence := "old", int32(1), 0
			initialized, changed, lost := false, false, false
			tags := []ecstypes.Tag{{Key: aws.String("apptown:instance"), Value: aws.String("installation")}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}
			service := func() *ecstypes.Service {
				state := ecstypes.DeploymentRolloutStateCompleted
				if mode == "first failed" && desired > 0 {
					state = ecstypes.DeploymentRolloutStateFailed
				}
				return &ecstypes.Service{ServiceArn: aws.String("service"), ServiceName: aws.String("service"), Status: aws.String("ACTIVE"), Tags: tags, DesiredCount: desired, Deployments: []ecstypes.Deployment{{Id: aws.String("deployment"), Status: aws.String("PRIMARY"), TaskDefinition: aws.String(revision), RolloutState: state}}}
			}
			a.RegisterTaskDefinitionFunc = func(_ context.Context, in *ecs.RegisterTaskDefinitionInput) (*ecs.RegisterTaskDefinitionOutput, error) {
				sequence++
				return &ecs.RegisterTaskDefinitionOutput{TaskDefinition: &ecstypes.TaskDefinition{TaskDefinitionArn: aws.String(fmt.Sprintf("revision:%d", sequence))}}, nil
			}
			a.RunTaskFunc = func(context.Context, *ecs.RunTaskInput) (*ecs.RunTaskOutput, error) {
				return &ecs.RunTaskOutput{Tasks: []ecstypes.Task{{TaskArn: aws.String("init")}}}, nil
			}
			a.DescribeTasksFunc = func(_ context.Context, in *ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
				if in.Tasks[0] == "init" {
					initialized = true
					exit := int32(0)
					if mode == "init failed" {
						exit = 7
					}
					return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{TaskArn: aws.String("init"), LastStatus: aws.String("STOPPED"), Containers: []ecstypes.Container{{ExitCode: aws.Int32(exit)}}}}}, nil
				}
				got := revision
				if mode == "old task" {
					got = "surviving-task"
				}
				return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{TaskArn: aws.String("running"), LastStatus: aws.String("RUNNING"), TaskDefinitionArn: aws.String(got)}}}, nil
			}
			a.DescribeServicesFunc = func(context.Context, *ecs.DescribeServicesInput) (*ecs.DescribeServicesOutput, error) {
				return &ecs.DescribeServicesOutput{Services: []ecstypes.Service{*service()}}, nil
			}
			a.ListTasksFunc = func(context.Context, *ecs.ListTasksInput) (*ecs.ListTasksOutput, error) {
				out := &ecs.ListTasksOutput{}
				if desired > 0 {
					out.TaskArns = []string{"running"}
				}
				return out, nil
			}
			update := func(def *string, count *int32) (*ecstypes.Service, error) {
				if !initialized {
					t.Fatal("service changed before init exited")
				}
				changed = true
				if def != nil {
					revision = aws.ToString(def)
				}
				desired = aws.ToInt32(count)
				if mode == "lost update" && !lost {
					lost = true
					return nil, errors.New("answer lost")
				}
				return service(), nil
			}
			a.UpdateServiceFunc = func(_ context.Context, in *ecs.UpdateServiceInput) (*ecs.UpdateServiceOutput, error) {
				s, e := update(in.TaskDefinition, in.DesiredCount)
				return &ecs.UpdateServiceOutput{Service: s}, e
			}
			a.CreateServiceFunc = func(_ context.Context, in *ecs.CreateServiceInput) (*ecs.CreateServiceOutput, error) {
				s, e := update(in.TaskDefinition, in.DesiredCount)
				if in.DeploymentConfiguration.DeploymentCircuitBreaker.Rollback {
					t.Fatal("provider rollback enabled")
				}
				return &ecs.CreateServiceOutput{Service: s}, e
			}
			d := awsecs.NewDriver(awsecs.Dependencies{Installation: "installation", Owner: owner, Owned: func(context.Context) ([]deploy.Owned, error) { return owner.items, nil }, Client: func(context.Context) (*awsecs.API, awsecs.Config, awsecs.Estate, error) {
				return a.API(), awsecs.Config{Cluster: "cluster"}, awsecs.Estate{}, nil
			}})
			d.PollInterval = time.Millisecond
			d.JobTimeout = 15 * time.Millisecond
			body, _ := json.Marshal(map[string]any{"Generation": int64(2), "Definition": ecs.RegisterTaskDefinitionInput{Family: aws.String("app")}, "Init": []ecs.RegisterTaskDefinitionInput{{Family: aws.String("init")}}})
			var noted map[string]any
			out, err := d.Reconcile(t.Context(), p, 2, deploy.Artifact{Kind: "aws-ecs", Body: body}, "key", func(string, ...any) {}, func(h map[string]any) error { noted = h; return nil })
			switch mode {
			case "init failed":
				if err == nil || changed {
					t.Fatalf("init failure changed service: %v", err)
				}
			case "old task":
				if err == nil || !strings.Contains(err.Error(), "rollback did not converge") || out.Running != "" {
					t.Fatalf("adopted mixed rollout: %+v %v", out, err)
				}
			case "first failed":
				if err == nil || !strings.Contains(err.Error(), "nothing is serving") || desired != 0 {
					t.Fatalf("first failure: desired %d %v", desired, err)
				}
			case "lost update":
				if err == nil {
					t.Fatal("lost answer accepted")
				}
				out, err = d.Resume(t.Context(), p, 2, noted, func(string, ...any) {})
				if err != nil || out.Running != "revision:1" {
					t.Fatalf("resume: %+v %v", out, err)
				}
			case "stopped":
				if err != nil || desired != 0 || out.Rollout != "stopped" {
					t.Fatalf("stopped release: %+v %v", out, err)
				}
			default:
				if err != nil || out.Generation != 2 || out.Running != "revision:1" {
					t.Fatalf("release: %+v %v", out, err)
				}
			}
		})
	}
}

// Generation 3 converged and serves. A parameter of generation 1 that cannot be
// deleted is left on the owned list; the release still reads as served.
func TestPruneFailureDoesNotFailAServingRelease(t *testing.T) {
	p := deploy.Placement{ID: idstest.N(73), Generation: 2}
	tags := []ecstypes.Tag{{Key: aws.String("apptown:instance"), Value: aws.String("installation")}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}
	a := &awsfake.Account{}
	a.DescribeServicesFunc = func(context.Context, *ecs.DescribeServicesInput) (*ecs.DescribeServicesOutput, error) {
		return &ecs.DescribeServicesOutput{Services: []ecstypes.Service{{ServiceArn: aws.String("service"), ServiceName: aws.String("service"), Status: aws.String("ACTIVE"), DesiredCount: 1, Tags: tags, Deployments: []ecstypes.Deployment{{Id: aws.String("d"), Status: aws.String("PRIMARY"), RolloutState: ecstypes.DeploymentRolloutStateCompleted, TaskDefinition: aws.String("revision:3")}}}}}, nil
	}
	a.ListTasksFunc = func(context.Context, *ecs.ListTasksInput) (*ecs.ListTasksOutput, error) {
		return &ecs.ListTasksOutput{TaskArns: []string{"task"}}, nil
	}
	a.DescribeTasksFunc = func(context.Context, *ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
		return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{TaskArn: aws.String("task"), LastStatus: aws.String("RUNNING"), TaskDefinitionArn: aws.String("revision:3")}}}, nil
	}
	a.SSMListTagsForResourceFunc = func(context.Context, *ssm.ListTagsForResourceInput) (*ssm.ListTagsForResourceOutput, error) {
		return &ssm.ListTagsForResourceOutput{TagList: []ssmtypes.Tag{{Key: aws.String("apptown:instance"), Value: aws.String("installation")}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}}, nil
	}
	pruned := false
	a.DeleteParametersFunc = func(context.Context, *ssm.DeleteParametersInput) (*ssm.DeleteParametersOutput, error) {
		pruned = true
		return nil, errors.New("throttled")
	}
	owner := &jobOwner{}
	d := awsecs.NewDriver(awsecs.Dependencies{Installation: "installation", Owner: owner, Owned: func(context.Context) ([]deploy.Owned, error) { return owner.items, nil }, Client: func(context.Context) (*awsecs.API, awsecs.Config, awsecs.Estate, error) {
		return a.API(), awsecs.Config{Cluster: "cluster"}, awsecs.Estate{}, nil
	}})
	d.PollInterval, d.JobTimeout = time.Millisecond, 50*time.Millisecond
	old := awsecs.ParameterPath(d, p) + "/g1/app/API_KEY"
	owner.items = []deploy.Owned{{Kind: "ecs-service", ID: "service"}, {Kind: "ssm-parameter", ID: "arn" + old, Name: old}}
	var logged []string
	out, err := d.Resume(t.Context(), p, 3, map[string]any{"service": "service", "definition": "revision:3"}, func(f string, args ...any) { logged = append(logged, fmt.Sprintf(f, args...)) })
	if err != nil || out.Generation != 3 || !pruned {
		t.Fatalf("served release read as failed: %+v %v (pruned %v)", out, err, pruned)
	}
	if len(logged) == 0 || !strings.Contains(strings.Join(logged, "\n"), "throttled") {
		t.Fatalf("cleanup failure not said: %v", logged)
	}
}
