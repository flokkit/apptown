package deployment

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"github.com/flokkit/apptown/internal/deploy/awsecs"
	"github.com/flokkit/apptown/internal/deploy/awsecs/awsfake"
)

// awsEstate is an account that starts empty below the operator's estate
// (cluster, listener, role) and remembers what the driver makes, so a
// release reads back only what an earlier call created.
type awsEstate struct {
	mu          sync.Mutex
	cfg         awsecs.Config
	n           int
	tags        map[string]map[string]string
	params      map[string]string
	definitions map[string]*ecs.RegisterTaskDefinitionInput
	services    map[string]*ecstypes.Service
}

func newAWSEstate() (*awsEstate, *awsfake.Account) {
	e := &awsEstate{cfg: awsecs.Config{Region: "ap-southeast-2", Account: "222222222222", Cluster: "existing", Listener: "arn:aws:elasticloadbalancing:ap-southeast-2:222222222222:listener/app/existing/id/listener", ExecutionRole: "arn:aws:iam::222222222222:role/execution", Domain: "apps.example.com", TaskSubnets: []string{"subnet-a", "subnet-b"}},
		tags: map[string]map[string]string{}, params: map[string]string{}, definitions: map[string]*ecs.RegisterTaskDefinitionInput{}, services: map[string]*ecstypes.Service{}}
	a := &awsfake.Account{}
	cfg := e.cfg
	// The operator's estate, as attaching checks it.
	a.GetCallerIdentityFunc = func(context.Context, *sts.GetCallerIdentityInput) (*sts.GetCallerIdentityOutput, error) {
		return &sts.GetCallerIdentityOutput{Account: aws.String(cfg.Account)}, nil
	}
	a.DescribeRegionsFunc = func(context.Context, *ec2.DescribeRegionsInput) (*ec2.DescribeRegionsOutput, error) {
		return &ec2.DescribeRegionsOutput{Regions: []ec2types.Region{{RegionName: aws.String(cfg.Region), OptInStatus: aws.String("opted-in")}}}, nil
	}
	a.DescribeClustersFunc = func(context.Context, *ecs.DescribeClustersInput) (*ecs.DescribeClustersOutput, error) {
		return &ecs.DescribeClustersOutput{Clusters: []ecstypes.Cluster{{Status: aws.String("ACTIVE"), CapacityProviders: []string{"FARGATE"}}}}, nil
	}
	a.DescribeSubnetsFunc = func(_ context.Context, in *ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error) {
		out := &ec2.DescribeSubnetsOutput{}
		for i, id := range in.SubnetIds {
			out.Subnets = append(out.Subnets, ec2types.Subnet{SubnetId: aws.String(id), VpcId: aws.String("vpc-existing"), AvailabilityZone: aws.String(fmt.Sprintf("zone-%d", i))})
		}
		return out, nil
	}
	a.DescribeRouteTablesFunc = func(context.Context, *ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error) {
		return &ec2.DescribeRouteTablesOutput{RouteTables: []ec2types.RouteTable{{Routes: []ec2types.Route{{DestinationCidrBlock: aws.String("0.0.0.0/0"), GatewayId: aws.String("igw-existing"), State: ec2types.RouteStateActive}}}}}, nil
	}
	a.DescribeListenersFunc = func(context.Context, *elasticloadbalancingv2.DescribeListenersInput) (*elasticloadbalancingv2.DescribeListenersOutput, error) {
		return &elasticloadbalancingv2.DescribeListenersOutput{Listeners: []elbtypes.Listener{{Protocol: elbtypes.ProtocolEnumHttps, LoadBalancerArn: aws.String("existing-lb")}}}, nil
	}
	a.DescribeLoadBalancersFunc = func(context.Context, *elasticloadbalancingv2.DescribeLoadBalancersInput) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
		return &elasticloadbalancingv2.DescribeLoadBalancersOutput{LoadBalancers: []elbtypes.LoadBalancer{{Type: elbtypes.LoadBalancerTypeEnumApplication, Scheme: elbtypes.LoadBalancerSchemeEnumInternetFacing, VpcId: aws.String("vpc-existing"), DNSName: aws.String("existing.elb.amazonaws.com"), SecurityGroups: []string{"sg-estate"}}}}, nil
	}
	a.DescribeListenerCertificatesFunc = func(context.Context, *elasticloadbalancingv2.DescribeListenerCertificatesInput) (*elasticloadbalancingv2.DescribeListenerCertificatesOutput, error) {
		return &elasticloadbalancingv2.DescribeListenerCertificatesOutput{Certificates: []elbtypes.Certificate{{CertificateArn: aws.String("certificate")}}}, nil
	}
	a.DescribeCertificateFunc = func(context.Context, *acm.DescribeCertificateInput) (*acm.DescribeCertificateOutput, error) {
		return &acm.DescribeCertificateOutput{Certificate: &acmtypes.CertificateDetail{Status: acmtypes.CertificateStatusIssued, SubjectAlternativeNames: []string{"*.apps.example.com"}}}, nil
	}
	a.GetRoleFunc = func(context.Context, *iam.GetRoleInput) (*iam.GetRoleOutput, error) {
		return &iam.GetRoleOutput{Role: &iamtypes.Role{Arn: aws.String(cfg.ExecutionRole), AssumeRolePolicyDocument: aws.String(`{"Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"Service":"ecs-tasks.amazonaws.com"}}]}`)}}, nil
	}
	a.SimulatePrincipalPolicyFunc = func(_ context.Context, in *iam.SimulatePrincipalPolicyInput) (*iam.SimulatePrincipalPolicyOutput, error) {
		out := &iam.SimulatePrincipalPolicyOutput{}
		for _, action := range in.ActionNames {
			out.EvaluationResults = append(out.EvaluationResults, iamtypes.EvaluationResult{EvalActionName: aws.String(action), EvalDecision: iamtypes.PolicyEvaluationDecisionTypeAllowed})
		}
		return out, nil
	}
	a.GetParametersByPathFunc = func(context.Context, *ssm.GetParametersByPathInput) (*ssm.GetParametersByPathOutput, error) {
		return &ssm.GetParametersByPathOutput{}, nil
	}

	// What the driver makes, kept with its tags.
	a.CreateSecurityGroupFunc = func(_ context.Context, in *ec2.CreateSecurityGroupInput) (*ec2.CreateSecurityGroupOutput, error) {
		if aws.ToBool(in.DryRun) {
			return nil, &smithy.GenericAPIError{Code: "DryRunOperation"}
		}
		tags := map[string]string{}
		for _, t := range in.TagSpecifications[0].Tags {
			tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
		}
		return &ec2.CreateSecurityGroupOutput{GroupId: aws.String(e.make("sg-", tags))}, nil
	}
	a.DescribeSecurityGroupsFunc = func(_ context.Context, in *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error) {
		out := &ec2.DescribeSecurityGroupsOutput{}
		for _, id := range in.GroupIds {
			out.SecurityGroups = append(out.SecurityGroups, ec2types.SecurityGroup{GroupId: aws.String(id), Tags: ec2Tags(e.tagsOf(id))})
		}
		return out, nil
	}
	a.AuthorizeSecurityGroupIngressFunc = func(context.Context, *ec2.AuthorizeSecurityGroupIngressInput) (*ec2.AuthorizeSecurityGroupIngressOutput, error) {
		return &ec2.AuthorizeSecurityGroupIngressOutput{}, nil
	}
	a.CreateLogGroupFunc = func(_ context.Context, in *cloudwatchlogs.CreateLogGroupInput) (*cloudwatchlogs.CreateLogGroupOutput, error) {
		e.put("arn:aws:logs:"+cfg.Region+":"+cfg.Account+":log-group:"+aws.ToString(in.LogGroupName), in.Tags)
		return &cloudwatchlogs.CreateLogGroupOutput{}, nil
	}
	a.LogsListTagsForResourceFunc = func(_ context.Context, in *cloudwatchlogs.ListTagsForResourceInput) (*cloudwatchlogs.ListTagsForResourceOutput, error) {
		return &cloudwatchlogs.ListTagsForResourceOutput{Tags: e.tagsOf(aws.ToString(in.ResourceArn))}, nil
	}
	a.PutRetentionPolicyFunc = func(context.Context, *cloudwatchlogs.PutRetentionPolicyInput) (*cloudwatchlogs.PutRetentionPolicyOutput, error) {
		return &cloudwatchlogs.PutRetentionPolicyOutput{}, nil
	}
	a.CreateRepositoryFunc = func(_ context.Context, in *ecr.CreateRepositoryInput) (*ecr.CreateRepositoryOutput, error) {
		tags := map[string]string{}
		for _, t := range in.Tags {
			tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
		}
		return &ecr.CreateRepositoryOutput{Repository: &ecrtypes.Repository{RepositoryArn: aws.String(e.make("arn:aws:ecr:repository/", tags)), RepositoryName: in.RepositoryName}}, nil
	}
	a.ECRListTagsForResourceFunc = func(_ context.Context, in *ecr.ListTagsForResourceInput) (*ecr.ListTagsForResourceOutput, error) {
		out := &ecr.ListTagsForResourceOutput{}
		for k, v := range e.tagsOf(aws.ToString(in.ResourceArn)) {
			out.Tags = append(out.Tags, ecrtypes.Tag{Key: aws.String(k), Value: aws.String(v)})
		}
		return out, nil
	}
	a.BatchGetImageFunc = func(_ context.Context, in *ecr.BatchGetImageInput) (*ecr.BatchGetImageOutput, error) {
		return &ecr.BatchGetImageOutput{Images: []ecrtypes.Image{{ImageId: &in.ImageIds[0]}}}, nil
	}
	a.CreateTargetGroupFunc = func(_ context.Context, in *elasticloadbalancingv2.CreateTargetGroupInput) (*elasticloadbalancingv2.CreateTargetGroupOutput, error) {
		return &elasticloadbalancingv2.CreateTargetGroupOutput{TargetGroups: []elbtypes.TargetGroup{{TargetGroupArn: aws.String(e.make("arn:aws:elasticloadbalancing:targetgroup/", elbTagMap(in.Tags)))}}}, nil
	}
	a.DescribeRulesFunc = func(context.Context, *elasticloadbalancingv2.DescribeRulesInput) (*elasticloadbalancingv2.DescribeRulesOutput, error) {
		return &elasticloadbalancingv2.DescribeRulesOutput{}, nil
	}
	a.CreateRuleFunc = func(_ context.Context, in *elasticloadbalancingv2.CreateRuleInput) (*elasticloadbalancingv2.CreateRuleOutput, error) {
		return &elasticloadbalancingv2.CreateRuleOutput{Rules: []elbtypes.Rule{{RuleArn: aws.String(e.make("arn:aws:elasticloadbalancing:listener-rule/", elbTagMap(in.Tags)))}}}, nil
	}
	a.DescribeTagsFunc = func(_ context.Context, in *elasticloadbalancingv2.DescribeTagsInput) (*elasticloadbalancingv2.DescribeTagsOutput, error) {
		out := &elasticloadbalancingv2.DescribeTagsOutput{}
		for _, arn := range in.ResourceArns {
			d := elbtypes.TagDescription{ResourceArn: aws.String(arn)}
			for k, v := range e.tagsOf(arn) {
				d.Tags = append(d.Tags, elbtypes.Tag{Key: aws.String(k), Value: aws.String(v)})
			}
			out.TagDescriptions = append(out.TagDescriptions, d)
		}
		return out, nil
	}
	a.DescribeTargetHealthFunc = func(context.Context, *elasticloadbalancingv2.DescribeTargetHealthInput) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error) {
		return &elasticloadbalancingv2.DescribeTargetHealthOutput{TargetHealthDescriptions: []elbtypes.TargetHealthDescription{{TargetHealth: &elbtypes.TargetHealth{State: elbtypes.TargetHealthStateEnumHealthy}}}}, nil
	}
	a.GetParametersFunc = func(_ context.Context, in *ssm.GetParametersInput) (*ssm.GetParametersOutput, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		out := &ssm.GetParametersOutput{}
		for _, name := range in.Names {
			if v, ok := e.params[name]; ok {
				out.Parameters = append(out.Parameters, ssmtypes.Parameter{Name: aws.String(name), Value: aws.String(v)})
			} else {
				out.InvalidParameters = append(out.InvalidParameters, name)
			}
		}
		return out, nil
	}
	a.PutParameterFunc = func(_ context.Context, in *ssm.PutParameterInput) (*ssm.PutParameterOutput, error) {
		e.mu.Lock()
		e.params[aws.ToString(in.Name)] = aws.ToString(in.Value)
		e.mu.Unlock()
		tags := map[string]string{}
		for _, t := range in.Tags {
			tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
		}
		e.put(aws.ToString(in.Name), tags)
		return &ssm.PutParameterOutput{}, nil
	}
	a.SSMListTagsForResourceFunc = func(_ context.Context, in *ssm.ListTagsForResourceInput) (*ssm.ListTagsForResourceOutput, error) {
		out := &ssm.ListTagsForResourceOutput{}
		for k, v := range e.tagsOf(aws.ToString(in.ResourceId)) {
			out.TagList = append(out.TagList, ssmtypes.Tag{Key: aws.String(k), Value: aws.String(v)})
		}
		return out, nil
	}
	a.DeleteParametersFunc = func(_ context.Context, in *ssm.DeleteParametersInput) (*ssm.DeleteParametersOutput, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		for _, name := range in.Names {
			delete(e.params, name)
		}
		return &ssm.DeleteParametersOutput{DeletedParameters: in.Names}, nil
	}
	a.RegisterTaskDefinitionFunc = func(_ context.Context, in *ecs.RegisterTaskDefinitionInput) (*ecs.RegisterTaskDefinitionOutput, error) {
		tags := map[string]string{}
		for _, t := range in.Tags {
			tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
		}
		arn := e.make("arn:aws:ecs:task-definition/"+aws.ToString(in.Family)+":", tags)
		e.mu.Lock()
		e.definitions[arn] = in
		e.mu.Unlock()
		return &ecs.RegisterTaskDefinitionOutput{TaskDefinition: &ecstypes.TaskDefinition{TaskDefinitionArn: aws.String(arn)}}, nil
	}
	a.DescribeTaskDefinitionFunc = func(_ context.Context, in *ecs.DescribeTaskDefinitionInput) (*ecs.DescribeTaskDefinitionOutput, error) {
		arn := aws.ToString(in.TaskDefinition)
		return &ecs.DescribeTaskDefinitionOutput{TaskDefinition: &ecstypes.TaskDefinition{TaskDefinitionArn: aws.String(arn)}, Tags: ecsTags(e.tagsOf(arn))}, nil
	}
	a.CreateServiceFunc = func(_ context.Context, in *ecs.CreateServiceInput) (*ecs.CreateServiceOutput, error) {
		tags := map[string]string{}
		for _, t := range in.Tags {
			tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
		}
		arn := e.make("arn:aws:ecs:service/", tags)
		s := &ecstypes.Service{ServiceArn: aws.String(arn), ServiceName: in.ServiceName, Status: aws.String("ACTIVE"), DesiredCount: aws.ToInt32(in.DesiredCount), Tags: in.Tags}
		e.mu.Lock()
		e.services[arn] = s
		e.mu.Unlock()
		e.roll(s, aws.ToString(in.TaskDefinition))
		return &ecs.CreateServiceOutput{Service: s}, nil
	}
	a.UpdateServiceFunc = func(_ context.Context, in *ecs.UpdateServiceInput) (*ecs.UpdateServiceOutput, error) {
		e.mu.Lock()
		s := e.services[aws.ToString(in.Service)]
		e.mu.Unlock()
		if s == nil {
			return nil, &smithy.GenericAPIError{Code: "ServiceNotFoundException"}
		}
		s.DesiredCount = aws.ToInt32(in.DesiredCount)
		e.roll(s, aws.ToString(in.TaskDefinition))
		return &ecs.UpdateServiceOutput{Service: s}, nil
	}
	a.DescribeServicesFunc = func(_ context.Context, in *ecs.DescribeServicesInput) (*ecs.DescribeServicesOutput, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		out := &ecs.DescribeServicesOutput{}
		for _, id := range in.Services {
			for arn, s := range e.services {
				if arn == id || aws.ToString(s.ServiceName) == id {
					out.Services = append(out.Services, *s)
				}
			}
		}
		if len(out.Services) == 0 {
			out.Failures = []ecstypes.Failure{{Reason: aws.String("MISSING")}}
		}
		return out, nil
	}
	a.ListTasksFunc = func(_ context.Context, in *ecs.ListTasksInput) (*ecs.ListTasksOutput, error) {
		return &ecs.ListTasksOutput{TaskArns: []string{"task/" + aws.ToString(in.ServiceName)}}, nil
	}
	a.DescribeTasksFunc = func(_ context.Context, in *ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		out := &ecs.DescribeTasksOutput{}
		for _, task := range in.Tasks {
			for _, s := range e.services {
				if task == "task/"+aws.ToString(s.ServiceName) {
					out.Tasks = append(out.Tasks, ecstypes.Task{TaskArn: aws.String(task), LastStatus: aws.String("RUNNING"), TaskDefinitionArn: s.TaskDefinition})
				}
			}
		}
		return out, nil
	}
	return e, a
}

