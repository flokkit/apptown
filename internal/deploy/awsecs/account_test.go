package awsecs_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/smithy-go"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/flokkit/apptown/internal/deploy/awsecs"
	"github.com/flokkit/apptown/internal/deploy/awsecs/awsfake"
)

// A key from another account must not even read the named estate.
func TestAccountMismatchRefusesBeforeAnyOtherCall(t *testing.T) {
	a := &awsfake.Account{GetCallerIdentityFunc: func(context.Context, *sts.GetCallerIdentityInput) (*sts.GetCallerIdentityOutput, error) {
		return &sts.GetCallerIdentityOutput{Account: aws.String("111111111111")}, nil
	}}
	cfg := awsecs.Config{Region: "ap-southeast-2", Account: "222222222222", Cluster: "existing", Listener: "listener", ExecutionRole: "role", Domain: "example.com", TaskSubnets: []string{"subnet-a", "subnet-b"}}
	_, err := awsecs.CheckAccount(t.Context(), a.API(), cfg, "installation-one")
	if err == nil || !strings.Contains(err.Error(), "111111111111, not 222222222222") {
		t.Fatalf("%v", err)
	}
	if !reflect.DeepEqual(a.Calls, []string{"GetCallerIdentity"}) {
		t.Fatalf("read wrong estate: %v", a.Calls)
	}
}

