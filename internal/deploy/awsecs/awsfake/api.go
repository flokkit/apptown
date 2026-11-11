// Package awsfake is the interface-level AWS fake beside the driver.
// It models only calls exercised by driver tests, not IAM authorization.
package awsfake

import (
	"context"
	"fmt"
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
	"github.com/flokkit/apptown/internal/deploy/awsecs"
	"sync"
)

// Account records calls and supplies service answers. Unconfigured calls fail.
type Account struct {
	SSMListTagsForResourceFunc        func(context.Context, *ssm.ListTagsForResourceInput) (*ssm.ListTagsForResourceOutput, error)
	GetParametersFunc                 func(context.Context, *ssm.GetParametersInput) (*ssm.GetParametersOutput, error)
	GetProductsFunc                   func(context.Context, *pricing.GetProductsInput) (*pricing.GetProductsOutput, error)
	GetResourcesFunc                  func(context.Context, *resourcegroupstaggingapi.GetResourcesInput) (*resourcegroupstaggingapi.GetResourcesOutput, error)
	LogsListTagsForResourceFunc       func(context.Context, *cloudwatchlogs.ListTagsForResourceInput) (*cloudwatchlogs.ListTagsForResourceOutput, error)
	DeleteLogGroupFunc                func(context.Context, *cloudwatchlogs.DeleteLogGroupInput) (*cloudwatchlogs.DeleteLogGroupOutput, error)
	PutRetentionPolicyFunc            func(context.Context, *cloudwatchlogs.PutRetentionPolicyInput) (*cloudwatchlogs.PutRetentionPolicyOutput, error)
	DescribeLogGroupsFunc             func(context.Context, *cloudwatchlogs.DescribeLogGroupsInput) (*cloudwatchlogs.DescribeLogGroupsOutput, error)
	CreateLogGroupFunc                func(context.Context, *cloudwatchlogs.CreateLogGroupInput) (*cloudwatchlogs.CreateLogGroupOutput, error)
	ECRListTagsForResourceFunc        func(context.Context, *ecr.ListTagsForResourceInput) (*ecr.ListTagsForResourceOutput, error)
	GetAuthorizationTokenFunc         func(context.Context, *ecr.GetAuthorizationTokenInput) (*ecr.GetAuthorizationTokenOutput, error)
	DeleteRepositoryFunc              func(context.Context, *ecr.DeleteRepositoryInput) (*ecr.DeleteRepositoryOutput, error)
	DescribeRepositoriesFunc          func(context.Context, *ecr.DescribeRepositoriesInput) (*ecr.DescribeRepositoriesOutput, error)
	CreateRepositoryFunc              func(context.Context, *ecr.CreateRepositoryInput) (*ecr.CreateRepositoryOutput, error)
	DescribeDBSnapshotsFunc           func(context.Context, *rds.DescribeDBSnapshotsInput) (*rds.DescribeDBSnapshotsOutput, error)
	RDSListTagsForResourceFunc        func(context.Context, *rds.ListTagsForResourceInput) (*rds.ListTagsForResourceOutput, error)
	DeleteDBSubnetGroupFunc           func(context.Context, *rds.DeleteDBSubnetGroupInput) (*rds.DeleteDBSubnetGroupOutput, error)
	DescribeDBSubnetGroupsFunc        func(context.Context, *rds.DescribeDBSubnetGroupsInput) (*rds.DescribeDBSubnetGroupsOutput, error)
	CreateDBSubnetGroupFunc           func(context.Context, *rds.CreateDBSubnetGroupInput) (*rds.CreateDBSubnetGroupOutput, error)
	DeleteDBInstanceFunc              func(context.Context, *rds.DeleteDBInstanceInput) (*rds.DeleteDBInstanceOutput, error)
	DescribeDBInstancesFunc           func(context.Context, *rds.DescribeDBInstancesInput) (*rds.DescribeDBInstancesOutput, error)
	CreateDBInstanceFunc              func(context.Context, *rds.CreateDBInstanceInput) (*rds.CreateDBInstanceOutput, error)
	DescribeTargetHealthFunc          func(context.Context, *elasticloadbalancingv2.DescribeTargetHealthInput) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error)
	DescribeTagsFunc                  func(context.Context, *elasticloadbalancingv2.DescribeTagsInput) (*elasticloadbalancingv2.DescribeTagsOutput, error)
	DeleteRuleFunc                    func(context.Context, *elasticloadbalancingv2.DeleteRuleInput) (*elasticloadbalancingv2.DeleteRuleOutput, error)
	DescribeRulesFunc                 func(context.Context, *elasticloadbalancingv2.DescribeRulesInput) (*elasticloadbalancingv2.DescribeRulesOutput, error)
	CreateRuleFunc                    func(context.Context, *elasticloadbalancingv2.CreateRuleInput) (*elasticloadbalancingv2.CreateRuleOutput, error)
	DeleteTargetGroupFunc             func(context.Context, *elasticloadbalancingv2.DeleteTargetGroupInput) (*elasticloadbalancingv2.DeleteTargetGroupOutput, error)
	DescribeTargetGroupsFunc          func(context.Context, *elasticloadbalancingv2.DescribeTargetGroupsInput) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error)
	CreateTargetGroupFunc             func(context.Context, *elasticloadbalancingv2.CreateTargetGroupInput) (*elasticloadbalancingv2.CreateTargetGroupOutput, error)
	DescribeTaskDefinitionFunc        func(context.Context, *ecs.DescribeTaskDefinitionInput) (*ecs.DescribeTaskDefinitionOutput, error)
	ListTasksFunc                     func(context.Context, *ecs.ListTasksInput) (*ecs.ListTasksOutput, error)
	DescribeServicesFunc              func(context.Context, *ecs.DescribeServicesInput) (*ecs.DescribeServicesOutput, error)
	DeleteServiceFunc                 func(context.Context, *ecs.DeleteServiceInput) (*ecs.DeleteServiceOutput, error)
	UpdateServiceFunc                 func(context.Context, *ecs.UpdateServiceInput) (*ecs.UpdateServiceOutput, error)
	CreateServiceFunc                 func(context.Context, *ecs.CreateServiceInput) (*ecs.CreateServiceOutput, error)
	DeleteSecurityGroupFunc           func(context.Context, *ec2.DeleteSecurityGroupInput) (*ec2.DeleteSecurityGroupOutput, error)
	AuthorizeSecurityGroupIngressFunc func(context.Context, *ec2.AuthorizeSecurityGroupIngressInput) (*ec2.AuthorizeSecurityGroupIngressOutput, error)
	RevokeSecurityGroupIngressFunc    func(context.Context, *ec2.RevokeSecurityGroupIngressInput) (*ec2.RevokeSecurityGroupIngressOutput, error)
	mu                                sync.Mutex
	Calls                             []string
	Fail                              map[string]error
	GetCallerIdentityFunc             func(context.Context, *sts.GetCallerIdentityInput) (*sts.GetCallerIdentityOutput, error)
	DescribeRegionsFunc               func(context.Context, *ec2.DescribeRegionsInput) (*ec2.DescribeRegionsOutput, error)
	DescribeSubnetsFunc               func(context.Context, *ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error)
	DescribeRouteTablesFunc           func(context.Context, *ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error)
	DescribeSecurityGroupsFunc        func(context.Context, *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error)
	CreateSecurityGroupFunc           func(context.Context, *ec2.CreateSecurityGroupInput) (*ec2.CreateSecurityGroupOutput, error)
	DescribeClustersFunc              func(context.Context, *ecs.DescribeClustersInput) (*ecs.DescribeClustersOutput, error)
	DescribeTasksFunc                 func(context.Context, *ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error)
	RegisterTaskDefinitionFunc        func(context.Context, *ecs.RegisterTaskDefinitionInput) (*ecs.RegisterTaskDefinitionOutput, error)
	DeregisterTaskDefinitionFunc      func(context.Context, *ecs.DeregisterTaskDefinitionInput) (*ecs.DeregisterTaskDefinitionOutput, error)
	DeleteTaskDefinitionsFunc         func(context.Context, *ecs.DeleteTaskDefinitionsInput) (*ecs.DeleteTaskDefinitionsOutput, error)
	RunTaskFunc                       func(context.Context, *ecs.RunTaskInput) (*ecs.RunTaskOutput, error)
	StopTaskFunc                      func(context.Context, *ecs.StopTaskInput) (*ecs.StopTaskOutput, error)
	DescribeListenersFunc             func(context.Context, *elasticloadbalancingv2.DescribeListenersInput) (*elasticloadbalancingv2.DescribeListenersOutput, error)
	DescribeListenerCertificatesFunc  func(context.Context, *elasticloadbalancingv2.DescribeListenerCertificatesInput) (*elasticloadbalancingv2.DescribeListenerCertificatesOutput, error)
	DescribeLoadBalancersFunc         func(context.Context, *elasticloadbalancingv2.DescribeLoadBalancersInput) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error)
	PutParameterFunc                  func(context.Context, *ssm.PutParameterInput) (*ssm.PutParameterOutput, error)
	DeleteParametersFunc              func(context.Context, *ssm.DeleteParametersInput) (*ssm.DeleteParametersOutput, error)
	GetParametersByPathFunc           func(context.Context, *ssm.GetParametersByPathInput) (*ssm.GetParametersByPathOutput, error)
	GetRoleFunc                       func(context.Context, *iam.GetRoleInput) (*iam.GetRoleOutput, error)
	SimulatePrincipalPolicyFunc       func(context.Context, *iam.SimulatePrincipalPolicyInput) (*iam.SimulatePrincipalPolicyOutput, error)
	DescribeCertificateFunc           func(context.Context, *acm.DescribeCertificateInput) (*acm.DescribeCertificateOutput, error)
}

