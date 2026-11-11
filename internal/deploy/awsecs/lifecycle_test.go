package awsecs_test

import (
	"context"
	"encoding/json"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	types "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/awsecs"
	"github.com/flokkit/apptown/internal/deploy/awsecs/awsfake"
	"github.com/flokkit/apptown/internal/ids"
	"strings"
	"testing"
	"time"
)

func TestObservationRequiresRunningTaskRevisionAndExplicitStop(t *testing.T) {
	for _, state := range []string{"active", "old task", "rolling", "failed", "stopped", "outside stop", "missing", "unreadable"} {
		t.Run(state, func(t *testing.T) {
			p := deploy.Placement{ID: ids.New(time.Now()), Generation: 2, Stopped: state == "stopped", Handle: map[string]any{"service": "service", "definition": "revision:2"}}
			a := &awsfake.Account{}
			a.DescribeServicesFunc = func(context.Context, *ecs.DescribeServicesInput) (*ecs.DescribeServicesOutput, error) {
				if state == "missing" {
					return &ecs.DescribeServicesOutput{Failures: []types.Failure{{Reason: aws.String("MISSING")}}}, nil
				}
				desired := int32(1)
				if state == "stopped" || state == "outside stop" {
					desired = 0
				}
				rollout := types.DeploymentRolloutStateCompleted
				if state == "rolling" {
					rollout = types.DeploymentRolloutStateInProgress
				}
				if state == "failed" {
					rollout = types.DeploymentRolloutStateFailed
				}
				return &ecs.DescribeServicesOutput{Services: []types.Service{{ServiceArn: aws.String("service"), ServiceName: aws.String("service"), DesiredCount: desired, Tags: []types.Tag{{Key: aws.String("apptown:instance"), Value: aws.String("installation")}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}, Deployments: []types.Deployment{{Status: aws.String("PRIMARY"), RolloutState: rollout, TaskDefinition: aws.String("revision:2")}}}}}, nil
			}
			a.ListTasksFunc = func(context.Context, *ecs.ListTasksInput) (*ecs.ListTasksOutput, error) {
				out := &ecs.ListTasksOutput{}
				if state != "stopped" && state != "outside stop" {
					out.TaskArns = []string{"task"}
				}
				return out, nil
			}
			a.DescribeTasksFunc = func(context.Context, *ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
				if state == "unreadable" {
					return &ecs.DescribeTasksOutput{Failures: []types.Failure{{Reason: aws.String("missing task")}}}, nil
				}
				revision := "revision:2"
				if state == "old task" {
					revision = "revision:1"
				}
				return &ecs.DescribeTasksOutput{Tasks: []types.Task{{TaskArn: aws.String("task"), TaskDefinitionArn: aws.String(revision), LastStatus: aws.String("RUNNING")}}}, nil
			}
			d := awsecs.NewDriver(awsecs.Dependencies{Installation: "installation", Client: func(context.Context) (*awsecs.API, awsecs.Config, awsecs.Estate, error) {
				return a.API(), awsecs.Config{Cluster: "cluster"}, awsecs.Estate{}, nil
			}})
			got, err := d.Observe(t.Context(), p)
			if state == "unreadable" {
				if err == nil {
					t.Fatal("unreadable task accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"active": "active", "old task": "failed", "rolling": "rolling", "failed": "failed", "stopped": "stopped", "outside stop": "failed", "missing": "absent"}[state]
			if got.Rollout != want {
				t.Fatalf("%s, want %s", got.Rollout, want)
			}
			if state != "active" && (got.Running != "" || got.Generation != 0) {
				t.Fatal("adopted a desired revision without running evidence")
			}
		})
	}
}
func TestDatabaseManifestRequiresEveryNonProviderDatabase(t *testing.T) {
	m := awsecs.DatabaseManifest{Instance: "instance", Databases: []string{"rdsadmin", "apptown", "postgres", "unexpected"}, ProviderOwned: []string{awsecs.ProviderOwnedDatabase}, Empty: map[string]bool{"apptown": false, "postgres": true, "unexpected": false}}
	raw, _ := json.Marshal(m)
	got, err := awsecs.ParseDatabaseManifest(raw, "instance")
	if err != nil {
		t.Fatal(err)
	}
	if err = got.Covered(map[string]bool{"apptown": true, "postgres": true}); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatal("unlisted extra database escaped the final set", err)
	}
	if err = got.Covered(map[string]bool{"apptown": true, "postgres": true, "unexpected": true}); err != nil {
		t.Fatal(err)
	}
	m.ProviderOwned = append(m.ProviderOwned, "unexpected")
	raw, _ = json.Marshal(m)
	if _, err = awsecs.ParseDatabaseManifest(raw, "instance"); err == nil {
		t.Fatal("extra provider exclusion accepted")
	}
	m.ProviderOwned = []string{awsecs.ProviderOwnedDatabase}
	delete(m.Empty, "postgres")
	raw, _ = json.Marshal(m)
	if _, err = awsecs.ParseDatabaseManifest(raw, "instance"); err == nil {
		t.Fatal("missing table count accepted")
	}
	m.Databases = []string{"apptown"}
	raw, _ = json.Marshal(m)
	if _, err = awsecs.ParseDatabaseManifest(raw, "instance"); err == nil {
		t.Fatal("unlisted rdsadmin exclusion accepted")
	}
}
