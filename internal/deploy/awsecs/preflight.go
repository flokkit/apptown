package awsecs

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

type taskSize struct{ CPU, Memory int }

func (s taskSize) words() string {
	return fmt.Sprintf("Fargate %.2g CPUs, %d MiB", float64(s.CPU)/1024, s.Memory)
}

func fit(r appcontract.Resources) (taskSize, error) {
	memory := int(math.Ceil(float64(r.MemoryBytes) / (1 << 20)))
	if memory == 0 {
		memory = 512
	}
	for _, pair := range []struct{ cpu, min, max, step int }{{256, 512, 2048, 512}, {512, 1024, 4096, 1024}, {1024, 2048, 8192, 1024}, {2048, 4096, 16384, 1024}, {4096, 8192, 30720, 1024}, {8192, 16384, 61440, 4096}, {16384, 32768, 122880, 8192}} {
		if float64(pair.cpu)/1024 < r.CPUs {
			continue
		}
		rounded := max(pair.min, ((memory+pair.step-1)/pair.step)*pair.step)
		// The 256-unit pair supports 512, 1024 and 2048 MiB, not 1536.
		if pair.cpu == 256 && rounded == 1536 {
			rounded = 2048
		}
		if rounded <= pair.max {
			return taskSize{pair.cpu, rounded}, nil
		}
	}
	if r.CPUs <= 32 {
		for _, m := range []int{60 * 1024, 120 * 1024, 244 * 1024} {
			if memory <= m {
				return taskSize{32768, m}, nil
			}
		}
	}
	return taskSize{}, fmt.Errorf("no Fargate size covers %s memory and %s CPUs", r.MemoryWords(), r.CPUWords())
}

func (d *Driver) FitResources(_ context.Context, c *appcontract.Contract) (map[string]string, error) {
	out := map[string]string{}
	if c == nil {
		return out, nil
	}
	for _, p := range c.Parts {
		size, err := fit(p.Resources)
		if err != nil {
			return nil, deploy.Terminal(err)
		}
		out[p.Name] = size.words()
	}
	return out, nil
}

// Preflight contains stage-one boundaries and is entirely local. It runs before
// a need, image, parameter or any other provider resource can be made.
func (d *Driver) Preflight(_ context.Context, p deploy.Placement, src deploy.Source, secrets deploy.ConsumerSecrets, _ deploy.Logf) error {
	refuse := func(s string) error { return deploy.Terminal(errors.New(s)) }
	if p.Gate != nil {
		return refuse("AWS ECS cannot place AppTown's gate yet, so it does not run a restored copy")
	}
	c := src.Contract
	if c == nil {
		return refuse("an AWS ECS release needs its contract")
	}
	if len(c.Parts) != 1 {
		return refuse("this target runs single-part apps for now")
	}
	for _, need := range c.Needs {
		if need != "postgres" {
			return refuse("AWS ECS does not provide the " + need + " need yet")
		}
		params := c.NeedParams[need]
		if v := params["version"]; v != "" && v != "16" && v != "17" {
			return refuse("AWS ECS provides PostgreSQL major versions 16 and 17")
		}
		for _, k := range []string{"instance", "shared", "sharedDatabase"} {
			if params[k] != "" {
				return refuse("AWS ECS provides a dedicated database instance per app; shared databases are not supported")
			}
		}
	}
	for _, part := range c.Parts {
		if part.DataDir != "" {
			return refuse("AWS ECS does not keep an app's data directory yet")
		}
		if _, err := fit(part.Resources); err != nil {
			return deploy.Terminal(err)
		}
		if !strings.Contains(src.Images[part.Name], "@sha256:") {
			return refuse("AWS ECS runs only images in a registry by digest")
		}
		if _, err := partHealth(part); err != nil {
			return deploy.Terminal(err)
		}
		if secrets != nil {
			for name, v := range secrets.EnvFor(part.Name) {
				if len(v) > 4096 {
					return refuse("secret " + name + " exceeds SSM's 4 KB limit")
				}
			}
		}
	}
	for _, step := range c.Init {
		if !strings.Contains(src.Init[step.Name], "@sha256:") {
			return refuse("AWS ECS init steps need images in a registry by digest")
		}
		if secrets != nil {
			for name, v := range secrets.EnvFor("init-" + step.Name) {
				if len(v) > 4096 {
					return refuse("secret " + name + " exceeds SSM's 4 KB limit")
				}
			}
		}
	}
	return nil
}

func partHealth(p appcontract.Part) (*appcontract.HealthPolicy, error) {
	if p.Intent == nil {
		return nil, nil
	}
	ready, live := p.Intent.Readiness, p.Intent.Liveness
	if live != nil && (ready == nil || !reflect.DeepEqual(ready, live)) {
		return nil, fmt.Errorf("part %s: AWS ECS cannot honour different readiness and liveness probes", p.Name)
	}
	if ready == nil {
		return nil, nil
	}
	if !p.Public || p.Port <= 0 {
		return nil, errors.New("ECS cannot check a part without a public route over HTTP")
	}
	if ready.HTTP == "" {
		return nil, errors.New("AWS ECS readiness needs an HTTP path")
	}
	policy, err := appcontract.NormalizeHealthPolicy(ready, "readiness", appcontract.HealthPolicy{Period: 30 * time.Second, Timeout: 5 * time.Second, StartupGrace: 5 * time.Second, FailureThreshold: 2, SuccessThreshold: 5})
	if err != nil {
		return nil, err
	}
	if policy.FailureThreshold < 2 || policy.FailureThreshold > 10 || policy.SuccessThreshold < 2 || policy.SuccessThreshold > 10 {
		return nil, errors.New("ALB healthy and unhealthy thresholds must be from 2 through 10")
	}
	if policy.Timeout < 2*time.Second || policy.Timeout >= policy.Period {
		return nil, errors.New("ALB timeout must be at least 2 seconds and shorter than its interval")
	}
	if ready.Expect != 0 && (ready.Expect < 200 || ready.Expect > 499) {
		return nil, errors.New("ALB expected status must be from 200 through 499")
	}
	return &policy, nil
}

// RefusesContract checks local shape constraints at the release door, before
// needs are prepared. Preflight also checks resolved images and secret values.
func RefusesContract(c *appcontract.Contract) string {
	if len(c.Parts) != 1 {
		return "this target runs single-part apps for now"
	}
	for _, need := range c.Needs {
		for _, key := range []string{"instance", "shared", "sharedDatabase"} {
			if c.NeedParams[need][key] != "" {
				return "AWS ECS provides a dedicated database instance per app; shared databases are not supported"
			}
		}
	}
	return ""
}