func (a *Account) called(method string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Calls = append(a.Calls, method)
	return a.Fail[method]
}
func (a *Account) API() *awsecs.API {
	return &awsecs.API{Pricing: a, Tagging: a, Logs: logsAPI{a}, ECR: ecrAPI{a}, RDS: rdsAPI{a}, STS: a, EC2: a, ECS: a, ELB: a, SSM: ssmAPI{a}, IAM: a, ACM: a}
}
func (a *Account) GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, _ ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	if err := a.called("GetCallerIdentity"); err != nil {
		return nil, err
	}
	if a.GetCallerIdentityFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call GetCallerIdentity")
	}
	return a.GetCallerIdentityFunc(ctx, in)
}
func (a *Account) DescribeRegions(ctx context.Context, in *ec2.DescribeRegionsInput, _ ...func(*ec2.Options)) (*ec2.DescribeRegionsOutput, error) {
	if err := a.called("DescribeRegions"); err != nil {
		return nil, err
	}
	if a.DescribeRegionsFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeRegions")
	}
	return a.DescribeRegionsFunc(ctx, in)
}
func (a *Account) DescribeSubnets(ctx context.Context, in *ec2.DescribeSubnetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	if err := a.called("DescribeSubnets"); err != nil {
		return nil, err
	}
	if a.DescribeSubnetsFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeSubnets")
	}
	return a.DescribeSubnetsFunc(ctx, in)
}
func (a *Account) DescribeRouteTables(ctx context.Context, in *ec2.DescribeRouteTablesInput, _ ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	if err := a.called("DescribeRouteTables"); err != nil {
		return nil, err
	}
	if a.DescribeRouteTablesFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeRouteTables")
	}
	return a.DescribeRouteTablesFunc(ctx, in)
}
func (a *Account) DescribeSecurityGroups(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	if err := a.called("DescribeSecurityGroups"); err != nil {
		return nil, err
	}
	if a.DescribeSecurityGroupsFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeSecurityGroups")
	}
	return a.DescribeSecurityGroupsFunc(ctx, in)
}
func (a *Account) CreateSecurityGroup(ctx context.Context, in *ec2.CreateSecurityGroupInput, _ ...func(*ec2.Options)) (*ec2.CreateSecurityGroupOutput, error) {
	if err := a.called("CreateSecurityGroup"); err != nil {
		return nil, err
	}
	if a.CreateSecurityGroupFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call CreateSecurityGroup")
	}
	return a.CreateSecurityGroupFunc(ctx, in)
}
func (a *Account) DescribeClusters(ctx context.Context, in *ecs.DescribeClustersInput, _ ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error) {
	if err := a.called("DescribeClusters"); err != nil {
		return nil, err
	}
	if a.DescribeClustersFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeClusters")
	}
	return a.DescribeClustersFunc(ctx, in)
}
func (a *Account) DescribeTasks(ctx context.Context, in *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	if err := a.called("DescribeTasks"); err != nil {
		return nil, err
	}
	if a.DescribeTasksFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeTasks")
	}
	return a.DescribeTasksFunc(ctx, in)
}
func (a *Account) RegisterTaskDefinition(ctx context.Context, in *ecs.RegisterTaskDefinitionInput, _ ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error) {
	if err := a.called("RegisterTaskDefinition"); err != nil {
		return nil, err
	}
	if a.RegisterTaskDefinitionFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call RegisterTaskDefinition")
	}
	return a.RegisterTaskDefinitionFunc(ctx, in)
}
func (a *Account) DeregisterTaskDefinition(ctx context.Context, in *ecs.DeregisterTaskDefinitionInput, _ ...func(*ecs.Options)) (*ecs.DeregisterTaskDefinitionOutput, error) {
	if err := a.called("DeregisterTaskDefinition"); err != nil {
		return nil, err
	}
	if a.DeregisterTaskDefinitionFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DeregisterTaskDefinition")
	}
	return a.DeregisterTaskDefinitionFunc(ctx, in)
}
func (a *Account) DeleteTaskDefinitions(ctx context.Context, in *ecs.DeleteTaskDefinitionsInput, _ ...func(*ecs.Options)) (*ecs.DeleteTaskDefinitionsOutput, error) {
	if err := a.called("DeleteTaskDefinitions"); err != nil {
		return nil, err
	}
	if a.DeleteTaskDefinitionsFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DeleteTaskDefinitions")
	}
	return a.DeleteTaskDefinitionsFunc(ctx, in)
}
func (a *Account) RunTask(ctx context.Context, in *ecs.RunTaskInput, _ ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
	if err := a.called("RunTask"); err != nil {
		return nil, err
	}
	if a.RunTaskFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call RunTask")
	}
	return a.RunTaskFunc(ctx, in)
}
func (a *Account) StopTask(ctx context.Context, in *ecs.StopTaskInput, _ ...func(*ecs.Options)) (*ecs.StopTaskOutput, error) {
	if err := a.called("StopTask"); err != nil {
		return nil, err
	}
	if a.StopTaskFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call StopTask")
	}
	return a.StopTaskFunc(ctx, in)
}
func (a *Account) DescribeListeners(ctx context.Context, in *elasticloadbalancingv2.DescribeListenersInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeListenersOutput, error) {
	if err := a.called("DescribeListeners"); err != nil {
		return nil, err
	}
	if a.DescribeListenersFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeListeners")
	}
	return a.DescribeListenersFunc(ctx, in)
}
func (a *Account) DescribeListenerCertificates(ctx context.Context, in *elasticloadbalancingv2.DescribeListenerCertificatesInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeListenerCertificatesOutput, error) {
	if err := a.called("DescribeListenerCertificates"); err != nil {
		return nil, err
	}
	if a.DescribeListenerCertificatesFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeListenerCertificates")
	}
	return a.DescribeListenerCertificatesFunc(ctx, in)
}
func (a *Account) DescribeLoadBalancers(ctx context.Context, in *elasticloadbalancingv2.DescribeLoadBalancersInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
	if err := a.called("DescribeLoadBalancers"); err != nil {
		return nil, err
	}
	if a.DescribeLoadBalancersFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeLoadBalancers")
	}
	return a.DescribeLoadBalancersFunc(ctx, in)
}
func (a *Account) PutParameter(ctx context.Context, in *ssm.PutParameterInput, _ ...func(*ssm.Options)) (*ssm.PutParameterOutput, error) {
	if err := a.called("PutParameter"); err != nil {
		return nil, err
	}
	if a.PutParameterFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call PutParameter")
	}
	return a.PutParameterFunc(ctx, in)
}
func (a *Account) DeleteParameters(ctx context.Context, in *ssm.DeleteParametersInput, _ ...func(*ssm.Options)) (*ssm.DeleteParametersOutput, error) {
	if err := a.called("DeleteParameters"); err != nil {
		return nil, err
	}
	if a.DeleteParametersFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DeleteParameters")
	}
	return a.DeleteParametersFunc(ctx, in)
}
func (a *Account) GetParametersByPath(ctx context.Context, in *ssm.GetParametersByPathInput, _ ...func(*ssm.Options)) (*ssm.GetParametersByPathOutput, error) {
	if err := a.called("GetParametersByPath"); err != nil {
		return nil, err
	}
	if a.GetParametersByPathFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call GetParametersByPath")
	}
	return a.GetParametersByPathFunc(ctx, in)
}
func (a *Account) GetRole(ctx context.Context, in *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	if err := a.called("GetRole"); err != nil {
		return nil, err
	}
	if a.GetRoleFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call GetRole")
	}
	return a.GetRoleFunc(ctx, in)
}
func (a *Account) SimulatePrincipalPolicy(ctx context.Context, in *iam.SimulatePrincipalPolicyInput, _ ...func(*iam.Options)) (*iam.SimulatePrincipalPolicyOutput, error) {
	if err := a.called("SimulatePrincipalPolicy"); err != nil {
		return nil, err
	}
	if a.SimulatePrincipalPolicyFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call SimulatePrincipalPolicy")
	}
	return a.SimulatePrincipalPolicyFunc(ctx, in)
}
func (a *Account) DescribeCertificate(ctx context.Context, in *acm.DescribeCertificateInput, _ ...func(*acm.Options)) (*acm.DescribeCertificateOutput, error) {
	if err := a.called("DescribeCertificate"); err != nil {
		return nil, err
	}
	if a.DescribeCertificateFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeCertificate")
	}
	return a.DescribeCertificateFunc(ctx, in)
}

