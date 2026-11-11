package awsecs

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// iamPrefix maps each API field to its IAM service prefix.
var iamPrefix = map[string]string{"STS": "sts", "EC2": "ec2", "ECS": "ecs", "ELB": "elasticloadbalancing", "SSM": "ssm", "IAM": "iam", "ACM": "acm", "RDS": "rds", "ECR": "ecr", "Logs": "logs", "Tagging": "tag", "Pricing": "pricing"}

// indirectActions are granted for effects that are not an API call in this
// code: the reason is the guarantee each one serves.
var indirectActions = map[string]string{
	"iam:PassRole":                    "task definitions name the execution role",
	"ecs:TagResource":                 "tags given in RegisterTaskDefinition, CreateService and RunTask",
	"ec2:CreateTags":                  "tags given in CreateSecurityGroup",
	"elasticloadbalancing:AddTags":    "tags given in CreateTargetGroup and CreateRule",
	"logs:TagResource":                "tags given in CreateLogGroup",
	"ssm:AddTagsToResource":           "tags given in PutParameter",
	"rds:AddTagsToResource":           "tags given in CreateDBInstance and CreateDBSubnetGroup",
	"ecr:TagResource":                 "tags given in CreateRepository",
	"ecr:BatchCheckLayerAvailability": "the registry push of a release image",
	"ecr:InitiateLayerUpload":         "the registry push of a release image",
	"ecr:UploadLayerPart":             "the registry push of a release image",
	"ecr:CompleteLayerUpload":         "the registry push of a release image",
	"ecr:PutImage":                    "the registry push of a release image",
	"ecr:BatchGetImage":               "the registry digest check after a push",
	"ecr:GetDownloadUrlForLayer":      "the registry digest check after a push",
}

// calledActions reads the AWS calls the driver and its deployment owner make,
// as `<client>.<Service>.<Method>(...)` selectors.
func calledActions(t *testing.T) map[string]bool {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := filepath.Glob("../../apptown/deployment/aws_*.go")
	if err != nil || len(owner) == 0 {
		t.Fatalf("deployment owner's AWS files not found: %v", err)
	}
	called := map[string]bool{}
	for _, path := range append(files, owner...) {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			method, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			service, ok := method.X.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if prefix, ok := iamPrefix[service.Sel.Name]; ok {
				called[prefix+":"+method.Sel.Name] = true
			}
			return true
		})
	}
	return called
}

// The sample stack's user policy is the authority AppTown runs with. A call it
// does not grant fails only in a real account; a grant nothing calls is
// authority nobody needs. Both templates are held equal in stock's tests.
func TestSetupPolicyGrantsExactlyTheCallsTheDriverMakes(t *testing.T) {
	raw, err := os.ReadFile("../../../stock/docs/guides/aws-target-setup.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var stack struct {
		Resources map[string]struct{ Properties map[string]any }
	}
	if err := json.Unmarshal(raw, &stack); err != nil {
		t.Fatal(err)
	}
	doc := stack.Resources["User"].Properties["Policies"].([]any)[0].(map[string]any)["PolicyDocument"].(map[string]any)["Fn::Sub"].([]any)[0].(string)
	var policy struct{ Statement []struct{ Action []string } }
	if err := json.Unmarshal([]byte(doc), &policy); err != nil {
		t.Fatal(err)
	}
	var granted []string
	for _, s := range policy.Statement {
		granted = append(granted, s.Action...)
	}
	grants := func(action string) bool {
		return slices.ContainsFunc(granted, func(g string) bool {
			if prefix, ok := strings.CutSuffix(g, "*"); ok {
				return strings.HasPrefix(action, prefix)
			}
			return g == action
		})
	}
	called := calledActions(t)
	if len(called) < 20 {
		t.Fatalf("read only %d AWS calls; the source walk is broken", len(called))
	}
	for action := range called {
		if !grants(action) {
			t.Errorf("the driver calls %s, which the setup policy does not grant", action)
		}
	}
	for _, g := range granted {
		used := indirectActions[g] != ""
		for action := range called {
			if prefix, ok := strings.CutSuffix(g, "*"); ok && strings.HasPrefix(action, prefix) || g == action {
				used = true
			}
		}
		if !used {
			t.Errorf("the setup policy grants %s, which nothing calls", g)
		}
	}
}
