package awsecs

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/flokkit/apptown/internal/deploy"
)

func (d *Driver) checkJobNetwork(ctx context.Context, a *API, p deploy.Placement, id string) error {
	out, err := a.EC2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{id}})
	if err != nil {
		return err
	}
	if len(out.SecurityGroups) == 1 && aws.ToString(out.SecurityGroups[0].GroupId) == id {
		tags := map[string]string{}
		for _, tag := range out.SecurityGroups[0].Tags {
			tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
		if tags["apptown:instance"] == d.Installation && tags["apptown:deployment"] == p.ID.String() {
			return nil
		}
	}
	return fmt.Errorf("security group %s is not this installation's recorded deployment network; nothing was started", id)
}