func (a *Account) AuthorizeSecurityGroupIngress(ctx context.Context, in *ec2.AuthorizeSecurityGroupIngressInput, _ ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupIngressOutput, error) {
	if err := a.called("AuthorizeSecurityGroupIngress"); err != nil {
		return nil, err
	}
	if a.AuthorizeSecurityGroupIngressFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call AuthorizeSecurityGroupIngress")
	}
	return a.AuthorizeSecurityGroupIngressFunc(ctx, in)
}

func (a *Account) RevokeSecurityGroupIngress(ctx context.Context, in *ec2.RevokeSecurityGroupIngressInput, _ ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupIngressOutput, error) {
	if err := a.called("RevokeSecurityGroupIngress"); err != nil {
		return nil, err
	}
	if a.RevokeSecurityGroupIngressFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call RevokeSecurityGroupIngress")
	}
	return a.RevokeSecurityGroupIngressFunc(ctx, in)
}

func (a *Account) DeleteSecurityGroup(ctx context.Context, in *ec2.DeleteSecurityGroupInput, _ ...func(*ec2.Options)) (*ec2.DeleteSecurityGroupOutput, error) {
	if err := a.called("DeleteSecurityGroup"); err != nil {
		return nil, err
	}
	if a.DeleteSecurityGroupFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DeleteSecurityGroup")
	}
	return a.DeleteSecurityGroupFunc(ctx, in)
}

