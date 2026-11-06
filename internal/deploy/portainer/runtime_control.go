package portainer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
	"gopkg.in/yaml.v3"
)

// scaleAppParts retains the stack's needs and init steps. Standalone Compose
// keeps app services at zero on stopped releases; Start restores their replicas
// using the same images, secrets, labels and settings already held by Portainer.
func scaleAppParts(compose string, ready Readiness, count int) (string, error) {
	var file map[string]any
	if err := yaml.Unmarshal([]byte(compose), &file); err != nil {
		return "", err
	}
	services, ok := file["services"].(map[string]any)
	if !ok {
		return "", errors.New("stack has no services")
	}
	for _, part := range ready.Components {
		if part.Init {
			continue
		}
		svc, ok := services[part.Service].(map[string]any)
		if !ok {
			return "", errors.New("stack is missing an app part")
		}
		config, _ := svc["deploy"].(map[string]any)
		if config == nil {
			config = map[string]any{}
		}
		config["replicas"] = count
		svc["deploy"] = config
	}
	data, err := yaml.Marshal(file)
	return string(data), err
}

type runtimeContainer struct {
	ID     string `json:"Id"`
	Labels map[string]string
	State  string
}

func runtimeAppContainers(ctx context.Context, cfg *Target, p deploy.Placement, expected *expectedRelease) ([]runtimeContainer, error) {
	if cfg == nil || cfg.Scope == nil {
		return nil, errors.New("runtime control requires a verified Portainer scope")
	}
	if expected.Scope != *cfg.Scope || expected.Readiness.Deployment != p.ID || expected.Generation != p.Generation {
		return nil, errors.New("runtime control scope differs from the recorded release")
	}
	stack, _ := p.Handle["stack"].(string)
	if stack == "" {
		return nil, errors.New("deployment has no recorded stack")
	}
	filters, _ := json.Marshal(map[string][]string{"label": {composeProject + "=" + stack}})
	code, raw, err := JSON(ctx, cfg, "GET", fmt.Sprintf("/api/endpoints/%d/docker/containers/json?all=1&filters=%s", cfg.Scope.EndpointID, url.QueryEscape(string(filters))), nil)
	if err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, fmt.Errorf("listing app containers: HTTP %d", code)
	}
	var all []runtimeContainer
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	if all == nil {
		return nil, errors.New("app container list is null")
	}
	var selected []runtimeContainer
	for _, c := range all {
		for _, part := range expected.Readiness.Components {
			if part.Init || c.Labels[composeService] != part.Service {
				continue
			}
			if c.ID == "" || c.Labels[composeProject] != stack || c.Labels[releaseDeployment] != p.ID.String() || c.Labels[releaseGeneration] != strconv.FormatInt(p.Generation, 10) || c.Labels[releaseArtifact] != expected.ArtifactHash {
				return nil, errors.New("app container differs from the recorded release")
			}
			selected = append(selected, c)
		}
	}
	return selected, nil
}

func observeStopped(ctx context.Context, cfg *Target, p deploy.Placement, expected *expectedRelease) (deploy.Observation, error) {
	if expected.Stopped {
		if p.Handle["stackApplied"] != true {
			return deploy.Observation{}, errors.New("Portainer has not confirmed applying the stopped release")
		}
		id, err := runtimeStackID(p.Handle)
		if err != nil {
			return deploy.Observation{}, err
		}
		file, err := StackFile(ctx, cfg, strconv.FormatInt(id, 10))
		if err != nil {
			return deploy.Observation{}, err
		}
		normalized, err := scaleAppParts(file, expected.Readiness, 0)
		if err != nil {
			return deploy.Observation{}, err
		}
		if deploy.ConfigHash(strings.TrimSpace(normalized)) != expected.SubmittedHash {
			return deploy.Observation{}, errors.New("Portainer has not applied the recorded stopped release")
		}
	}
	containers, err := runtimeAppContainers(ctx, cfg, p, expected)
	if err != nil {
		return deploy.Observation{}, err
	}
	for _, c := range containers {
		if c.State != "exited" && c.State != "created" {
			return deploy.Observation{}, errors.New("the target has not confirmed all app parts stopped")
		}
	}
	return deploy.Observation{Generation: p.Generation, Rollout: "stopped", Health: "unknown", URL: p.URL, Handle: p.Handle}, nil
}

