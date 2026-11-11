package awsecs_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/awsecs"
	"github.com/flokkit/apptown/internal/deploy/awsecs/awsfake"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type consumerSecrets map[string]map[string]string

func (s consumerSecrets) EnvFor(name string) map[string]string { return s[name] }

func TestSecretsAreImmutablePerGenerationAndConsumerAndPrunedOnlyAfterConvergence(t *testing.T) {
	p := deploy.Placement{ID: idstest.N(92)}
	owner := &jobOwner{items: []deploy.Owned{{Kind: "security-group", ID: "sg"}, {Kind: "log-group", ID: "log", Name: "/log"}, {Kind: "ecr-repository", ID: "repo", Name: "repo"}, {Kind: "target-group", ID: "target"}, {Kind: "listener-rule", ID: "rule"}}}
	a := &awsfake.Account{}
	tags := map[string]string{"apptown:instance": "installation", "apptown:deployment": p.ID.String()}
	a.DescribeSecurityGroupsFunc = func(context.Context, *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error) {
		return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{{GroupId: aws.String("sg"), Tags: []ec2types.Tag{{Key: aws.String("apptown:instance"), Value: aws.String("installation")}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}}}}, nil
	}
	a.LogsListTagsForResourceFunc = func(context.Context, *cloudwatchlogs.ListTagsForResourceInput) (*cloudwatchlogs.ListTagsForResourceOutput, error) {
		return &cloudwatchlogs.ListTagsForResourceOutput{Tags: tags}, nil
	}
	a.ECRListTagsForResourceFunc = func(context.Context, *ecr.ListTagsForResourceInput) (*ecr.ListTagsForResourceOutput, error) {
		return &ecr.ListTagsForResourceOutput{Tags: []ecrtypes.Tag{{Key: aws.String("apptown:instance"), Value: aws.String("installation")}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}}, nil
	}
	a.DescribeTagsFunc = func(_ context.Context, in *elasticloadbalancingv2.DescribeTagsInput) (*elasticloadbalancingv2.DescribeTagsOutput, error) {
		return &elasticloadbalancingv2.DescribeTagsOutput{TagDescriptions: []elbtypes.TagDescription{{ResourceArn: aws.String(in.ResourceArns[0]), Tags: []elbtypes.Tag{{Key: aws.String("apptown:instance"), Value: aws.String("installation")}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}}}}, nil
	}
	a.PutRetentionPolicyFunc = func(context.Context, *cloudwatchlogs.PutRetentionPolicyInput) (*cloudwatchlogs.PutRetentionPolicyOutput, error) {
		return &cloudwatchlogs.PutRetentionPolicyOutput{}, nil
	}
	parameters := map[string]string{}
	a.GetParametersFunc = func(_ context.Context, in *ssm.GetParametersInput) (*ssm.GetParametersOutput, error) {
		out := &ssm.GetParametersOutput{}
		for _, name := range in.Names {
			if value, ok := parameters[name]; ok {
				out.Parameters = append(out.Parameters, ssmtypes.Parameter{Name: aws.String(name), Value: aws.String(value)})
			} else {
				out.InvalidParameters = append(out.InvalidParameters, name)
			}
		}
		return out, nil
	}
	a.PutParameterFunc = func(_ context.Context, in *ssm.PutParameterInput) (*ssm.PutParameterOutput, error) {
		name := aws.ToString(in.Name)
		if _, ok := parameters[name]; ok || aws.ToBool(in.Overwrite) {
			t.Fatal("overwrote generation secret")
		}
		parameters[name] = aws.ToString(in.Value)
		return &ssm.PutParameterOutput{}, nil
	}
	a.SSMListTagsForResourceFunc = func(context.Context, *ssm.ListTagsForResourceInput) (*ssm.ListTagsForResourceOutput, error) {
		return &ssm.ListTagsForResourceOutput{TagList: []ssmtypes.Tag{{Key: aws.String("apptown:instance"), Value: aws.String("installation")}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}}, nil
	}
	converged := false
	a.DeleteParametersFunc = func(_ context.Context, in *ssm.DeleteParametersInput) (*ssm.DeleteParametersOutput, error) {
		if !converged {
			t.Fatal("pruned before running revision converged")
		}
		for _, name := range in.Names {
			if !strings.Contains(name, "/g1/") {
				t.Fatal("deleted rollback generation")
			}
			delete(parameters, name)
		}
		return &ssm.DeleteParametersOutput{DeletedParameters: in.Names}, nil
	}
	d := awsecs.NewDriver(awsecs.Dependencies{Installation: "installation", Owner: owner, Owned: func(context.Context) ([]deploy.Owned, error) { return owner.items, nil }, Client: func(context.Context) (*awsecs.API, awsecs.Config, awsecs.Estate, error) {
		return a.API(), awsecs.Config{Cluster: "cluster"}, awsecs.Estate{}, nil
	}, CopyImage: func(_ context.Context, _ deploy.Placement, image string) (string, error) { return image, nil }})
	contract, err := appcontract.Parse([]byte("app:\n  worker:\n    image: reg/app:1\n    public: true\ninit:\n  - {name: migrate, image: reg/app:1, command: [migrate]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	image := "reg/app@sha256:" + strings.Repeat("a", 64)
	source := deploy.Source{Contract: contract, Images: map[string]string{"worker": image}, Init: map[string]string{"migrate": image}}
	secrets := consumerSecrets{"worker": {"API_KEY": "part-secret"}, "init-migrate": {"API_KEY": "init-secret"}}
	for generation := int64(1); generation <= 3; generation++ {
		d.Generation = generation
		artifact, err := d.Render(t.Context(), p, source, nil, secrets, func(string, ...any) {})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(artifact.Body), "part-secret") || strings.Contains(string(artifact.Body), "init-secret") {
			t.Fatal("secret in release artifact")
		}
		var plan struct {
			Definition ecs.RegisterTaskDefinitionInput
			Init       []ecs.RegisterTaskDefinitionInput
		}
		if err := json.Unmarshal(artifact.Body, &plan); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			definition ecs.RegisterTaskDefinitionInput
			want       string
		}{{plan.Definition, "part-secret"}, {plan.Init[0], "init-secret"}} {
			refs := tc.definition.ContainerDefinitions[0].Secrets
			if len(refs) != 1 || parameters[aws.ToString(refs[0].ValueFrom)] != tc.want {
				t.Fatalf("wrong consumer value: %+v", refs)
			}
		}
		if _, err := d.Render(t.Context(), p, source, nil, secrets, func(string, ...any) {}); err != nil {
			t.Fatalf("retry: %v", err)
		}
	}
	secrets["init-migrate"]["API_KEY"] = "different"
	if _, err := d.Render(t.Context(), p, source, nil, secrets, func(string, ...any) {}); err == nil {
		t.Fatal("changed immutable generation accepted")
	}
	if len(parameters) != 6 {
		t.Fatalf("pruned before witness: %v", parameters)
	}
	a.DescribeServicesFunc = func(context.Context, *ecs.DescribeServicesInput) (*ecs.DescribeServicesOutput, error) {
		return &ecs.DescribeServicesOutput{Services: []ecstypes.Service{{ServiceArn: aws.String("service"), ServiceName: aws.String("service"), DesiredCount: 1, Tags: []ecstypes.Tag{{Key: aws.String("apptown:instance"), Value: aws.String("installation")}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}, Deployments: []ecstypes.Deployment{{Status: aws.String("PRIMARY"), RolloutState: ecstypes.DeploymentRolloutStateCompleted}}}}}, nil
	}
	a.ListTasksFunc = func(context.Context, *ecs.ListTasksInput) (*ecs.ListTasksOutput, error) {
		return &ecs.ListTasksOutput{TaskArns: []string{"task"}}, nil
	}
	a.DescribeTasksFunc = func(context.Context, *ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
		converged = true
		return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{LastStatus: aws.String("RUNNING"), TaskDefinitionArn: aws.String("revision3")}}}, nil
	}
	if _, err := d.Resume(t.Context(), p, 3, map[string]any{"service": "service", "definition": "revision3"}, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if len(parameters) != 4 {
		t.Fatalf("did not keep exactly n and n-1: %v", parameters)
	}
}
