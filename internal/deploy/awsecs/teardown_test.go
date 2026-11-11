package awsecs_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/smithy-go"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/awsecs"
	"github.com/flokkit/apptown/internal/deploy/awsecs/awsfake"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestKeepDataTakeDownValidatesWholeListAndRetainsDatabaseNetwork(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "changed identity"}[foreign], func(t *testing.T) {
			p := deploy.Placement{ID: idstest.N(19)}
			owned := []deploy.Owned{{Kind: "security-group", ID: "app"}, {Kind: "security-group", ID: "db", Data: true}, {Kind: "rds-subnet-group", ID: "subnet-arn", Name: "subnet", Data: true}}
			a := &awsfake.Account{}
			a.DescribeSecurityGroupsFunc = func(_ context.Context, in *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error) {
				id := in.GroupIds[0]
				if foreign && id == "db" {
					id = "another-group"
				}
				return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{{GroupId: aws.String(id), Tags: []ec2types.Tag{{Key: aws.String("apptown:instance"), Value: aws.String("installation")}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}}}}, nil
			}
			a.RDSListTagsForResourceFunc = func(context.Context, *rds.ListTagsForResourceInput) (*rds.ListTagsForResourceOutput, error) {
				return &rds.ListTagsForResourceOutput{TagList: []rdstypes.Tag{{Key: aws.String("apptown:instance"), Value: aws.String("installation")}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}}, nil
			}
			revoked, deleted := false, false
			a.RevokeSecurityGroupIngressFunc = func(_ context.Context, in *ec2.RevokeSecurityGroupIngressInput) (*ec2.RevokeSecurityGroupIngressOutput, error) {
				if aws.ToString(in.GroupId) != "db" || aws.ToString(in.IpPermissions[0].UserIdGroupPairs[0].GroupId) != "app" {
					t.Fatal("changed foreign network")
				}
				revoked = true
				return &ec2.RevokeSecurityGroupIngressOutput{}, nil
			}
			a.DeleteSecurityGroupFunc = func(_ context.Context, in *ec2.DeleteSecurityGroupInput) (*ec2.DeleteSecurityGroupOutput, error) {
				if !revoked || aws.ToString(in.GroupId) != "app" {
					t.Fatal("deleted retained or referenced group")
				}
				deleted = true
				return &ec2.DeleteSecurityGroupOutput{}, nil
			}
			d := awsecs.NewDriver(awsecs.Dependencies{Installation: "installation", Client: func(context.Context) (*awsecs.API, awsecs.Config, awsecs.Estate, error) {
				return a.API(), awsecs.Config{}, awsecs.Estate{}, nil
			}})
			out, err := d.Teardown(t.Context(), p, 1, deploy.TakeDown{Owned: owned})
			if foreign {
				if err == nil || revoked || deleted {
					t.Fatalf("foreign identity had effects: %+v %v", out, err)
				}
			} else if err != nil || !deleted || len(out.Left) != 2 || !out.Left[0].Data || !out.Left[1].Data {
				t.Fatalf("retention: %+v %v", out, err)
			}
		})
	}
}

// A first release whose init step failed leaves the dedicated database but no
// service. Its take-down and copies must not wait on a service that never was.
func TestWritersOfANeverMadeServiceAreAlreadyStopped(t *testing.T) {
	for _, state := range []string{"never recorded", "recorded, gone"} {
		t.Run(state, func(t *testing.T) {
			p := deploy.Placement{ID: idstest.N(23)}
			owner := &jobOwner{}
			if state == "recorded, gone" {
				owner.items = []deploy.Owned{{Kind: "ecs-service", ID: "service"}}
			}
			a := &awsfake.Account{}
			a.DescribeServicesFunc = func(context.Context, *ecs.DescribeServicesInput) (*ecs.DescribeServicesOutput, error) {
				return &ecs.DescribeServicesOutput{Failures: []ecstypes.Failure{{Reason: aws.String("MISSING")}}}, nil
			}
			d := awsecs.NewDriver(awsecs.Dependencies{Installation: "installation", Owner: owner, Owned: func(context.Context) ([]deploy.Owned, error) { return owner.items, nil }, Client: func(context.Context) (*awsecs.API, awsecs.Config, awsecs.Estate, error) {
				return a.API(), awsecs.Config{Cluster: "cluster"}, awsecs.Estate{}, nil
			}})
			d.PollInterval, d.JobTimeout = time.Millisecond, 20*time.Millisecond
			resume, stopped, err := d.StopWriters(t.Context(), p, nil, nil)
			if err != nil || stopped != 0 || resume == nil {
				t.Fatalf("stopped %d: %v", stopped, err)
			}
			if slices.Contains(a.Calls, "UpdateService") {
				t.Fatal("changed a service that is not there")
			}
			if left := resume(t.Context()); len(left) != 0 {
				t.Fatalf("resume: %v", left)
			}
		})
	}
}

