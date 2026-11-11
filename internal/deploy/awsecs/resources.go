package awsecs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/smithy-go"
	"github.com/flokkit/apptown/internal/deploy"
)

func missing(err error) bool {
	var e smithy.APIError
	if !errors.As(err, &e) {
		return false
	}
	switch e.ErrorCode() {
	case "ServiceNotFoundException", "ClusterNotFoundException", "DBInstanceNotFound", "DBInstanceNotFoundFault", "DBSubnetGroupNotFoundFault", "RepositoryNotFoundException", "ResourceNotFoundException", "ParameterNotFound", "InvalidResourceId", "TargetGroupNotFound", "RuleNotFound", "InvalidGroup.NotFound":
		return true
	}
	return false
}
func (d *Driver) record(ctx context.Context, o deploy.Owned) error {
	if d.Owner == nil || d.Installation == "" {
		return errors.New("AWS requires an installation id and owned records before creating resources")
	}
	return d.Owner.Created(context.WithoutCancel(ctx), o)
}
func (d *Driver) recorded(ctx context.Context, kind string) (deploy.Owned, bool, error) {
	if d.Owned == nil {
		return deploy.Owned{}, false, errors.New("AWS requires owned records")
	}
	os, err := d.Owned(ctx)
	if err != nil {
		return deploy.Owned{}, false, err
	}
	for _, o := range os {
		if o.Kind == kind && !o.Data {
			return o, true, nil
		}
	}
	return deploy.Owned{}, false, nil
}
func (d *Driver) checkOwned(ctx context.Context, a *API, c Config, p deploy.Placement, o deploy.Owned) (bool, error) {
	tags := map[string]string{}
	identity := o.ID
	add := func(k, v *string) { tags[aws.ToString(k)] = aws.ToString(v) }
	var err error
	switch o.Kind {
	case "security-group":
		var out *ec2.DescribeSecurityGroupsOutput
		out, err = a.EC2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{o.ID}})
		if err == nil {
			if len(out.SecurityGroups) != 1 {
				return false, fmt.Errorf("cannot identify security group %s", o.ID)
			}
			g := out.SecurityGroups[0]
			identity = aws.ToString(g.GroupId)
			for _, t := range g.Tags {
				add(t.Key, t.Value)
			}
		}
	case "ecs-service":
		var out *ecs.DescribeServicesOutput
		out, err = a.ECS.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(c.Cluster), Services: []string{o.ID}, Include: []ecstypes.ServiceField{ecstypes.ServiceFieldTags}})
		if err == nil {
			if len(out.Services) == 0 {
				if len(out.Failures) == 1 && aws.ToString(out.Failures[0].Reason) == "MISSING" {
					return false, nil
				}
				return false, fmt.Errorf("cannot identify recorded service %s", o.ID)
			}
			g := out.Services[0]
			identity = aws.ToString(g.ServiceArn)
			for _, t := range g.Tags {
				add(t.Key, t.Value)
			}
		}
	case "task-definition":
		var out *ecs.DescribeTaskDefinitionOutput
		out, err = a.ECS.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: aws.String(o.ID), Include: []ecstypes.TaskDefinitionField{ecstypes.TaskDefinitionFieldTags}})
		// Every data job deletes its own revision while its record stays on
		// the owned list. ECS answers a deleted revision with a ClientException,
		// not a not-found code, and one still being deleted needs nothing more.
		var api smithy.APIError
		if err != nil && errors.As(err, &api) && api.ErrorCode() == "ClientException" && strings.Contains(api.ErrorMessage(), "Unable to describe task definition") {
			return false, nil
		}
		if err == nil {
			if out.TaskDefinition == nil || out.TaskDefinition.Status == ecstypes.TaskDefinitionStatusDeleteInProgress {
				return false, nil
			}
			identity = aws.ToString(out.TaskDefinition.TaskDefinitionArn)
			for _, t := range out.Tags {
				add(t.Key, t.Value)
			}
		}
	case "ecs-task":
		var out *ecs.DescribeTasksOutput
		out, err = a.ECS.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: aws.String(c.Cluster), Tasks: []string{o.ID}, Include: []ecstypes.TaskField{ecstypes.TaskFieldTags}})
		if err == nil {
			if len(out.Tasks) == 0 {
				if len(out.Failures) == 1 && aws.ToString(out.Failures[0].Reason) == "MISSING" {
					return false, nil
				}
				return false, fmt.Errorf("cannot identify recorded task %s", o.ID)
			}
			g := out.Tasks[0]
			identity = aws.ToString(g.TaskArn)
			for _, t := range g.Tags {
				add(t.Key, t.Value)
			}
		}
	case "target-group", "listener-rule":
		var out *elasticloadbalancingv2.DescribeTagsOutput
		out, err = a.ELB.DescribeTags(ctx, &elasticloadbalancingv2.DescribeTagsInput{ResourceArns: []string{o.ID}})
		if err == nil {
			if len(out.TagDescriptions) != 1 {
				return false, fmt.Errorf("cannot read tags of %s", o.ID)
			}
			identity = aws.ToString(out.TagDescriptions[0].ResourceArn)
			for _, t := range out.TagDescriptions[0].Tags {
				add(t.Key, t.Value)
			}
		}
	case "ecr-repository":
		var out *ecr.ListTagsForResourceOutput
		out, err = a.ECR.ListTagsForResource(ctx, &ecr.ListTagsForResourceInput{ResourceArn: aws.String(o.ID)})
		if err == nil {
			for _, t := range out.Tags {
				add(t.Key, t.Value)
			}
		}
	case "log-group":
		var out *cloudwatchlogs.ListTagsForResourceOutput
		out, err = a.Logs.ListTagsForResource(ctx, &cloudwatchlogs.ListTagsForResourceInput{ResourceArn: aws.String(strings.TrimSuffix(o.ID, ":*"))})
		if err == nil {
			tags = out.Tags
		}
	case "ssm-parameter":
		var out *ssm.ListTagsForResourceOutput
		out, err = a.SSM.ListTagsForResource(ctx, &ssm.ListTagsForResourceInput{ResourceType: "Parameter", ResourceId: aws.String(o.Name)})
		if err == nil {
			for _, t := range out.TagList {
				add(t.Key, t.Value)
			}
		}
	case "rds-instance", "rds-subnet-group":
		var out *rds.ListTagsForResourceOutput
		out, err = a.RDS.ListTagsForResource(ctx, &rds.ListTagsForResourceInput{ResourceName: aws.String(o.ID)})
		if err == nil {
			for _, t := range out.TagList {
				add(t.Key, t.Value)
			}
		}
	default:
		return false, fmt.Errorf("cannot verify AWS resource kind %s", o.Kind)
	}
	if missing(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if identity != o.ID || tags["apptown:instance"] != d.Installation || tags["apptown:deployment"] != p.ID.String() {
		return false, fmt.Errorf("%s no longer has its recorded identity and deployment tags; nothing was changed", o.Label())
	}
	return true, nil
}