func (a *Account) CreateService(ctx context.Context, in *ecs.CreateServiceInput, _ ...func(*ecs.Options)) (*ecs.CreateServiceOutput, error) {
	if err := a.called("CreateService"); err != nil {
		return nil, err
	}
	if a.CreateServiceFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call CreateService")
	}
	return a.CreateServiceFunc(ctx, in)
}

func (a *Account) UpdateService(ctx context.Context, in *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
	if err := a.called("UpdateService"); err != nil {
		return nil, err
	}
	if a.UpdateServiceFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call UpdateService")
	}
	return a.UpdateServiceFunc(ctx, in)
}

func (a *Account) DeleteService(ctx context.Context, in *ecs.DeleteServiceInput, _ ...func(*ecs.Options)) (*ecs.DeleteServiceOutput, error) {
	if err := a.called("DeleteService"); err != nil {
		return nil, err
	}
	if a.DeleteServiceFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DeleteService")
	}
	return a.DeleteServiceFunc(ctx, in)
}

func (a *Account) DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	if err := a.called("DescribeServices"); err != nil {
		return nil, err
	}
	if a.DescribeServicesFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeServices")
	}
	return a.DescribeServicesFunc(ctx, in)
}

func (a *Account) ListTasks(ctx context.Context, in *ecs.ListTasksInput, _ ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	if err := a.called("ListTasks"); err != nil {
		return nil, err
	}
	if a.ListTasksFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call ListTasks")
	}
	return a.ListTasksFunc(ctx, in)
}

