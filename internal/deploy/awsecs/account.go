package awsecs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

// Estate is the read-only attach result used for this operation's networking.
type Estate struct {
	VPC, LoadBalancerDNS string
	PublicIP             bool
	LoadBalancerGroups   []string
}

// CheckAccount refuses an unsuitable estate without making a resource. Account
// identity is checked before any other AWS call, including on a dry run.
func CheckAccount(ctx context.Context, a *API, c Config, installation string) (Estate, error) {
	var out Estate
	if err := c.Validate(); err != nil {
		return out, err
	}
	who, err := a.STS.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return out, err
	}
	if aws.ToString(who.Account) != c.Account {
		return out, fmt.Errorf("this key belongs to account %s, not %s, which this target names", aws.ToString(who.Account), c.Account)
	}
	regions, err := a.EC2.DescribeRegions(ctx, &ec2.DescribeRegionsInput{RegionNames: []string{c.Region}, AllRegions: aws.Bool(true)})
	if err != nil {
		return out, err
	}
	if len(regions.Regions) != 1 || (aws.ToString(regions.Regions[0].OptInStatus) != "opt-in-not-required" && aws.ToString(regions.Regions[0].OptInStatus) != "opted-in") {
		return out, fmt.Errorf("region %s is not enabled; enable it before attaching", c.Region)
	}
	clusters, err := a.ECS.DescribeClusters(ctx, &ecs.DescribeClustersInput{Clusters: []string{c.Cluster}})
	if err != nil {
		return out, err
	}
	if len(clusters.Failures) > 0 || len(clusters.Clusters) != 1 || aws.ToString(clusters.Clusters[0].Status) != "ACTIVE" || !slices.Contains(clusters.Clusters[0].CapacityProviders, "FARGATE") {
		return out, fmt.Errorf("cluster %s must be active and support FARGATE; update it before attaching", c.Cluster)
	}
	subs, err := a.EC2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: c.TaskSubnets})
	if err != nil {
		return out, err
	}
	if len(subs.Subnets) != len(c.TaskSubnets) {
		return out, errors.New("some task subnets were not found; name existing subnets")
	}
	var public *bool
	for _, s := range subs.Subnets {
		if out.VPC == "" {
			out.VPC = aws.ToString(s.VpcId)
		}
		if out.VPC != aws.ToString(s.VpcId) {
			return out, errors.New("task subnets span VPCs; choose subnets in one VPC")
		}
		tables, err := a.EC2.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{Filters: []ec2types.Filter{{Name: aws.String("association.subnet-id"), Values: []string{aws.ToString(s.SubnetId)}}}})
		if err != nil {
			return out, err
		}
		if len(tables.RouteTables) == 0 {
			tables, err = a.EC2.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{out.VPC}}, {Name: aws.String("association.main"), Values: []string{"true"}}}})
			if err != nil {
				return out, err
			}
		}
		found, isPublic := false, false
		for _, t := range tables.RouteTables {
			for _, r := range t.Routes {
				if aws.ToString(r.DestinationCidrBlock) == "0.0.0.0/0" && r.State == ec2types.RouteStateActive {
					if strings.HasPrefix(aws.ToString(r.GatewayId), "igw-") {
						found, isPublic = true, true
					}
					if aws.ToString(r.NatGatewayId) != "" {
						found = true
					}
				}
			}
		}
		if !found {
			return out, fmt.Errorf("tasks in %s could not pull images: it has no route to an internet gateway or a NAT gateway", aws.ToString(s.SubnetId))
		}
		if public != nil && *public != isPublic {
			return out, errors.New("task subnets mix public and private routes; choose one route type")
		}
		public = aws.Bool(isPublic)
	}
	out.PublicIP = public != nil && *public
	dbids := c.DatabaseSubnets
	if len(dbids) == 0 {
		dbids = c.TaskSubnets
	}
	dbsubs, err := a.EC2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: dbids})
	if err != nil {
		return out, err
	}
	zones := map[string]bool{}
	for _, s := range dbsubs.Subnets {
		if aws.ToString(s.VpcId) != out.VPC {
			return out, errors.New("database subnets are in another VPC; choose this target's VPC")
		}
		zones[aws.ToString(s.AvailabilityZone)] = true
	}
	if len(dbsubs.Subnets) != len(dbids) || len(zones) < 2 {
		return out, errors.New("database subnets must exist and span at least two zones; choose two zones in this VPC")
	}
	listeners, err := a.ELB.DescribeListeners(ctx, &elasticloadbalancingv2.DescribeListenersInput{ListenerArns: []string{c.Listener}})
	if err != nil {
		return out, err
	}
	if len(listeners.Listeners) != 1 || string(listeners.Listeners[0].Protocol) != "HTTPS" {
		return out, errors.New("listener must be HTTPS; name an existing HTTPS listener")
	}
	lbs, err := a.ELB.DescribeLoadBalancers(ctx, &elasticloadbalancingv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{aws.ToString(listeners.Listeners[0].LoadBalancerArn)}})
	if err != nil {
		return out, err
	}
	if len(lbs.LoadBalancers) != 1 {
		return out, errors.New("listener's load balancer was not found; name an existing listener")
	}
	lb := lbs.LoadBalancers[0]
	if string(lb.Type) != "application" || string(lb.Scheme) != "internet-facing" || aws.ToString(lb.VpcId) != out.VPC {
		return out, errors.New("listener must be on an internet-facing application load balancer in this VPC; choose another listener")
	}
	out.LoadBalancerDNS = aws.ToString(lb.DNSName)
	out.LoadBalancerGroups = lb.SecurityGroups
	covered := false
	var marker *string
	for {
		certs, err := a.ELB.DescribeListenerCertificates(ctx, &elasticloadbalancingv2.DescribeListenerCertificatesInput{ListenerArn: aws.String(c.Listener), Marker: marker})
		if err != nil {
			return out, err
		}
		for _, crt := range certs.Certificates {
			certificate, err := a.ACM.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: crt.CertificateArn})
			if err != nil {
				return out, err
			}
			if certificate.Certificate != nil && string(certificate.Certificate.Status) == "ISSUED" && slices.Contains(certificate.Certificate.SubjectAlternativeNames, "*."+c.Domain) {
				covered = true
			}
		}
		marker = certs.NextMarker
		if aws.ToString(marker) == "" {
			break
		}
	}
	if !covered {
		return out, fmt.Errorf("listener has no issued certificate for *.%s; attach that certificate first", c.Domain)
	}
	roleName := c.ExecutionRole[strings.LastIndex(c.ExecutionRole, "/")+1:]
	role, err := a.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(roleName)})
	if err != nil {
		return out, err
	}
	if role.Role == nil || aws.ToString(role.Role.Arn) != c.ExecutionRole {
		return out, errors.New("execution role identity differs; name its full ARN")
	}
	trust, err := url.QueryUnescape(aws.ToString(role.Role.AssumeRolePolicyDocument))
	if err != nil {
		return out, err
	}
	var doc struct {
		Statement []struct {
			Effect    string
			Action    any
			Principal struct{ Service any }
		}
	}
	if err := json.Unmarshal([]byte(trust), &doc); err != nil {
		return out, err
	}
	trusted := false
	for _, s := range doc.Statement {
		if s.Effect == "Allow" && containsValue(s.Action, "sts:AssumeRole") && containsValue(s.Principal.Service, "ecs-tasks.amazonaws.com") {
			trusted = true
		}
	}
	if !trusted {
		return out, errors.New("execution role does not trust ecs-tasks.amazonaws.com; update its trust policy")
	}
	prefix := installationPart(installation)
	base := "arn:aws:"
	checks := map[string][]string{
		"*": {"ecr:GetAuthorizationToken"},
		base + "ecr:" + c.Region + ":" + c.Account + ":repository/apptown/" + prefix + "/attach-check":                   {"ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer", "ecr:BatchCheckLayerAvailability"},
		base + "logs:" + c.Region + ":" + c.Account + ":log-group:/apptown/" + prefix + "/attach-check:log-stream:check": {"logs:CreateLogStream", "logs:PutLogEvents"},
		base + "ssm:" + c.Region + ":" + c.Account + ":parameter/apptown/" + prefix + "/attach-check":                    {"ssm:GetParameters"},
	}
	for resource, actions := range checks {
		result, err := a.IAM.SimulatePrincipalPolicy(ctx, &iam.SimulatePrincipalPolicyInput{PolicySourceArn: aws.String(c.ExecutionRole), ActionNames: actions, ResourceArns: []string{resource}})
		if err != nil {
			return out, err
		}
		allowed := map[string]bool{}
		for _, r := range result.EvaluationResults {
			allowed[aws.ToString(r.EvalActionName)] = r.EvalDecision == iamtypes.PolicyEvaluationDecisionTypeAllowed
		}
		for _, action := range actions {
			if !allowed[action] {
				return out, fmt.Errorf("execution role cannot %s on %s; update its policy", action, resource)
			}
		}
	}
	for name, service := range map[string]string{"AWSServiceRoleForECS": "ecs.amazonaws.com", "AWSServiceRoleForRDS": "rds.amazonaws.com"} {
		if _, err := a.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)}); err != nil {
			return out, fmt.Errorf("%s is unavailable; an administrator runs aws iam create-service-linked-role --aws-service-name %s: %w", name, service, err)
		}
	}
	if _, err := a.SSM.GetParametersByPath(ctx, &ssm.GetParametersByPathInput{Path: aws.String("/apptown/" + prefix + "/"), MaxResults: aws.Int32(1)}); err != nil {
		return out, fmt.Errorf("cannot read this installation's parameter prefix %s: %w", prefix, err)
	}
	_, err = a.EC2.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{DryRun: aws.Bool(true), GroupName: aws.String("at-" + prefix + "-attach-check"), Description: aws.String("AppTown attach permission check"), VpcId: aws.String(out.VPC), TagSpecifications: []ec2types.TagSpecification{{ResourceType: ec2types.ResourceTypeSecurityGroup, Tags: []ec2types.Tag{{Key: aws.String("apptown:instance"), Value: aws.String(installation)}}}}})
	if err == nil {
		return out, fmt.Errorf("AWS did not confirm the security-group dry run for installation %s (prefix %s); check the key's policy", installation, prefix)
	}
	var apierr smithy.APIError
	if !errors.As(err, &apierr) || apierr.ErrorCode() != "DryRunOperation" {
		return out, fmt.Errorf("key cannot create for installation %s (prefix %s) in %s; check the key's policy: %w", installation, prefix, out.VPC, err)
	}
	return out, nil
}

func containsValue(v any, want string) bool {
	switch x := v.(type) {
	case string:
		return x == want
	case []any:
		for _, s := range x {
			if s == want {
				return true
			}
		}
	}
	return false
}
