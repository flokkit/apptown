package awsecs_test

import (
	"context"
	"slices"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/awsecs"
	"github.com/flokkit/apptown/internal/deploy/awsecs/awsfake"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// The listener is shared with another team and another installation. AppTown
// adds its rule at a priority nobody holds, on the named listener only, and
// deletes a rule only when it is on the deployment's record and still carries
// this installation's and deployment's tags.
func TestListenerRulesTakeAFreePriorityAndNeverTouchAnotherRule(t *testing.T) {
	const listener = "arn:aws:elasticloadbalancing:r:1:listener/app/shared/lb/l1"
	p := deploy.Placement{ID: idstest.N(31)}
	ours := []elbtypes.Tag{{Key: aws.String("apptown:instance"), Value: aws.String("installation")}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}
	foreign := []elbtypes.Tag{{Key: aws.String("apptown:instance"), Value: aws.String("another-installation")}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}
	rules := map[string][]elbtypes.Tag{"rule/team-a": nil, "rule/team-b": foreign, "target": ours}
	a := &awsfake.Account{}
	a.DescribeRulesFunc = func(_ context.Context, in *elasticloadbalancingv2.DescribeRulesInput) (*elasticloadbalancingv2.DescribeRulesOutput, error) {
		if aws.ToString(in.ListenerArn) != listener {
			t.Fatalf("read another listener %s", aws.ToString(in.ListenerArn))
		}
		if in.Marker == nil {
			return &elasticloadbalancingv2.DescribeRulesOutput{Rules: []elbtypes.Rule{{Priority: aws.String("1"), RuleArn: aws.String("rule/team-a")}, {Priority: aws.String("default")}}, NextMarker: aws.String("page-2")}, nil
		}
		return &elasticloadbalancingv2.DescribeRulesOutput{Rules: []elbtypes.Rule{{Priority: aws.String("2"), RuleArn: aws.String("rule/team-b")}, {Priority: aws.String("4")}}}, nil
	}
	created := ""
	a.CreateRuleFunc = func(_ context.Context, in *elasticloadbalancingv2.CreateRuleInput) (*elasticloadbalancingv2.CreateRuleOutput, error) {
		if aws.ToString(in.ListenerArn) != listener || aws.ToInt32(in.Priority) != 3 {
			t.Fatalf("rule on %s at %d", aws.ToString(in.ListenerArn), aws.ToInt32(in.Priority))
		}
		created = "rule/apptown-" + strconv.Itoa(int(aws.ToInt32(in.Priority)))
		rules[created] = in.Tags
		return &elasticloadbalancingv2.CreateRuleOutput{Rules: []elbtypes.Rule{{RuleArn: aws.String(created)}}}, nil
	}
	a.DescribeTagsFunc = func(_ context.Context, in *elasticloadbalancingv2.DescribeTagsInput) (*elasticloadbalancingv2.DescribeTagsOutput, error) {
		arn := in.ResourceArns[0]
		return &elasticloadbalancingv2.DescribeTagsOutput{TagDescriptions: []elbtypes.TagDescription{{ResourceArn: aws.String(arn), Tags: rules[arn]}}}, nil
	}
	var deleted []string
	a.DeleteRuleFunc = func(_ context.Context, in *elasticloadbalancingv2.DeleteRuleInput) (*elasticloadbalancingv2.DeleteRuleOutput, error) {
		deleted = append(deleted, aws.ToString(in.RuleArn))
		return &elasticloadbalancingv2.DeleteRuleOutput{}, nil
	}
	owner := &jobOwner{items: []deploy.Owned{{Kind: "target-group", ID: "target"}}}
	d := awsecs.NewDriver(awsecs.Dependencies{Installation: "installation", Owner: owner, Owned: func(context.Context) ([]deploy.Owned, error) { return owner.items, nil }, Client: func(context.Context) (*awsecs.API, awsecs.Config, awsecs.Estate, error) {
		return a.API(), awsecs.Config{Cluster: "cluster", Listener: listener, Domain: "apps.example"}, awsecs.Estate{}, nil
	}})
	c := awsecs.Config{Cluster: "cluster", Listener: listener, Domain: "apps.example"}
	_, rule, err := awsecs.EnsureRoute(d, t.Context(), a.API(), c, awsecs.Estate{}, p, "sg", appcontract.Part{Name: "web", Public: true, Port: 8080})
	if err != nil || rule != created {
		t.Fatalf("rule %q: %v", rule, err)
	}
	if err := d.RemoveRoute(t.Context(), p); err != nil || !slices.Equal(deleted, []string{created}) {
		t.Fatalf("removed %v: %v", deleted, err)
	}

	// A record that names a rule now carrying another installation's tags
	// (a recreated ARN, or a record written in error) is refused untouched.
	for _, arn := range []string{"rule/team-a", "rule/team-b"} {
		deleted = nil
		owner.items = []deploy.Owned{{Kind: "listener-rule", ID: arn}}
		if err := d.RemoveRoute(t.Context(), p); err == nil {
			t.Fatalf("removed route through %s", arn)
		}
		if _, err := d.Teardown(t.Context(), p, 1, deploy.TakeDown{Owned: owner.items, BackedUp: true}); err == nil {
			t.Fatalf("took down %s", arn)
		}
		if len(deleted) != 0 {
			t.Fatalf("deleted a rule AppTown did not create: %v", deleted)
		}
	}
}