func attachedAccount() (*awsfake.Account, awsecs.Config) {
	cfg := awsecs.Config{Region: "ap-southeast-2", Account: "222222222222", Cluster: "existing", Listener: "arn:aws:elasticloadbalancing:ap-southeast-2:222222222222:listener/app/existing/id/listener", ExecutionRole: "arn:aws:iam::222222222222:role/execution", Domain: "apps.example.com", TaskSubnets: []string{"subnet-a", "subnet-b"}}
	a := &awsfake.Account{}
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
	a.GetRoleFunc = func(_ context.Context, in *iam.GetRoleInput) (*iam.GetRoleOutput, error) {
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
	a.CreateSecurityGroupFunc = func(_ context.Context, in *ec2.CreateSecurityGroupInput) (*ec2.CreateSecurityGroupOutput, error) {
		if !aws.ToBool(in.DryRun) {
			return nil, errors.New("attach tried to make a resource")
		}
		if aws.ToString(in.VpcId) != "vpc-existing" || aws.ToString(in.TagSpecifications[0].Tags[0].Value) != "installation-one" {
			return nil, errors.New("wrong installation or VPC")
		}
		return nil, &smithy.GenericAPIError{Code: "DryRunOperation"}
	}
	return a, cfg
}

// Each refusal is a concrete estate mistake; attaching must never repair it.
func TestAttachChecksExistingEstateWithoutChangingIt(t *testing.T) {
	tests := []struct {
		name, want string
		change     func(*awsfake.Account, *awsecs.Config)
	}{
		{"valid", "", func(*awsfake.Account, *awsecs.Config) {}},
		{"disabled region", "not enabled", func(a *awsfake.Account, _ *awsecs.Config) {
			a.DescribeRegionsFunc = func(context.Context, *ec2.DescribeRegionsInput) (*ec2.DescribeRegionsOutput, error) {
				return &ec2.DescribeRegionsOutput{}, nil
			}
		}},
		{"spot only", "support FARGATE", func(a *awsfake.Account, _ *awsecs.Config) {
			a.DescribeClustersFunc = func(context.Context, *ecs.DescribeClustersInput) (*ecs.DescribeClustersOutput, error) {
				return &ecs.DescribeClustersOutput{Clusters: []ecstypes.Cluster{{Status: aws.String("ACTIVE"), CapacityProviders: []string{"FARGATE_SPOT"}}}}, nil
			}
		}},
		{"mixed VPC", "span VPCs", func(a *awsfake.Account, _ *awsecs.Config) {
			prev := a.DescribeSubnetsFunc
			a.DescribeSubnetsFunc = func(ctx context.Context, in *ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error) {
				out, err := prev(ctx, in)
				out.Subnets[1].VpcId = aws.String("foreign-vpc")
				return out, err
			}
		}},
		{"missing route", "no route", func(a *awsfake.Account, _ *awsecs.Config) {
			a.DescribeRouteTablesFunc = func(context.Context, *ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error) {
				return &ec2.DescribeRouteTablesOutput{}, nil
			}
		}},
		{"mixed public private", "mix public and private", func(a *awsfake.Account, _ *awsecs.Config) {
			a.DescribeRouteTablesFunc = func(_ context.Context, in *ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error) {
				r := ec2types.Route{DestinationCidrBlock: aws.String("0.0.0.0/0"), State: ec2types.RouteStateActive}
				if in.Filters[0].Values[0] == "subnet-a" {
					r.GatewayId = aws.String("igw-existing")
				} else {
					r.NatGatewayId = aws.String("nat-existing")
				}
				return &ec2.DescribeRouteTablesOutput{RouteTables: []ec2types.RouteTable{{Routes: []ec2types.Route{r}}}}, nil
			}
		}},
		{"one database zone", "at least two zones", func(_ *awsfake.Account, c *awsecs.Config) { c.DatabaseSubnets = []string{"subnet-a"} }},
		{"HTTP listener", "must be HTTPS", func(a *awsfake.Account, _ *awsecs.Config) {
			a.DescribeListenersFunc = func(context.Context, *elasticloadbalancingv2.DescribeListenersInput) (*elasticloadbalancingv2.DescribeListenersOutput, error) {
				return &elasticloadbalancingv2.DescribeListenersOutput{Listeners: []elbtypes.Listener{{Protocol: elbtypes.ProtocolEnumHttp}}}, nil
			}
		}},
		{"internal listener", "internet-facing", func(a *awsfake.Account, _ *awsecs.Config) {
			prev := a.DescribeLoadBalancersFunc
			a.DescribeLoadBalancersFunc = func(ctx context.Context, in *elasticloadbalancingv2.DescribeLoadBalancersInput) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
				out, err := prev(ctx, in)
				out.LoadBalancers[0].Scheme = elbtypes.LoadBalancerSchemeEnumInternal
				return out, err
			}
		}},
		{"listener in another VPC", "in this VPC", func(a *awsfake.Account, _ *awsecs.Config) {
			prev := a.DescribeLoadBalancersFunc
			a.DescribeLoadBalancersFunc = func(ctx context.Context, in *elasticloadbalancingv2.DescribeLoadBalancersInput) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
				out, err := prev(ctx, in)
				out.LoadBalancers[0].VpcId = aws.String("foreign-vpc")
				return out, err
			}
		}},
		{"certificate", "no issued certificate", func(a *awsfake.Account, _ *awsecs.Config) {
			a.DescribeCertificateFunc = func(context.Context, *acm.DescribeCertificateInput) (*acm.DescribeCertificateOutput, error) {
				return &acm.DescribeCertificateOutput{Certificate: &acmtypes.CertificateDetail{Status: acmtypes.CertificateStatusIssued, SubjectAlternativeNames: []string{"*.foreign.example.com"}}}, nil
			}
		}},
		{"role trust", "does not trust", func(a *awsfake.Account, _ *awsecs.Config) {
			prev := a.GetRoleFunc
			a.GetRoleFunc = func(ctx context.Context, in *iam.GetRoleInput) (*iam.GetRoleOutput, error) {
				out, err := prev(ctx, in)
				out.Role.AssumeRolePolicyDocument = aws.String(`{"Statement":[]}`)
				return out, err
			}
		}},
		{"SSM denied", "cannot ssm:GetParameters", func(a *awsfake.Account, _ *awsecs.Config) {
			prev := a.SimulatePrincipalPolicyFunc
			a.SimulatePrincipalPolicyFunc = func(ctx context.Context, in *iam.SimulatePrincipalPolicyInput) (*iam.SimulatePrincipalPolicyOutput, error) {
				out, err := prev(ctx, in)
				for i := range out.EvaluationResults {
					if aws.ToString(out.EvaluationResults[i].EvalActionName) == "ssm:GetParameters" {
						out.EvaluationResults[i].EvalDecision = iamtypes.PolicyEvaluationDecisionTypeImplicitDeny
					}
				}
				return out, err
			}
		}},
		{"missing service role", "create-service-linked-role", func(a *awsfake.Account, _ *awsecs.Config) {
			prev := a.GetRoleFunc
			a.GetRoleFunc = func(ctx context.Context, in *iam.GetRoleInput) (*iam.GetRoleOutput, error) {
				if strings.HasPrefix(aws.ToString(in.RoleName), "AWSServiceRole") {
					return nil, errors.New("not found")
				}
				return prev(ctx, in)
			}
		}},
		{"wrong prefix", "check the key's policy", func(a *awsfake.Account, _ *awsecs.Config) {
			a.CreateSecurityGroupFunc = func(context.Context, *ec2.CreateSecurityGroupInput) (*ec2.CreateSecurityGroupOutput, error) {
				return nil, &smithy.GenericAPIError{Code: "UnauthorizedOperation"}
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, c := attachedAccount()
			tt.change(a, &c)
			got, err := awsecs.CheckAccount(t.Context(), a.API(), c, "installation-one")
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if !got.PublicIP || got.LoadBalancerDNS != "existing.elb.amazonaws.com" {
					t.Fatalf("%+v", got)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v; want %s", err, tt.want)
			}
			for _, call := range a.Calls {
				if strings.HasPrefix(call, "Create") && call != "CreateSecurityGroup" {
					t.Fatalf("attach made a resource: %s", call)
				}
			}
		})
	}
}
