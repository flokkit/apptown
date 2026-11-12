package awsecs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	tagtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/flokkit/apptown/internal/deploy"
)

// ValidateTakeDown checks the complete recorded list before writers or routing
// change, while the final copy still needs the deployment's resources.
func (d *Driver) ValidateTakeDown(ctx context.Context, p deploy.Placement, owned []deploy.Owned) error {
	a, c, _, err := d.Client(ctx)
	if err != nil {
		return err
	}
	for _, o := range owned {
		if _, err := d.checkOwned(ctx, a, c, p, o); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) Teardown(ctx context.Context, p deploy.Placement, _ int64, td deploy.TakeDown) (deploy.TakenDown, error) {
	out := deploy.TakenDown{}
	a, c, _, err := d.Client(ctx)
	if err != nil {
		return out, err
	}
	live := map[string]bool{}
	// Validate the whole deletion list before the first effect.
	for _, o := range td.Owned {
		ok, err := d.checkOwned(ctx, a, c, p, o)
		if err != nil {
			return out, err
		}
		live[o.ID] = ok
	}
	// Retained DB groups must release their reference to the app group before
	// that group can be deleted. Only groups validated above can be changed.
	if !td.BackedUp && !td.Synthetic {
		for _, db := range td.Owned {
			if db.Kind != "security-group" || !db.Data || !live[db.ID] {
				continue
			}
			for _, app := range td.Owned {
				if app.Kind != "security-group" || app.Data || !live[app.ID] {
					continue
				}
				_, err := a.EC2.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{GroupId: aws.String(db.ID), IpPermissions: []ec2types.IpPermission{{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(5432), ToPort: aws.Int32(5432), UserIdGroupPairs: []ec2types.UserIdGroupPair{{GroupId: aws.String(app.ID)}}}}})
				if err != nil {
					var api interface{ ErrorCode() string }
					if !errors.As(err, &api) || api.ErrorCode() != "InvalidPermission.NotFound" {
						return out, err
					}
				}
			}
		}
	}
	owned := append([]deploy.Owned(nil), td.Owned...)
	order := map[string]int{"listener-rule": 0, "ecs-service": 1, "ecs-task": 2, "rds-instance": 3, "rds-subnet-group": 4, "task-definition": 5, "target-group": 6, "ecr-repository": 7, "ssm-parameter": 8, "log-group": 9, "security-group": 10}
	// The database's group admits the app's group, and EC2 refuses to delete
	// a group another group's rule names: the database's group goes first.
	sort.SliceStable(owned, func(i, j int) bool {
		if owned[i].Kind == owned[j].Kind {
			return owned[i].Data && !owned[j].Data
		}
		return order[owned[i].Kind] < order[owned[j].Kind]
	})
	for _, o := range owned {
		if !live[o.ID] {
			continue
		}
		if o.Data && !td.BackedUp && !td.Synthetic {
			out.Left = append(out.Left, deploy.Left{What: o.Kind, Handle: map[string]any{"id": o.ID, "name": o.Name, "kind": o.Kind}, Data: true, Why: "kept without a final copy; the dedicated RDS instance and its network continue to cost money"})
			continue
		}
		err = d.remove(ctx, a, c, o)
		if err != nil && !missing(err) {
			if o.Kind == "ecs-service" || o.Kind == "listener-rule" {
				return out, err
			}
			out.Left = append(out.Left, deploy.Left{What: o.Kind, Handle: map[string]any{"id": o.ID, "name": o.Name, "kind": o.Kind}, Data: o.Data, Why: err.Error()})
		} else {
			out.Deleted = append(out.Deleted, o.Label())
		}
	}
	return out, nil
}
func (d *Driver) remove(ctx context.Context, a *API, c Config, o deploy.Owned) error {
	var err error
	switch o.Kind {
	case "listener-rule":
		_, err = a.ELB.DeleteRule(ctx, &elasticloadbalancingv2.DeleteRuleInput{RuleArn: aws.String(o.ID)})
	case "ecs-service":
		service, e := a.ECS.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(c.Cluster), Services: []string{o.ID}})
		if e != nil {
			return e
		}
		for _, f := range service.Failures {
			if aws.ToString(f.Reason) != "MISSING" {
				return fmt.Errorf("describing service: %v", service.Failures)
			}
		}
		if len(service.Services) > 0 && aws.ToString(service.Services[0].Status) == "ACTIVE" {
			_, err = a.ECS.UpdateService(ctx, &ecs.UpdateServiceInput{Cluster: aws.String(c.Cluster), Service: aws.String(o.ID), DesiredCount: aws.Int32(0)})
			if err != nil {
				return err
			}
			_, err = a.ECS.DeleteService(ctx, &ecs.DeleteServiceInput{Cluster: aws.String(c.Cluster), Service: aws.String(o.ID), Force: aws.Bool(true)})
			if err != nil {
				return err
			}
		}
		for {
			out, e := a.ECS.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(c.Cluster), Services: []string{o.ID}})
			if e != nil {
				return e
			}
			for _, f := range out.Failures {
				if aws.ToString(f.Reason) != "MISSING" {
					return fmt.Errorf("waiting for service: %v", out.Failures)
				}
			}
			if len(out.Services) == 0 || aws.ToString(out.Services[0].Status) == "INACTIVE" {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d.PollInterval):
			}
		}
	case "ecs-task":
		_, err = a.ECS.StopTask(ctx, &ecs.StopTaskInput{Cluster: aws.String(c.Cluster), Task: aws.String(o.ID)})
		if err == nil {
			_, err = d.waitJob(ctx, a, c.Cluster, o.ID)
		}
	case "task-definition":
		_, err = a.ECS.DeregisterTaskDefinition(ctx, &ecs.DeregisterTaskDefinitionInput{TaskDefinition: aws.String(o.ID)})
		if err == nil {
			var out *ecs.DeleteTaskDefinitionsOutput
			out, err = a.ECS.DeleteTaskDefinitions(ctx, &ecs.DeleteTaskDefinitionsInput{TaskDefinitions: []string{o.ID}})
			if err == nil && len(out.Failures) > 0 {
				err = fmt.Errorf("task definition was not deleted: %v", out.Failures)
			}
		}
	case "target-group":
		_, err = a.ELB.DeleteTargetGroup(ctx, &elasticloadbalancingv2.DeleteTargetGroupInput{TargetGroupArn: aws.String(o.ID)})
	case "ecr-repository":
		_, err = a.ECR.DeleteRepository(ctx, &ecr.DeleteRepositoryInput{RepositoryName: aws.String(o.Name), Force: true})
	case "ssm-parameter":
		_, err = a.SSM.DeleteParameters(ctx, &ssm.DeleteParametersInput{Names: []string{o.Name}})
	case "log-group":
		_, err = a.Logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(o.Name)})
	case "rds-instance":
		err = d.deleteInstance(ctx, a, o.Name)
	case "rds-subnet-group":
		_, err = a.RDS.DeleteDBSubnetGroup(ctx, &rds.DeleteDBSubnetGroupInput{DBSubnetGroupName: aws.String(o.Name)})
	case "security-group":
		deadline := d.Now().Add(2 * time.Minute)
		for {
			_, err = a.EC2.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: aws.String(o.ID)})
			if err == nil || d.Now().After(deadline) {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d.PollInterval):
			}
		}
	default:
		err = fmt.Errorf("AWS cannot remove resource kind %s", o.Kind)
	}
	return err
}

