package awsecs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

type releasePlan struct {
	Generation                        int64
	Definition                        ecs.RegisterTaskDefinitionInput
	Init                              []ecs.RegisterTaskDefinitionInput
	Network                           *ecstypes.NetworkConfiguration
	Target, Rule, URL, Release, Ready string
	Static                            bool
}

func (d *Driver) Render(ctx context.Context, p deploy.Placement, src deploy.Source, bindings deploy.NeedBindings, secrets deploy.ConsumerSecrets, log deploy.Logf) (deploy.Artifact, error) {
	if err := d.Preflight(ctx, p, src, secrets, log); err != nil {
		return deploy.Artifact{}, err
	}
	if p.Next < 1 {
		return deploy.Artifact{}, errors.New("AWS render needs the generation the release will be admitted as")
	}
	a, c, e, err := d.Client(ctx)
	if err != nil {
		return deploy.Artifact{}, err
	}
	group, logs, err := d.ensureBase(ctx, a, c, e, p)
	if err != nil {
		return deploy.Artifact{}, err
	}
	connections := map[string]string{}
	for _, b := range bindings {
		maps.Copy(connections, b.Env)
		maps.Copy(connections, b.Secret)
	}
	selected, err := appcontract.PrepareWired(src.Contract, connections)
	if err != nil {
		return deploy.Artifact{}, err
	}
	if err := deploy.RefuseNeedSecretCollisions(selected, slices.Sorted(maps.Keys(connections)), secrets); err != nil {
		return deploy.Artifact{}, err
	}
	pl := releasePlan{Generation: p.Next, Network: network(c, e, group)}
	part := selected.Parts[0]
	pl.Static = part.IsStatic()
	pl.Release = src.Contract.PublicRelease(src.Revision)
	if part.Public && part.Port > 0 {
		pl.URL = "https://" + d.name(p) + "." + c.Domain
		pl.Target, pl.Rule, err = d.ensureRoute(ctx, a, c, e, p, group, part)
		if err != nil {
			return deploy.Artifact{}, err
		}
	}
	hp, _ := partHealth(part)
	if hp != nil {
		pl.Ready = part.Intent.Readiness.HTTP
	}
	definition := func(name, ref string, command []string, env map[string]string, size taskSize) (ecs.RegisterTaskDefinitionInput, error) {
		image, err := d.CopyImage(ctx, p, ref)
		if err != nil {
			return ecs.RegisterTaskDefinitionInput{}, err
		}
		values := maps.Clone(env)
		if values == nil {
			values = map[string]string{}
		}
		values["APPTOWN_INSTANCE"] = d.Installation
		values[appcontract.ReleaseEnvVar] = pl.Release
		values["APPTOWN_APP_URL"] = pl.URL
		if part.Public {
			values["APPTOWN_ORIGINS"] = pl.URL
		}
		if part.IsStatic() && src.Revision != "" {
			values[appcontract.ReleaseEnvVar] = appcontract.ReleaseIdentity(part.Name, src.Revision)
		}
		private := map[string]string{}
		if secrets != nil {
			maps.Copy(private, secrets.EnvFor(name))
		}
		maps.Copy(private, connections)
		for k, v := range values {
			for _, secret := range private {
				if secret != "" && strings.Contains(v, secret) {
					private[k] = v
					delete(values, k)
					break
				}
			}
		}
		container := ecstypes.ContainerDefinition{Name: aws.String("app"), Image: aws.String(image), Essential: aws.Bool(true), Command: command, LogConfiguration: &ecstypes.LogConfiguration{LogDriver: ecstypes.LogDriverAwslogs, Options: map[string]string{"awslogs-group": logs, "awslogs-region": c.Region, "awslogs-stream-prefix": name}}}
		for _, k := range slices.Sorted(maps.Keys(values)) {
			if _, ok := private[k]; !ok {
				container.Environment = append(container.Environment, ecstypes.KeyValuePair{Name: aws.String(k), Value: aws.String(values[k])})
			}
		}
		for _, k := range slices.Sorted(maps.Keys(private)) {
			if len(private[k]) > 4096 {
				return ecs.RegisterTaskDefinitionInput{}, fmt.Errorf("secret %s exceeds SSM's 4 KB limit", k)
			}
			path := d.path(p) + "/g" + strconv.FormatInt(p.Next, 10) + "/" + name + "/" + k
			got, err := a.SSM.GetParameters(ctx, &ssm.GetParametersInput{Names: []string{path}, WithDecryption: aws.Bool(true)})
			if err != nil {
				return ecs.RegisterTaskDefinitionInput{}, err
			}
			if len(got.Parameters) == 0 {
				_, err = a.SSM.PutParameter(ctx, &ssm.PutParameterInput{Name: aws.String(path), Value: aws.String(private[k]), Type: ssmtypes.ParameterTypeSecureString, Tags: []ssmtypes.Tag{{Key: aws.String("apptown:instance"), Value: aws.String(d.Installation)}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}})
				if err != nil {
					return ecs.RegisterTaskDefinitionInput{}, err
				}
			} else if len(got.Parameters) != 1 || aws.ToString(got.Parameters[0].Name) != path || aws.ToString(got.Parameters[0].Value) != private[k] {
				return ecs.RegisterTaskDefinitionInput{}, errors.New("AWS generation already has different secret values; retry with the admitted values")
			}

			if err = d.record(ctx, deploy.Owned{Kind: "ssm-parameter", ID: "arn:aws:ssm:" + c.Region + ":" + c.Account + ":parameter" + path, Name: path}); err != nil {
				return ecs.RegisterTaskDefinitionInput{}, err
			}
			container.Secrets = append(container.Secrets, ecstypes.Secret{Name: aws.String(k), ValueFrom: aws.String(path)})
		}
		if name == part.Name && part.Public && part.Port > 0 {
			container.PortMappings = []ecstypes.PortMapping{{ContainerPort: aws.Int32(int32(part.Port)), Protocol: ecstypes.TransportProtocolTcp}}
		}
		tags := append(d.tags(p), ecstypes.Tag{Key: aws.String("apptown:generation"), Value: aws.String(strconv.FormatInt(p.Next, 10))})
		return ecs.RegisterTaskDefinitionInput{Family: aws.String(d.name(p) + "-" + name), Cpu: aws.String(strconv.Itoa(size.CPU)), Memory: aws.String(strconv.Itoa(size.Memory)), NetworkMode: ecstypes.NetworkModeAwsvpc, RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate}, ExecutionRoleArn: aws.String(c.ExecutionRole), RuntimePlatform: &ecstypes.RuntimePlatform{CpuArchitecture: ecstypes.CPUArchitectureX8664, OperatingSystemFamily: ecstypes.OSFamilyLinux}, Tags: tags, ContainerDefinitions: []ecstypes.ContainerDefinition{container}}, nil
	}
	size, _ := fit(part.Resources)
	pl.Definition, err = definition(part.Name, src.Images[part.Name], part.Command, part.Env, size)
	if err != nil {
		return deploy.Artifact{}, err
	}
	pl.Definition.Family = aws.String(d.name(p))
	for _, init := range selected.Init {
		size, _ := fit(appcontract.Resources{})
		def, err := definition("init-"+init.Name, src.Init[init.Name], init.Command, init.Env, size)
		if err != nil {
			return deploy.Artifact{}, err
		}
		pl.Init = append(pl.Init, def)
	}
	body, err := json.Marshal(pl)
	return deploy.Artifact{Kind: "aws-ecs", Body: body, Hash: deploy.ConfigHash(string(body))}, err
}
func network(c Config, e Estate, group string) *ecstypes.NetworkConfiguration {
	public := ecstypes.AssignPublicIpDisabled
	if e.PublicIP {
		public = ecstypes.AssignPublicIpEnabled
	}
	return &ecstypes.NetworkConfiguration{AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: c.TaskSubnets, SecurityGroups: []string{group}, AssignPublicIp: public}}
}
func (d *Driver) ensureRoute(ctx context.Context, a *API, c Config, e Estate, p deploy.Placement, group string, part appcontract.Part) (string, string, error) {
	target, ok, err := d.recorded(ctx, "target-group")
	if err != nil {
		return "", "", err
	}
	if ok {
		ok, err = d.checkOwned(ctx, a, c, p, target)
		if err != nil {
			return "", "", err
		}
	}
	if !ok {
		tags := []elbtypes.Tag{}
		for _, t := range d.tags(p) {
			tags = append(tags, elbtypes.Tag{Key: t.Key, Value: t.Value})
		}
		in := &elasticloadbalancingv2.CreateTargetGroupInput{Name: aws.String(d.name(p)), Protocol: elbtypes.ProtocolEnumHttp, Port: aws.Int32(int32(part.Port)), VpcId: aws.String(e.VPC), TargetType: elbtypes.TargetTypeEnumIp, Tags: tags}
		hp, _ := partHealth(part)
		if hp != nil {
			in.HealthCheckPath = aws.String(part.Intent.Readiness.HTTP)
			in.HealthCheckIntervalSeconds = aws.Int32(int32(hp.Period.Seconds()))
			in.HealthCheckTimeoutSeconds = aws.Int32(int32(hp.Timeout.Seconds()))
			in.HealthyThresholdCount = aws.Int32(int32(hp.SuccessThreshold))
			in.UnhealthyThresholdCount = aws.Int32(int32(hp.FailureThreshold))
			if part.Intent.Readiness.Expect != 0 {
				in.Matcher = &elbtypes.Matcher{HttpCode: aws.String(strconv.Itoa(part.Intent.Readiness.Expect))}
			}
		}
		out, err := a.ELB.CreateTargetGroup(ctx, in)
		if err != nil {
			return "", "", err
		}
		if len(out.TargetGroups) != 1 {
			return "", "", errors.New("AWS returned no target group")
		}
		target = deploy.Owned{Kind: "target-group", ID: aws.ToString(out.TargetGroups[0].TargetGroupArn), Name: d.name(p)}
		if err = d.record(ctx, target); err != nil {
			return "", "", err
		}
		for _, lbGroup := range e.LoadBalancerGroups {
			_, err = a.EC2.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{GroupId: aws.String(group), IpPermissions: []ec2types.IpPermission{{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(int32(part.Port)), ToPort: aws.Int32(int32(part.Port)), UserIdGroupPairs: []ec2types.UserIdGroupPair{{GroupId: aws.String(lbGroup)}}}}})
			if err != nil {
				return "", "", err
			}
		}
	}
	rule, ok, err := d.recorded(ctx, "listener-rule")
	if err != nil {
		return "", "", err
	}
	if ok {
		ok, err = d.checkOwned(ctx, a, c, p, rule)
		if err != nil {
			return "", "", err
		}
	}
	if !ok {
		used := map[int]bool{}
		var marker *string
		for {
			out, err := a.ELB.DescribeRules(ctx, &elasticloadbalancingv2.DescribeRulesInput{ListenerArn: aws.String(c.Listener), Marker: marker})
			if err != nil {
				return "", "", err
			}
			for _, r := range out.Rules {
				n, _ := strconv.Atoi(aws.ToString(r.Priority))
				used[n] = true
			}
			marker = out.NextMarker
			if aws.ToString(marker) == "" {
				break
			}
		}
		priority := 1
		for used[priority] {
			priority++
		}
		if priority > 50000 {
			return "", "", errors.New("the listener has no free rule priority")
		}
		tags := []elbtypes.Tag{}
		for _, t := range d.tags(p) {
			tags = append(tags, elbtypes.Tag{Key: t.Key, Value: t.Value})
		}
		out, err := a.ELB.CreateRule(ctx, &elasticloadbalancingv2.CreateRuleInput{ListenerArn: aws.String(c.Listener), Priority: aws.Int32(int32(priority)), Conditions: []elbtypes.RuleCondition{{Field: aws.String("host-header"), Values: []string{d.name(p) + "." + c.Domain}}}, Actions: []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: aws.String(target.ID)}}, Tags: tags})
		if err != nil {
			return "", "", err
		}
		if len(out.Rules) != 1 {
			return "", "", errors.New("AWS returned no listener rule")
		}
		rule = deploy.Owned{Kind: "listener-rule", ID: aws.ToString(out.Rules[0].RuleArn), Name: d.name(p)}
		if err = d.record(ctx, rule); err != nil {
			return "", "", err
		}
	}
	return target.ID, rule.ID, nil
}

// pruneParameters runs only after a witnessed generation. Keep n and n-1 for
// rollback; retained owned records continue to identify any failed cleanup.
func (d *Driver) pruneParameters(ctx context.Context, p deploy.Placement, g int64) error {
	owned, err := d.Owned(ctx)
	if err != nil {
		return err
	}
	a, c, _, err := d.Client(ctx)
	if err != nil {
		return err
	}
	for _, o := range owned {
		if o.Kind != "ssm-parameter" {
			continue
		}
		suffix, ok := strings.CutPrefix(o.Name, d.path(p)+"/g")
		if !ok {
			continue
		}
		n, _, ok := strings.Cut(suffix, "/")
		if !ok {
			continue
		}
		generation, err := strconv.ParseInt(n, 10, 64)
		if err != nil || generation >= g-1 {
			continue
		}
		live, err := d.checkOwned(ctx, a, c, p, o)
		if err != nil {
			return err
		}
		if !live {
			continue
		}
		if err := d.remove(ctx, a, c, o); err != nil {
			return err
		}
	}
	return nil
}