func (a *Account) DescribeTaskDefinition(ctx context.Context, in *ecs.DescribeTaskDefinitionInput, _ ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error) {
	if err := a.called("DescribeTaskDefinition"); err != nil {
		return nil, err
	}
	if a.DescribeTaskDefinitionFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeTaskDefinition")
	}
	return a.DescribeTaskDefinitionFunc(ctx, in)
}

func (a *Account) CreateTargetGroup(ctx context.Context, in *elasticloadbalancingv2.CreateTargetGroupInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.CreateTargetGroupOutput, error) {
	if err := a.called("CreateTargetGroup"); err != nil {
		return nil, err
	}
	if a.CreateTargetGroupFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call CreateTargetGroup")
	}
	return a.CreateTargetGroupFunc(ctx, in)
}

func (a *Account) DescribeTargetGroups(ctx context.Context, in *elasticloadbalancingv2.DescribeTargetGroupsInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error) {
	if err := a.called("DescribeTargetGroups"); err != nil {
		return nil, err
	}
	if a.DescribeTargetGroupsFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeTargetGroups")
	}
	return a.DescribeTargetGroupsFunc(ctx, in)
}

func (a *Account) DeleteTargetGroup(ctx context.Context, in *elasticloadbalancingv2.DeleteTargetGroupInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DeleteTargetGroupOutput, error) {
	if err := a.called("DeleteTargetGroup"); err != nil {
		return nil, err
	}
	if a.DeleteTargetGroupFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DeleteTargetGroup")
	}
	return a.DeleteTargetGroupFunc(ctx, in)
}