// RemoveRoute stops new requests while the final copy still needs the task
// network, image repository, parameters and logs.
func (d *Driver) RemoveRoute(ctx context.Context, p deploy.Placement) error {
	a, c, _, err := d.Client(ctx)
	if err != nil {
		return err
	}
	o, ok, err := d.recorded(ctx, "listener-rule")
	if err != nil || !ok {
		return err
	}
	live, err := d.checkOwned(ctx, a, c, p, o)
	if err != nil || !live {
		return err
	}
	return d.remove(ctx, a, c, o)
}
func (d *Driver) Inventory(ctx context.Context) ([]deploy.Resource, error) {
	a, c, _, err := d.Client(ctx)
	if err != nil {
		return nil, err
	}
	if a.Tagging == nil {
		return nil, errors.New("AWS inventory has no tagging client")
	}
	var answer []deploy.Resource
	var token *string
	for {
		out, err := a.Tagging.GetResources(ctx, &resourcegroupstaggingapi.GetResourcesInput{TagFilters: []tagtypes.TagFilter{{Key: aws.String("apptown:instance"), Values: []string{d.Installation}}}, PaginationToken: token})
		if err != nil {
			return nil, err
		}
		for _, r := range out.ResourceTagMappingList {
			tags := map[string]string{}
			for _, t := range r.Tags {
				tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
			}
			if tags["apptown:instance"] != d.Installation {
				continue
			}
			arn := aws.ToString(r.ResourceARN)
			kind := ""
			for marker, k := range map[string]string{":service/": "ecs-service", ":task-definition/": "task-definition", ":task/": "ecs-task", ":targetgroup/": "target-group", ":listener-rule/": "listener-rule", ":security-group/": "security-group", ":repository/": "ecr-repository", ":log-group:": "log-group", ":parameter/": "ssm-parameter", ":db:": "database", ":subgrp:": "rds-subnet-group"} {
				if strings.Contains(arn, marker) {
					kind = k
					break
				}
			}
			if kind != "" {
				answer = append(answer, deploy.Resource{Kind: kind, ID: arn, Name: arn, Region: c.Region, Owner: tags["apptown:deployment"]})
			}
		}
		token = out.PaginationToken
		if aws.ToString(token) == "" {
			break
		}
	}
	return answer, nil
}