func (d *Driver) Runtime(ctx context.Context, p deploy.Placement, action string) error {
	cfg, err := d.Config(ctx, d.TargetID)
	if err != nil {
		return err
	}
	expected, err := savedRelease(p.Handle)
	if err != nil {
		return err
	}
	if expected == nil {
		return errors.New("runtime controls require recorded release components; deploy a release first")
	}
	containers, err := runtimeAppContainers(ctx, cfg, p, expected)
	if err != nil {
		return err
	}
	run := func(verb string) error {
		for _, c := range containers {
			code, _, err := JSON(ctx, cfg, "POST", fmt.Sprintf("/api/endpoints/%d/docker/containers/%s/%s", cfg.Scope.EndpointID, url.PathEscape(c.ID), verb), nil)
			if err != nil {
				return err
			}
			if code != 204 && code != 304 {
				return fmt.Errorf("could not %s app container: HTTP %d", verb, code)
			}
		}
		return nil
	}
	switch action {
	case "stop":
		if err := run("stop"); err != nil {
			return err
		}
		_, err := observeStopped(ctx, cfg, p, expected)
		return err
	case "restart":
		if err := run("stop"); err != nil {
			return err
		}
		if _, err := observeStopped(ctx, cfg, p, expected); err != nil {
			return err
		}
		if err := run("start"); err != nil {
			return err
		}
	case "start":
		// A stopped release may have replaced its app services at zero. Updating
		// that same recorded stack restores app replicas; needs keep their config.
		if expected.Stopped {
			id, err := runtimeStackID(p.Handle)
			if err != nil {
				return err
			}
			file, err := StackFile(ctx, cfg, strconv.FormatInt(id, 10))
			if err != nil {
				return err
			}
			stack, _ := p.Handle["stack"].(string)
			if err := VerifyStackAssociation(ctx, *cfg, Stack{ID: id, Name: stack, EndpointID: cfg.Scope.EndpointID}, p.ID, nil); err != nil {
				return err
			}
			content, err := scaleAppParts(file, expected.Readiness, 1)
			if err != nil {
				return err
			}
			associations, err := SavedAssociations(p.Handle)
			if err != nil {
				return err
			}
			if _, err := applyStack(ctx, cfg, cfg.Scope.EndpointID, stack, content, p.ID, associations); err != nil {
				return err
			}
		} else if err := run("start"); err != nil {
			return err
		}
	default:
		return errors.New("unknown runtime action")
	}
	// Observe exact current-release components, without building or backups.
	_, _, err = inspectRelease(ctx, cfg, p, p.Handle["stack"].(string), expected)
	return err
}

func runtimeStackID(handle map[string]any) (int64, error) {
	raw, err := json.Marshal(handle["stackId"])
	if err != nil {
		return 0, err
	}
	var id int64
	if err := json.Unmarshal(raw, &id); err != nil || id <= 0 {
		return 0, errors.New("stack has no recorded ID")
	}
	return id, nil
}

func (d *Driver) ObserveRuntime(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
	cfg, err := d.Config(ctx, d.TargetID)
	if err != nil {
		return deploy.Observation{}, err
	}
	expected, err := savedRelease(p.Handle)
	if err != nil {
		return deploy.Observation{}, err
	}
	if expected == nil {
		return deploy.Observation{}, errors.New("runtime controls require recorded release components; deploy a release first")
	}
	if p.Stopped {
		return observeStopped(ctx, cfg, p, expected)
	}
	containers, err := runtimeAppContainers(ctx, cfg, p, expected)
	if err != nil {
		return deploy.Observation{}, err
	}
	for _, part := range expected.Readiness.Components {
		if part.Init {
			continue
		}
		found := false
		for _, c := range containers {
			if c.Labels[composeService] != part.Service {
				continue
			}
			found = true
			if c.State != "running" {
				return deploy.Observation{}, errors.New("the target has not confirmed all app parts running")
			}
		}
		if !found {
			return deploy.Observation{}, errors.New("an app part is missing")
		}
	}
	return deploy.Observation{Generation: p.Generation, Rollout: "active"}, nil
}