func (a *Account) CreateRule(ctx context.Context, in *elasticloadbalancingv2.CreateRuleInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.CreateRuleOutput, error) {
	if err := a.called("CreateRule"); err != nil {
		return nil, err
	}
	if a.CreateRuleFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call CreateRule")
	}
	return a.CreateRuleFunc(ctx, in)
}

func (a *Account) DescribeRules(ctx context.Context, in *elasticloadbalancingv2.DescribeRulesInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeRulesOutput, error) {
	if err := a.called("DescribeRules"); err != nil {
		return nil, err
	}
	if a.DescribeRulesFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeRules")
	}
	return a.DescribeRulesFunc(ctx, in)
}

func (a *Account) DeleteRule(ctx context.Context, in *elasticloadbalancingv2.DeleteRuleInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DeleteRuleOutput, error) {
	if err := a.called("DeleteRule"); err != nil {
		return nil, err
	}
	if a.DeleteRuleFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DeleteRule")
	}
	return a.DeleteRuleFunc(ctx, in)
}

func (a *Account) DescribeTags(ctx context.Context, in *elasticloadbalancingv2.DescribeTagsInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTagsOutput, error) {
	if err := a.called("DescribeTags"); err != nil {
		return nil, err
	}
	if a.DescribeTagsFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeTags")
	}
	return a.DescribeTagsFunc(ctx, in)
}

func (a *Account) DescribeTargetHealth(ctx context.Context, in *elasticloadbalancingv2.DescribeTargetHealthInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error) {
	if err := a.called("DescribeTargetHealth"); err != nil {
		return nil, err
	}
	if a.DescribeTargetHealthFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeTargetHealth")
	}
	return a.DescribeTargetHealthFunc(ctx, in)
}

func (a *Account) CreateDBInstance(ctx context.Context, in *rds.CreateDBInstanceInput, _ ...func(*rds.Options)) (*rds.CreateDBInstanceOutput, error) {
	if err := a.called("CreateDBInstance"); err != nil {
		return nil, err
	}
	if a.CreateDBInstanceFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call CreateDBInstance")
	}
	return a.CreateDBInstanceFunc(ctx, in)
}

func (a *Account) DescribeDBInstances(ctx context.Context, in *rds.DescribeDBInstancesInput, _ ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	if err := a.called("DescribeDBInstances"); err != nil {
		return nil, err
	}
	if a.DescribeDBInstancesFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeDBInstances")
	}
	return a.DescribeDBInstancesFunc(ctx, in)
}

