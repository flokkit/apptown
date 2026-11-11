package awsecs

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"strings"
)

// sanitizedError keeps provider diagnostics while removing the supplied keys.
// Do not wrap the original error: downstream error renderers may unwrap it.
func sanitizedError(err error, keyID, secret string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	clean := func(s string) string {
		for _, v := range []string{secret, keyID} {
			if v != "" {
				s = strings.ReplaceAll(s, v, "<credential>")
			}
		}
		return s
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		return &smithy.GenericAPIError{Code: clean(api.ErrorCode()), Message: clean(api.ErrorMessage()), Fault: api.ErrorFault()}
	}
	return errors.New(clean(err.Error()))
}

type sdkSTS struct {
	client        *sts.Client
	keyID, secret string
}

func (a sdkSTS) GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, opt ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	out, err := a.client.GetCallerIdentity(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

type sdkEC2 struct {
	client        *ec2.Client
	keyID, secret string
}

func (a sdkEC2) DescribeRegions(ctx context.Context, in *ec2.DescribeRegionsInput, opt ...func(*ec2.Options)) (*ec2.DescribeRegionsOutput, error) {
	out, err := a.client.DescribeRegions(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}
func (a sdkEC2) DescribeSubnets(ctx context.Context, in *ec2.DescribeSubnetsInput, opt ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	out, err := a.client.DescribeSubnets(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}
func (a sdkEC2) DescribeRouteTables(ctx context.Context, in *ec2.DescribeRouteTablesInput, opt ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	out, err := a.client.DescribeRouteTables(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}
func (a sdkEC2) DescribeSecurityGroups(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, opt ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	out, err := a.client.DescribeSecurityGroups(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}
func (a sdkEC2) CreateSecurityGroup(ctx context.Context, in *ec2.CreateSecurityGroupInput, opt ...func(*ec2.Options)) (*ec2.CreateSecurityGroupOutput, error) {
	out, err := a.client.CreateSecurityGroup(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

type sdkECS struct {
	client        *ecs.Client
	keyID, secret string
}

func (a sdkECS) DescribeClusters(ctx context.Context, in *ecs.DescribeClustersInput, opt ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error) {
	out, err := a.client.DescribeClusters(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}
func (a sdkECS) DescribeTasks(ctx context.Context, in *ecs.DescribeTasksInput, opt ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	out, err := a.client.DescribeTasks(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}
func (a sdkECS) RegisterTaskDefinition(ctx context.Context, in *ecs.RegisterTaskDefinitionInput, opt ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error) {
	out, err := a.client.RegisterTaskDefinition(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}
func (a sdkECS) DeregisterTaskDefinition(ctx context.Context, in *ecs.DeregisterTaskDefinitionInput, opt ...func(*ecs.Options)) (*ecs.DeregisterTaskDefinitionOutput, error) {
	out, err := a.client.DeregisterTaskDefinition(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}
func (a sdkECS) DeleteTaskDefinitions(ctx context.Context, in *ecs.DeleteTaskDefinitionsInput, opt ...func(*ecs.Options)) (*ecs.DeleteTaskDefinitionsOutput, error) {
	out, err := a.client.DeleteTaskDefinitions(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}
func (a sdkECS) RunTask(ctx context.Context, in *ecs.RunTaskInput, opt ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
	out, err := a.client.RunTask(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}
func (a sdkECS) StopTask(ctx context.Context, in *ecs.StopTaskInput, opt ...func(*ecs.Options)) (*ecs.StopTaskOutput, error) {
	out, err := a.client.StopTask(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

type sdkELB struct {
	client        *elasticloadbalancingv2.Client
	keyID, secret string
}

func (a sdkELB) DescribeListeners(ctx context.Context, in *elasticloadbalancingv2.DescribeListenersInput, opt ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeListenersOutput, error) {
	out, err := a.client.DescribeListeners(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}
func (a sdkELB) DescribeListenerCertificates(ctx context.Context, in *elasticloadbalancingv2.DescribeListenerCertificatesInput, opt ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeListenerCertificatesOutput, error) {
	out, err := a.client.DescribeListenerCertificates(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}
func (a sdkELB) DescribeLoadBalancers(ctx context.Context, in *elasticloadbalancingv2.DescribeLoadBalancersInput, opt ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
	out, err := a.client.DescribeLoadBalancers(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

type sdkSSM struct {
	client        *ssm.Client
	keyID, secret string
}

func (a sdkSSM) PutParameter(ctx context.Context, in *ssm.PutParameterInput, opt ...func(*ssm.Options)) (*ssm.PutParameterOutput, error) {
	out, err := a.client.PutParameter(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}
func (a sdkSSM) DeleteParameters(ctx context.Context, in *ssm.DeleteParametersInput, opt ...func(*ssm.Options)) (*ssm.DeleteParametersOutput, error) {
	out, err := a.client.DeleteParameters(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}
func (a sdkSSM) GetParametersByPath(ctx context.Context, in *ssm.GetParametersByPathInput, opt ...func(*ssm.Options)) (*ssm.GetParametersByPathOutput, error) {
	out, err := a.client.GetParametersByPath(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

type sdkIAM struct {
	client        *iam.Client
	keyID, secret string
}

func (a sdkIAM) GetRole(ctx context.Context, in *iam.GetRoleInput, opt ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	out, err := a.client.GetRole(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}
func (a sdkIAM) SimulatePrincipalPolicy(ctx context.Context, in *iam.SimulatePrincipalPolicyInput, opt ...func(*iam.Options)) (*iam.SimulatePrincipalPolicyOutput, error) {
	out, err := a.client.SimulatePrincipalPolicy(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

type sdkACM struct {
	client        *acm.Client
	keyID, secret string
}

func (a sdkACM) DescribeCertificate(ctx context.Context, in *acm.DescribeCertificateInput, opt ...func(*acm.Options)) (*acm.DescribeCertificateOutput, error) {
	out, err := a.client.DescribeCertificate(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkEC2) AuthorizeSecurityGroupIngress(ctx context.Context, in *ec2.AuthorizeSecurityGroupIngressInput, opt ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupIngressOutput, error) {
	out, err := a.client.AuthorizeSecurityGroupIngress(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkEC2) RevokeSecurityGroupIngress(ctx context.Context, in *ec2.RevokeSecurityGroupIngressInput, opt ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupIngressOutput, error) {
	out, err := a.client.RevokeSecurityGroupIngress(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkEC2) DeleteSecurityGroup(ctx context.Context, in *ec2.DeleteSecurityGroupInput, opt ...func(*ec2.Options)) (*ec2.DeleteSecurityGroupOutput, error) {
	out, err := a.client.DeleteSecurityGroup(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkECS) CreateService(ctx context.Context, in *ecs.CreateServiceInput, opt ...func(*ecs.Options)) (*ecs.CreateServiceOutput, error) {
	out, err := a.client.CreateService(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkECS) UpdateService(ctx context.Context, in *ecs.UpdateServiceInput, opt ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
	out, err := a.client.UpdateService(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkECS) DeleteService(ctx context.Context, in *ecs.DeleteServiceInput, opt ...func(*ecs.Options)) (*ecs.DeleteServiceOutput, error) {
	out, err := a.client.DeleteService(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkECS) DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, opt ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	out, err := a.client.DescribeServices(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkECS) ListTasks(ctx context.Context, in *ecs.ListTasksInput, opt ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	out, err := a.client.ListTasks(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkECS) DescribeTaskDefinition(ctx context.Context, in *ecs.DescribeTaskDefinitionInput, opt ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error) {
	out, err := a.client.DescribeTaskDefinition(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkELB) CreateTargetGroup(ctx context.Context, in *elasticloadbalancingv2.CreateTargetGroupInput, opt ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.CreateTargetGroupOutput, error) {
	out, err := a.client.CreateTargetGroup(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkELB) DescribeTargetGroups(ctx context.Context, in *elasticloadbalancingv2.DescribeTargetGroupsInput, opt ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error) {
	out, err := a.client.DescribeTargetGroups(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkELB) DeleteTargetGroup(ctx context.Context, in *elasticloadbalancingv2.DeleteTargetGroupInput, opt ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DeleteTargetGroupOutput, error) {
	out, err := a.client.DeleteTargetGroup(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkELB) CreateRule(ctx context.Context, in *elasticloadbalancingv2.CreateRuleInput, opt ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.CreateRuleOutput, error) {
	out, err := a.client.CreateRule(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkELB) DescribeRules(ctx context.Context, in *elasticloadbalancingv2.DescribeRulesInput, opt ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeRulesOutput, error) {
	out, err := a.client.DescribeRules(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkELB) DeleteRule(ctx context.Context, in *elasticloadbalancingv2.DeleteRuleInput, opt ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DeleteRuleOutput, error) {
	out, err := a.client.DeleteRule(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkELB) DescribeTags(ctx context.Context, in *elasticloadbalancingv2.DescribeTagsInput, opt ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTagsOutput, error) {
	out, err := a.client.DescribeTags(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkELB) DescribeTargetHealth(ctx context.Context, in *elasticloadbalancingv2.DescribeTargetHealthInput, opt ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error) {
	out, err := a.client.DescribeTargetHealth(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

type sdkRDS struct {
	client        *rds.Client
	keyID, secret string
}

func (a sdkRDS) CreateDBInstance(ctx context.Context, in *rds.CreateDBInstanceInput, opt ...func(*rds.Options)) (*rds.CreateDBInstanceOutput, error) {
	out, err := a.client.CreateDBInstance(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkRDS) DescribeDBInstances(ctx context.Context, in *rds.DescribeDBInstancesInput, opt ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	out, err := a.client.DescribeDBInstances(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkRDS) DeleteDBInstance(ctx context.Context, in *rds.DeleteDBInstanceInput, opt ...func(*rds.Options)) (*rds.DeleteDBInstanceOutput, error) {
	out, err := a.client.DeleteDBInstance(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkRDS) CreateDBSubnetGroup(ctx context.Context, in *rds.CreateDBSubnetGroupInput, opt ...func(*rds.Options)) (*rds.CreateDBSubnetGroupOutput, error) {
	out, err := a.client.CreateDBSubnetGroup(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkRDS) DescribeDBSubnetGroups(ctx context.Context, in *rds.DescribeDBSubnetGroupsInput, opt ...func(*rds.Options)) (*rds.DescribeDBSubnetGroupsOutput, error) {
	out, err := a.client.DescribeDBSubnetGroups(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkRDS) DeleteDBSubnetGroup(ctx context.Context, in *rds.DeleteDBSubnetGroupInput, opt ...func(*rds.Options)) (*rds.DeleteDBSubnetGroupOutput, error) {
	out, err := a.client.DeleteDBSubnetGroup(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkRDS) ListTagsForResource(ctx context.Context, in *rds.ListTagsForResourceInput, opt ...func(*rds.Options)) (*rds.ListTagsForResourceOutput, error) {
	out, err := a.client.ListTagsForResource(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkRDS) DescribeDBSnapshots(ctx context.Context, in *rds.DescribeDBSnapshotsInput, opt ...func(*rds.Options)) (*rds.DescribeDBSnapshotsOutput, error) {
	out, err := a.client.DescribeDBSnapshots(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

type sdkECR struct {
	client        *ecr.Client
	keyID, secret string
}

func (a sdkECR) CreateRepository(ctx context.Context, in *ecr.CreateRepositoryInput, opt ...func(*ecr.Options)) (*ecr.CreateRepositoryOutput, error) {
	out, err := a.client.CreateRepository(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkECR) DescribeRepositories(ctx context.Context, in *ecr.DescribeRepositoriesInput, opt ...func(*ecr.Options)) (*ecr.DescribeRepositoriesOutput, error) {
	out, err := a.client.DescribeRepositories(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkECR) DeleteRepository(ctx context.Context, in *ecr.DeleteRepositoryInput, opt ...func(*ecr.Options)) (*ecr.DeleteRepositoryOutput, error) {
	out, err := a.client.DeleteRepository(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkECR) GetAuthorizationToken(ctx context.Context, in *ecr.GetAuthorizationTokenInput, opt ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
	out, err := a.client.GetAuthorizationToken(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkECR) ListTagsForResource(ctx context.Context, in *ecr.ListTagsForResourceInput, opt ...func(*ecr.Options)) (*ecr.ListTagsForResourceOutput, error) {
	out, err := a.client.ListTagsForResource(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

type sdkLogs struct {
	client        *cloudwatchlogs.Client
	keyID, secret string
}

func (a sdkLogs) CreateLogGroup(ctx context.Context, in *cloudwatchlogs.CreateLogGroupInput, opt ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogGroupOutput, error) {
	out, err := a.client.CreateLogGroup(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkLogs) DescribeLogGroups(ctx context.Context, in *cloudwatchlogs.DescribeLogGroupsInput, opt ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
	out, err := a.client.DescribeLogGroups(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkLogs) PutRetentionPolicy(ctx context.Context, in *cloudwatchlogs.PutRetentionPolicyInput, opt ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutRetentionPolicyOutput, error) {
	out, err := a.client.PutRetentionPolicy(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkLogs) DeleteLogGroup(ctx context.Context, in *cloudwatchlogs.DeleteLogGroupInput, opt ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DeleteLogGroupOutput, error) {
	out, err := a.client.DeleteLogGroup(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkLogs) ListTagsForResource(ctx context.Context, in *cloudwatchlogs.ListTagsForResourceInput, opt ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.ListTagsForResourceOutput, error) {
	out, err := a.client.ListTagsForResource(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

type sdkTagging struct {
	client        *resourcegroupstaggingapi.Client
	keyID, secret string
}

func (a sdkTagging) GetResources(ctx context.Context, in *resourcegroupstaggingapi.GetResourcesInput, opt ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
	out, err := a.client.GetResources(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

type sdkPricing struct {
	client        *pricing.Client
	keyID, secret string
}

func (a sdkPricing) GetProducts(ctx context.Context, in *pricing.GetProductsInput, opt ...func(*pricing.Options)) (*pricing.GetProductsOutput, error) {
	out, err := a.client.GetProducts(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkSSM) GetParameters(ctx context.Context, in *ssm.GetParametersInput, opt ...func(*ssm.Options)) (*ssm.GetParametersOutput, error) {
	out, err := a.client.GetParameters(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}

func (a sdkSSM) ListTagsForResource(ctx context.Context, in *ssm.ListTagsForResourceInput, opt ...func(*ssm.Options)) (*ssm.ListTagsForResourceOutput, error) {
	out, err := a.client.ListTagsForResource(ctx, in, opt...)
	return out, sanitizedError(err, a.keyID, a.secret)
}
