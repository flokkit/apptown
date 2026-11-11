package stock

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

type awsStatement struct {
	Sid       string
	Effect    string
	Action    []string
	Resource  any
	Condition map[string]map[string]any
}
type awsPolicy struct {
	Version   string
	Statement []awsStatement
}

// Both administrator templates must grant the same authority. In particular,
// create-time tagging must never grant a way to acquire a foreign resource.
func TestAWSSetupTemplatesKeepTheSameOwnershipBoundary(t *testing.T) {
	var stack struct {
		Resources map[string]struct {
			Type       string
			Properties map[string]any
		}
	}
	raw, err := os.ReadFile("docs/guides/aws-target-setup.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &stack); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"AWS::IAM::User": true, "AWS::IAM::Role": true, "AWS::EC2::SecurityGroup": true, "AWS::ElasticLoadBalancingV2::LoadBalancer": true, "AWS::ElasticLoadBalancingV2::Listener": true}
	for name, r := range stack.Resources {
		if !allowed[r.Type] {
			t.Errorf("setup changes estate resource %s (%s)", name, r.Type)
		}
	}
	policies := stack.Resources["User"].Properties["Policies"].([]any)
	sub := policies[0].(map[string]any)["PolicyDocument"].(map[string]any)["Fn::Sub"].([]any)
	cf := sub[0].(string)
	terraform, err := os.ReadFile("docs/guides/aws-target-setup.tf")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(terraform), "policy = <<-POLICY\n")
	if !ok {
		t.Fatal("missing Terraform policy")
	}
	tf, _, ok := strings.Cut(rest, "\nPOLICY")
	if !ok {
		t.Fatal("unterminated policy")
	}
	for from, to := range map[string]string{
		"AWS::Region": "data.aws_region.current.region", "AWS::AccountId": "data.aws_caller_identity.current.account_id", "InstallationPrefix": "local.installation_prefix", "InstallationId": "var.installation_id", "VpcId": "var.vpc_id", "ClusterName": "var.cluster_name", "ExecutionRoleArn": "aws_iam_role.execution.arn", "EffectiveListenerArn": "local.listener_arn", "RulePrefix": "local.rule_prefix",
	} {
		tf = strings.ReplaceAll(tf, "${"+to+"}", "${"+from+"}")
	}
	var a, b awsPolicy
	if err := json.Unmarshal([]byte(cf), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(tf), &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("CloudFormation and Terraform grant different authority")
	}
	by := map[string]awsStatement{}
	for _, s := range a.Statement {
		if s.Effect != "Allow" {
			t.Errorf("unexpected effect %s", s.Effect)
		}
		by[s.Sid] = s
		for _, action := range s.Action {
			if strings.HasPrefix(action, "iam:") && action != "iam:PassRole" && action != "iam:GetRole" && action != "iam:SimulatePrincipalPolicy" {
				t.Errorf("AppTown has IAM authority %s", action)
			}
			for _, forbidden := range []string{"s3:", "efs:", "servicediscovery:", "route53:", "ec2:CreateVpc", "ec2:CreateSubnet", "ecs:CreateCluster", "elasticloadbalancing:ModifyListener", "elasticloadbalancing:DeleteListener", "elasticloadbalancing:DeleteLoadBalancer"} {
				if strings.HasPrefix(action, forbidden) {
					t.Errorf("out-of-scope authority %s", action)
				}
			}
		}
	}
	for _, sid := range []string{"SecurityGroupsOwn", "ServicesOwn", "TasksStopOwn", "ListenerRulesOwn"} {
		if by[sid].Condition["StringEquals"]["aws:ResourceTag/apptown:instance"] != "${InstallationId}" {
			t.Errorf("%s lets a foreign resource change", sid)
		}
	}
	for sid, key := range map[string]string{"SecurityGroupsTagOnCreate": "ec2:CreateAction", "EcsTagOnCreate": "ecs:CreateAction", "LoadBalancerTagOnCreate": "elasticloadbalancing:CreateAction"} {
		if by[sid].Condition["StringEquals"][key] == nil {
			t.Errorf("%s permits standalone retagging", sid)
		}
	}
	vpc := by["SecurityGroupsInVpc"]
	if len(vpc.Condition) != 0 || vpc.Resource != "arn:aws:ec2:${AWS::Region}:${AWS::AccountId}:vpc/${VpcId}" {
		t.Fatal("existing VPC must have its own grant without a request-tag condition")
	}
	if by["SecurityGroupsCreate"].Condition["StringEquals"]["aws:RequestTag/apptown:instance"] != "${InstallationId}" {
		t.Fatal("new group must carry this installation's tag")
	}
	if by["TasksRun"].Condition["StringEquals"]["ecs:cluster"] != "arn:aws:ecs:${AWS::Region}:${AWS::AccountId}:cluster/${ClusterName}" {
		t.Fatal("tasks escape the named cluster")
	}
	if by["ListenerRulesOwn"].Resource != "${RulePrefix}" {
		t.Fatal("rules escape the named listener")
	}
}