func (a *Account) DeleteDBInstance(ctx context.Context, in *rds.DeleteDBInstanceInput, _ ...func(*rds.Options)) (*rds.DeleteDBInstanceOutput, error) {
	if err := a.called("DeleteDBInstance"); err != nil {
		return nil, err
	}
	if a.DeleteDBInstanceFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DeleteDBInstance")
	}
	return a.DeleteDBInstanceFunc(ctx, in)
}

func (a *Account) CreateDBSubnetGroup(ctx context.Context, in *rds.CreateDBSubnetGroupInput, _ ...func(*rds.Options)) (*rds.CreateDBSubnetGroupOutput, error) {
	if err := a.called("CreateDBSubnetGroup"); err != nil {
		return nil, err
	}
	if a.CreateDBSubnetGroupFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call CreateDBSubnetGroup")
	}
	return a.CreateDBSubnetGroupFunc(ctx, in)
}

func (a *Account) DescribeDBSubnetGroups(ctx context.Context, in *rds.DescribeDBSubnetGroupsInput, _ ...func(*rds.Options)) (*rds.DescribeDBSubnetGroupsOutput, error) {
	if err := a.called("DescribeDBSubnetGroups"); err != nil {
		return nil, err
	}
	if a.DescribeDBSubnetGroupsFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeDBSubnetGroups")
	}
	return a.DescribeDBSubnetGroupsFunc(ctx, in)
}

func (a *Account) DeleteDBSubnetGroup(ctx context.Context, in *rds.DeleteDBSubnetGroupInput, _ ...func(*rds.Options)) (*rds.DeleteDBSubnetGroupOutput, error) {
	if err := a.called("DeleteDBSubnetGroup"); err != nil {
		return nil, err
	}
	if a.DeleteDBSubnetGroupFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DeleteDBSubnetGroup")
	}
	return a.DeleteDBSubnetGroupFunc(ctx, in)
}

func (a rdsAPI) ListTagsForResource(ctx context.Context, in *rds.ListTagsForResourceInput, _ ...func(*rds.Options)) (*rds.ListTagsForResourceOutput, error) {
	if err := a.called("ListTagsForResource"); err != nil {
		return nil, err
	}
	if a.RDSListTagsForResourceFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call ListTagsForResource")
	}
	return a.RDSListTagsForResourceFunc(ctx, in)
}

func (a *Account) DescribeDBSnapshots(ctx context.Context, in *rds.DescribeDBSnapshotsInput, _ ...func(*rds.Options)) (*rds.DescribeDBSnapshotsOutput, error) {
	if err := a.called("DescribeDBSnapshots"); err != nil {
		return nil, err
	}
	if a.DescribeDBSnapshotsFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeDBSnapshots")
	}
	return a.DescribeDBSnapshotsFunc(ctx, in)
}

func (a *Account) CreateRepository(ctx context.Context, in *ecr.CreateRepositoryInput, _ ...func(*ecr.Options)) (*ecr.CreateRepositoryOutput, error) {
	if err := a.called("CreateRepository"); err != nil {
		return nil, err
	}
	if a.CreateRepositoryFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call CreateRepository")
	}
	return a.CreateRepositoryFunc(ctx, in)
}

func (a *Account) DescribeRepositories(ctx context.Context, in *ecr.DescribeRepositoriesInput, _ ...func(*ecr.Options)) (*ecr.DescribeRepositoriesOutput, error) {
	if err := a.called("DescribeRepositories"); err != nil {
		return nil, err
	}
	if a.DescribeRepositoriesFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeRepositories")
	}
	return a.DescribeRepositoriesFunc(ctx, in)
}

func (a *Account) DeleteRepository(ctx context.Context, in *ecr.DeleteRepositoryInput, _ ...func(*ecr.Options)) (*ecr.DeleteRepositoryOutput, error) {
	if err := a.called("DeleteRepository"); err != nil {
		return nil, err
	}
	if a.DeleteRepositoryFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DeleteRepository")
	}
	return a.DeleteRepositoryFunc(ctx, in)
}

func (a *Account) GetAuthorizationToken(ctx context.Context, in *ecr.GetAuthorizationTokenInput, _ ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
	if err := a.called("GetAuthorizationToken"); err != nil {
		return nil, err
	}
	if a.GetAuthorizationTokenFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call GetAuthorizationToken")
	}
	return a.GetAuthorizationTokenFunc(ctx, in)
}

