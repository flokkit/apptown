package awsecs_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/awsecs"
	"github.com/flokkit/apptown/internal/deploy/awsecs/awsfake"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type recordedNeed struct{ handle map[string]any }

func (n *recordedNeed) Recorded(context.Context, ids.ID, string) (map[string]any, error) {
	return n.handle, nil
}
func (n *recordedNeed) RecordIntent(_ context.Context, _ ids.ID, _ string, request map[string]any) error {
	n.handle = map[string]any{"requested": request}
	return nil
}
func (n *recordedNeed) RecordIdentity(_ context.Context, _ ids.ID, _ string, request map[string]any, id, name string) error {
	n.handle = map[string]any{"requested": request, "id": id, "name": name}
	return nil
}

func TestRDSRecordsIdentityBeforeAdminJobAndCleansFailedJobCredentials(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "admin failed"}[failed], func(t *testing.T) {
			p := deploy.Placement{ID: idstest.N(91)}
			n := &recordedNeed{}
			owner := &jobOwner{items: []deploy.Owned{{Kind: "security-group", ID: "sg-app"}, {Kind: "log-group", ID: "log", Name: "/log"}, {Kind: "ecr-repository", ID: "repo", Name: "repo"}}}
			a := &awsfake.Account{}
			tags := []ec2types.Tag{{Key: aws.String("apptown:instance"), Value: aws.String("installation")}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}
			a.DescribeSecurityGroupsFunc = func(_ context.Context, in *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error) {
				return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{{GroupId: aws.String(in.GroupIds[0]), Tags: tags}}}, nil
			}
			a.LogsListTagsForResourceFunc = func(context.Context, *cloudwatchlogs.ListTagsForResourceInput) (*cloudwatchlogs.ListTagsForResourceOutput, error) {
				return &cloudwatchlogs.ListTagsForResourceOutput{Tags: map[string]string{"apptown:instance": "installation", "apptown:deployment": p.ID.String()}}, nil
			}
			a.ECRListTagsForResourceFunc = func(context.Context, *ecr.ListTagsForResourceInput) (*ecr.ListTagsForResourceOutput, error) {
				return &ecr.ListTagsForResourceOutput{Tags: []ecrtypes.Tag{{Key: tags[0].Key, Value: tags[0].Value}, {Key: tags[1].Key, Value: tags[1].Value}}}, nil
			}
			a.PutRetentionPolicyFunc = func(context.Context, *cloudwatchlogs.PutRetentionPolicyInput) (*cloudwatchlogs.PutRetentionPolicyOutput, error) {
				return &cloudwatchlogs.PutRetentionPolicyOutput{}, nil
			}
			a.CreateSecurityGroupFunc = func(_ context.Context, in *ec2.CreateSecurityGroupInput) (*ec2.CreateSecurityGroupOutput, error) {
				if len(in.TagSpecifications) != 1 || len(in.TagSpecifications[0].Tags) != 2 {
					t.Fatal("database network untagged")
				}
				return &ec2.CreateSecurityGroupOutput{GroupId: aws.String("sg-db")}, nil
			}
			a.AuthorizeSecurityGroupIngressFunc = func(_ context.Context, in *ec2.AuthorizeSecurityGroupIngressInput) (*ec2.AuthorizeSecurityGroupIngressOutput, error) {
				if aws.ToString(in.GroupId) != "sg-db" || aws.ToString(in.IpPermissions[0].UserIdGroupPairs[0].GroupId) != "sg-app" || len(in.IpPermissions[0].IpRanges) > 0 {
					t.Fatal("database publicly reachable")
				}
				return &ec2.AuthorizeSecurityGroupIngressOutput{}, nil
			}
			a.CreateDBSubnetGroupFunc = func(_ context.Context, in *rds.CreateDBSubnetGroupInput) (*rds.CreateDBSubnetGroupOutput, error) {
				if len(in.SubnetIds) != 2 {
					t.Fatal("wrong database subnets")
				}
				return &rds.CreateDBSubnetGroupOutput{DBSubnetGroup: &rdstypes.DBSubnetGroup{DBSubnetGroupArn: aws.String("subnet-arn")}}, nil
			}
			a.CreateDBInstanceFunc = func(_ context.Context, in *rds.CreateDBInstanceInput) (*rds.CreateDBInstanceOutput, error) {
				if n.handle == nil {
					t.Fatal("created before intent")
				}
				if aws.ToString(in.DBInstanceClass) != "db.t4g.micro" || !aws.ToBool(in.StorageEncrypted) || aws.ToBool(in.PubliclyAccessible) || aws.ToInt32(in.BackupRetentionPeriod) != 1 || aws.ToString(in.MasterUserPassword) != "admin-password" {
					t.Fatal("RDS settings differ from dedicated private instance")
				}
				return &rds.CreateDBInstanceOutput{DBInstance: &rdstypes.DBInstance{DBInstanceArn: aws.String("db-arn"), DBInstanceIdentifier: in.DBInstanceIdentifier, DBInstanceStatus: aws.String("available"), Endpoint: &rdstypes.Endpoint{Address: aws.String("private-db"), Port: aws.Int32(5432)}}}, nil
			}
			parameters := map[string]string{}
			a.PutParameterFunc = func(_ context.Context, in *ssm.PutParameterInput) (*ssm.PutParameterOutput, error) {
				parameters[aws.ToString(in.Name)] = aws.ToString(in.Value)
				return &ssm.PutParameterOutput{}, nil
			}
			a.DeleteParametersFunc = func(_ context.Context, in *ssm.DeleteParametersInput) (*ssm.DeleteParametersOutput, error) {
				for _, name := range in.Names {
					delete(parameters, name)
				}
				return &ssm.DeleteParametersOutput{DeletedParameters: in.Names}, nil
			}
			a.RegisterTaskDefinitionFunc = func(_ context.Context, in *ecs.RegisterTaskDefinitionInput) (*ecs.RegisterTaskDefinitionOutput, error) {
				for _, env := range in.ContainerDefinitions[0].Environment {
					if aws.ToString(env.Value) == "admin-password" {
						t.Fatal("admin credential in definition")
					}
				}
				return &ecs.RegisterTaskDefinitionOutput{TaskDefinition: &ecstypes.TaskDefinition{TaskDefinitionArn: aws.String("definition")}}, nil
			}
			a.RunTaskFunc = func(context.Context, *ecs.RunTaskInput) (*ecs.RunTaskOutput, error) {
				if n.handle["id"] != "db-arn" {
					t.Fatal("admin ran before identity recorded")
				}
				return &ecs.RunTaskOutput{Tasks: []ecstypes.Task{{TaskArn: aws.String("job")}}}, nil
			}
			a.DescribeTasksFunc = func(context.Context, *ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
				exit := int32(0)
				if failed {
					exit = 7
				}
				return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{TaskArn: aws.String("job"), LastStatus: aws.String("STOPPED"), Containers: []ecstypes.Container{{ExitCode: aws.Int32(exit)}}}}}, nil
			}
			a.DeregisterTaskDefinitionFunc = func(context.Context, *ecs.DeregisterTaskDefinitionInput) (*ecs.DeregisterTaskDefinitionOutput, error) {
				return &ecs.DeregisterTaskDefinitionOutput{}, nil
			}
			a.DeleteTaskDefinitionsFunc = func(context.Context, *ecs.DeleteTaskDefinitionsInput) (*ecs.DeleteTaskDefinitionsOutput, error) {
				return &ecs.DeleteTaskDefinitionsOutput{}, nil
			}
			d := awsecs.NewDriver(awsecs.Dependencies{Installation: "installation", Needs: n, PostgresSetup: "test postgres setup", Owner: owner, Owned: func(context.Context) ([]deploy.Owned, error) { return owner.items, nil }, Credentials: func(context.Context) (map[string]string, error) {
				return map[string]string{"password": "app-password", "adminPassword": "admin-password"}, nil
			}, Client: func(context.Context) (*awsecs.API, awsecs.Config, awsecs.Estate, error) {
				return a.API(), awsecs.Config{Cluster: "existing", TaskSubnets: []string{"a", "b"}}, awsecs.Estate{VPC: "vpc"}, nil
			}, CopyImage: func(context.Context, deploy.Placement, string) (string, error) { return "ecr@sha256:abc", nil }})
			binding, handle, err := d.FulfilNeed(t.Context(), p, "postgres", map[string]string{}, func(string, ...any) {})
			if failed {
				if err == nil {
					t.Fatal("failed admin job accepted")
				}
			} else if err != nil || binding.Secret["PGUSER"] != "app" || binding.Secret["PGDATABASE"] != "app" || handle["id"] != "db-arn" {
				t.Fatalf("binding %+v handle %v: %v", binding, handle, err)
			}
			if len(parameters) > 0 {
				t.Fatal("admin credentials survived cleanup")
			}
		})
	}
}