// Each data job deletes its task definition revision; the record stays. A
// take-down after any backup must treat that revision as gone, not refuse.
func TestTakeDownPassesOverDeletedJobDefinitions(t *testing.T) {
	for _, answer := range []string{"deleted", "deleting"} {
		t.Run(answer, func(t *testing.T) {
			p := deploy.Placement{ID: idstest.N(29)}
			owned := []deploy.Owned{{Kind: "task-definition", ID: "job:1", Name: "job"}, {Kind: "security-group", ID: "app"}}
			a := &awsfake.Account{}
			a.DescribeTaskDefinitionFunc = func(context.Context, *ecs.DescribeTaskDefinitionInput) (*ecs.DescribeTaskDefinitionOutput, error) {
				if answer == "deleted" {
					return nil, &smithy.GenericAPIError{Code: "ClientException", Message: "Unable to describe task definition."}
				}
				return &ecs.DescribeTaskDefinitionOutput{TaskDefinition: &ecstypes.TaskDefinition{TaskDefinitionArn: aws.String("job:1"), Status: ecstypes.TaskDefinitionStatusDeleteInProgress}}, nil
			}
			a.DescribeSecurityGroupsFunc = func(context.Context, *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error) {
				return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{{GroupId: aws.String("app"), Tags: []ec2types.Tag{{Key: aws.String("apptown:instance"), Value: aws.String("installation")}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}}}}, nil
			}
			a.DeleteSecurityGroupFunc = func(context.Context, *ec2.DeleteSecurityGroupInput) (*ec2.DeleteSecurityGroupOutput, error) {
				return &ec2.DeleteSecurityGroupOutput{}, nil
			}
			d := awsecs.NewDriver(awsecs.Dependencies{Installation: "installation", Client: func(context.Context) (*awsecs.API, awsecs.Config, awsecs.Estate, error) {
				return a.API(), awsecs.Config{Cluster: "cluster"}, awsecs.Estate{}, nil
			}})
			out, err := d.Teardown(t.Context(), p, 1, deploy.TakeDown{Owned: owned, BackedUp: true})
			if err != nil || len(out.Deleted) != 1 || slices.Contains(a.Calls, "DeregisterTaskDefinition") {
				t.Fatalf("take-down: %+v %v %v", out, err, a.Calls)
			}
		})
	}
}

// After a checked final set the database's group still admits the app's group;
// EC2 refuses to delete a group that another group's rule names.
func TestFinalTakeDownDeletesTheDatabaseGroupBeforeTheAppGroup(t *testing.T) {
	p := deploy.Placement{ID: idstest.N(37)}
	owned := []deploy.Owned{{Kind: "security-group", ID: "app"}, {Kind: "security-group", ID: "db", Data: true}}
	a := &awsfake.Account{}
	a.DescribeSecurityGroupsFunc = func(_ context.Context, in *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error) {
		return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{{GroupId: aws.String(in.GroupIds[0]), Tags: []ec2types.Tag{{Key: aws.String("apptown:instance"), Value: aws.String("installation")}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}}}}, nil
	}
	dbGone := false
	a.DeleteSecurityGroupFunc = func(_ context.Context, in *ec2.DeleteSecurityGroupInput) (*ec2.DeleteSecurityGroupOutput, error) {
		if aws.ToString(in.GroupId) == "app" && !dbGone {
			return nil, &smithy.GenericAPIError{Code: "DependencyViolation", Message: "resource app has a dependent object"}
		}
		dbGone = dbGone || aws.ToString(in.GroupId) == "db"
		return &ec2.DeleteSecurityGroupOutput{}, nil
	}
	d := awsecs.NewDriver(awsecs.Dependencies{Installation: "installation", Client: func(context.Context) (*awsecs.API, awsecs.Config, awsecs.Estate, error) {
		return a.API(), awsecs.Config{}, awsecs.Estate{}, nil
	}})
	d.PollInterval = time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	out, err := d.Teardown(ctx, p, 1, deploy.TakeDown{Owned: owned, BackedUp: true})
	if err != nil || len(out.Deleted) != 2 || len(out.Left) != 0 {
		t.Fatalf("take-down: %+v %v", out, err)
	}
}