func (d *Driver) ensureBase(ctx context.Context, a *API, c Config, e Estate, p deploy.Placement) (group, logs string, err error) {
	if d.Owner == nil || d.Owned == nil || d.Installation == "" {
		return "", "", errors.New("AWS requires an installation id and owned records before any effect")
	}
	for _, kind := range []string{"security-group", "log-group", "ecr-repository"} {
		o, ok, re := d.recorded(ctx, kind)
		if re != nil {
			return "", "", re
		}
		if ok {
			live, re := d.checkOwned(ctx, a, c, p, o)
			if re != nil {
				return "", "", re
			}
			ok = live
		}
		if !ok {
			name := d.name(p)
			switch kind {
			case "security-group":
				tags := []ec2types.Tag{}
				for _, t := range d.tags(p) {
					tags = append(tags, ec2types.Tag{Key: t.Key, Value: t.Value})
				}
				out, re := a.EC2.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{GroupName: aws.String(name), Description: aws.String("AppTown deployment"), VpcId: aws.String(e.VPC), TagSpecifications: []ec2types.TagSpecification{{ResourceType: ec2types.ResourceTypeSecurityGroup, Tags: tags}}})
				if re != nil {
					return "", "", re
				}
				o = deploy.Owned{Kind: kind, ID: aws.ToString(out.GroupId), Name: name}
			case "log-group":
				name = d.path(p)
				tags := map[string]string{"apptown:instance": d.Installation, "apptown:deployment": p.ID.String()}
				_, re := a.Logs.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(name), Tags: tags})
				if re != nil {
					return "", "", re
				}
				o = deploy.Owned{Kind: kind, ID: "arn:aws:logs:" + c.Region + ":" + c.Account + ":log-group:" + name, Name: name}
			case "ecr-repository":
				name = "apptown/" + installationPart(d.Installation) + "/" + deploy.HostKey(p.ID)
				tags := []ecrtypes.Tag{}
				for _, t := range d.tags(p) {
					tags = append(tags, ecrtypes.Tag{Key: t.Key, Value: t.Value})
				}
				out, re := a.ECR.CreateRepository(ctx, &ecr.CreateRepositoryInput{RepositoryName: aws.String(name), Tags: tags})
				if re != nil {
					return "", "", re
				}
				if out.Repository == nil {
					return "", "", errors.New("AWS returned no repository")
				}
				o = deploy.Owned{Kind: kind, ID: aws.ToString(out.Repository.RepositoryArn), Name: name}
			}
			if o.ID == "" {
				return "", "", errors.New("AWS returned no resource identity")
			}
			if re := d.record(ctx, o); re != nil {
				return "", "", re
			}
		}
		switch kind {
		case "security-group":
			group = o.ID
		case "log-group":
			logs = o.Name
			_, err = a.Logs.PutRetentionPolicy(ctx, &cloudwatchlogs.PutRetentionPolicyInput{LogGroupName: aws.String(logs), RetentionInDays: aws.Int32(14)})
			if err != nil {
				return "", "", err
			}
		}
	}
	return group, logs, nil
}