func (e *awsEstate) make(prefix string, tags map[string]string) string {
	e.mu.Lock()
	e.n++
	id := fmt.Sprintf("%s%d", prefix, e.n)
	e.mu.Unlock()
	e.put(id, tags)
	return id
}
func (e *awsEstate) put(id string, tags map[string]string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tags[id] = tags
}
func (e *awsEstate) tagsOf(id string) map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tags[id]
}

// roll completes a rollout at once: the new revision is primary and runs.
func (e *awsEstate) roll(s *ecstypes.Service, definition string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s.TaskDefinition = aws.String(definition)
	s.Deployments = []ecstypes.Deployment{{Id: aws.String("rollout-" + definition), Status: aws.String("PRIMARY"), TaskDefinition: aws.String(definition), RolloutState: ecstypes.DeploymentRolloutStateCompleted}}
}

func ec2Tags(m map[string]string) (out []ec2types.Tag) {
	for k, v := range m {
		out = append(out, ec2types.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	return out
}
func ecsTags(m map[string]string) (out []ecstypes.Tag) {
	for k, v := range m {
		out = append(out, ecstypes.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	return out
}
func elbTagMap(tags []elbtypes.Tag) map[string]string {
	out := map[string]string{}
	for _, t := range tags {
		out[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return out
}

// A brand-new AWS deployment's first release renders for the generation
// admission mints, applies, converges and settles; so does the next one.
// The account starts empty below the operator's estate: nothing the driver
// reads back was put there by the test.
func TestAWSFirstReleaseOfANewDeploymentRendersForTheAdmittedGeneration(t *testing.T) {
	p := newPromotionTargetsWith(t, false, "app:\n  web:\n    public: true\n    build: worker\n    ports: ['8080']\n")
	b := p.land(t)
	estate, account := newAWSEstate()
	p.s.providers.aws = awsecs.Options{
		API: func(awsecs.Config, string, string) (*awsecs.API, error) { return account.API(), nil },
		CopyImage: func(_ context.Context, repo, ref string) (string, error) {
			_, digest, _ := strings.Cut(ref, "@")
			return repo + "@" + digest, nil
		},
	}
	c := estate.cfg
	if _, err := p.s.attachAWS(t.Context(), TargetAttachment{Kind: KindAWSECS, Name: "aws", KeyIDFrom: "AWS_ACCESS_KEY_ID", SecretKeyFrom: "AWS_SECRET_ACCESS_KEY", Region: c.Region, Account: c.Account, Cluster: c.Cluster, Listener: c.Listener, ExecutionRole: c.ExecutionRole, TaskSubnets: c.TaskSubnets, Domain: c.Domain}, "fixture"); err != nil {
		t.Fatal(err)
	}
	target := p.target(t, "aws")
	if _, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, "aws", target.ID, Attributes{Feed: "manual", Audience: "public", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	for generation := int64(1); generation <= 2; generation++ {
		rel, err := p.releaseByName(t, "aws", b)
		if err != nil || rel.State != Released {
			t.Fatalf("release %d: %v %+v", generation, err, rel)
		}
		dep, err := p.s.Deployments.ByName(t.Context(), p.project, "aws")
		if err != nil {
			t.Fatal(err)
		}
		if dep.DesiredGeneration != generation || dep.ObservedGeneration != generation {
			t.Fatalf("release %d settled as desired %d observed %d", generation, dep.DesiredGeneration, dep.ObservedGeneration)
		}
		running := fmt.Sprint(dep.Handle["definition"])
		if got := estate.tagsOf(running)["apptown:generation"]; got != fmt.Sprint(generation) {
			t.Fatalf("release %d runs %s, registered for generation %q", generation, running, got)
		}
	}
}