func (a ecrAPI) ListTagsForResource(ctx context.Context, in *ecr.ListTagsForResourceInput, _ ...func(*ecr.Options)) (*ecr.ListTagsForResourceOutput, error) {
	if err := a.called("ListTagsForResource"); err != nil {
		return nil, err
	}
	if a.ECRListTagsForResourceFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call ListTagsForResource")
	}
	return a.ECRListTagsForResourceFunc(ctx, in)
}

func (a *Account) CreateLogGroup(ctx context.Context, in *cloudwatchlogs.CreateLogGroupInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogGroupOutput, error) {
	if err := a.called("CreateLogGroup"); err != nil {
		return nil, err
	}
	if a.CreateLogGroupFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call CreateLogGroup")
	}
	return a.CreateLogGroupFunc(ctx, in)
}

func (a *Account) DescribeLogGroups(ctx context.Context, in *cloudwatchlogs.DescribeLogGroupsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
	if err := a.called("DescribeLogGroups"); err != nil {
		return nil, err
	}
	if a.DescribeLogGroupsFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DescribeLogGroups")
	}
	return a.DescribeLogGroupsFunc(ctx, in)
}

func (a *Account) PutRetentionPolicy(ctx context.Context, in *cloudwatchlogs.PutRetentionPolicyInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutRetentionPolicyOutput, error) {
	if err := a.called("PutRetentionPolicy"); err != nil {
		return nil, err
	}
	if a.PutRetentionPolicyFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call PutRetentionPolicy")
	}
	return a.PutRetentionPolicyFunc(ctx, in)
}

func (a *Account) DeleteLogGroup(ctx context.Context, in *cloudwatchlogs.DeleteLogGroupInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DeleteLogGroupOutput, error) {
	if err := a.called("DeleteLogGroup"); err != nil {
		return nil, err
	}
	if a.DeleteLogGroupFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call DeleteLogGroup")
	}
	return a.DeleteLogGroupFunc(ctx, in)
}

func (a logsAPI) ListTagsForResource(ctx context.Context, in *cloudwatchlogs.ListTagsForResourceInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.ListTagsForResourceOutput, error) {
	if err := a.called("ListTagsForResource"); err != nil {
		return nil, err
	}
	if a.LogsListTagsForResourceFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call ListTagsForResource")
	}
	return a.LogsListTagsForResourceFunc(ctx, in)
}

func (a *Account) GetResources(ctx context.Context, in *resourcegroupstaggingapi.GetResourcesInput, _ ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
	if err := a.called("GetResources"); err != nil {
		return nil, err
	}
	if a.GetResourcesFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call GetResources")
	}
	return a.GetResourcesFunc(ctx, in)
}

func (a *Account) GetProducts(ctx context.Context, in *pricing.GetProductsInput, _ ...func(*pricing.Options)) (*pricing.GetProductsOutput, error) {
	if err := a.called("GetProducts"); err != nil {
		return nil, err
	}
	if a.GetProductsFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call GetProducts")
	}
	return a.GetProductsFunc(ctx, in)
}

func (a *Account) GetParameters(ctx context.Context, in *ssm.GetParametersInput, _ ...func(*ssm.Options)) (*ssm.GetParametersOutput, error) {
	if err := a.called("GetParameters"); err != nil {
		return nil, err
	}
	if a.GetParametersFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call GetParameters")
	}
	return a.GetParametersFunc(ctx, in)
}

func (a ssmAPI) ListTagsForResource(ctx context.Context, in *ssm.ListTagsForResourceInput, _ ...func(*ssm.Options)) (*ssm.ListTagsForResourceOutput, error) {
	if err := a.called("ListTagsForResource"); err != nil {
		return nil, err
	}
	if a.SSMListTagsForResourceFunc == nil {
		return nil, fmt.Errorf("unconfigured AWS call ListTagsForResource")
	}
	return a.SSMListTagsForResourceFunc(ctx, in)
}

type rdsAPI struct{ *Account }

type ecrAPI struct{ *Account }

type logsAPI struct{ *Account }

type ssmAPI struct{ *Account }
