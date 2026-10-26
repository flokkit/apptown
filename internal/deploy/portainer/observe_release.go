package portainer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
)

// observeRelease verifies identity and component state only. A later scoped
// public readiness sample is required before health may become healthy.
func inspectRelease(ctx context.Context, cfg *Target, p deploy.Placement, stack string, expected *expectedRelease) (deploy.Observation, string, error) {
	publicID := ""
	out := deploy.Observation{Generation: p.Generation, Rollout: "unknown", Health: "unknown", URL: p.URL}
	if cfg.Scope == nil || *cfg.Scope != expected.Scope || expected.Readiness.Deployment != p.ID || expected.Generation != p.Generation {
		return out, "", errors.New("Portainer release observation differs from recorded deployment, generation or provider scope")
	}
	filters, _ := json.Marshal(map[string][]string{"label": {composeProject + "=" + stack}})
	code, raw, err := JSON(ctx, cfg, "GET", fmt.Sprintf("/api/endpoints/%d/docker/containers/json?all=1&filters=%s", cfg.Scope.EndpointID, url.QueryEscape(string(filters))), nil)
	if err != nil {
		return out, "", err
	}
	if code != 200 {
		return out, "", fmt.Errorf("listing Portainer release components: HTTP %d", code)
	}
	var listed []struct {
		ID     string            `json:"Id"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return out, "", err
	}
	if listed == nil {
		return out, "", errors.New("Portainer release component list is null")
	}
	for _, component := range expected.Readiness.Components {
		id := ""
		for _, c := range listed {
			if c.Labels[composeProject] != stack || c.Labels[composeService] != component.Service {
				continue
			}
			if id != "" {
				return out, "", fmt.Errorf("Portainer component %s has ambiguous containers", component.Service)
			}
			if c.ID == "" {
				return out, "", errors.New("Portainer component has no container identity")
			}
			id = c.ID
		}
		if id == "" {
			return out, "", fmt.Errorf("Portainer component %s is missing", component.Service)
		}
		code, raw, err = JSON(ctx, cfg, "GET", fmt.Sprintf("/api/endpoints/%d/docker/containers/%s/json", cfg.Scope.EndpointID, url.PathEscape(id)), nil)
		if err != nil {
			return out, "", err
		}
		if code != 200 {
			return out, "", fmt.Errorf("inspecting Portainer release component: HTTP %d", code)
		}
		var actual struct {
			ID     string `json:"Id"`
			Image  string
			Config struct {
				Image  string
				Labels map[string]string
			}
			State struct {
				Running   bool
				StartedAt time.Time
				Status    string
				ExitCode  int
				Health    *struct{ Status string }
			}
		}
		if err := json.Unmarshal(raw, &actual); err != nil {
			return out, "", err
		}
		labels := actual.Config.Labels
		if actual.ID != id || actual.Image == "" || actual.Config.Image != component.Image || labels[composeProject] != stack || labels[composeService] != component.Service || labels[releaseDeployment] != p.ID.String() || labels[releaseGeneration] != strconv.FormatInt(expected.Generation, 10) || labels[releaseArtifact] != expected.ArtifactHash || labels[releaseComponent] != component.Service {
			return out, "", fmt.Errorf("Portainer component %s does not match the admitted release", component.Service)
		}
		code, raw, err = JSON(ctx, cfg, "GET", fmt.Sprintf("/api/endpoints/%d/docker/images/%s/json", cfg.Scope.EndpointID, url.PathEscape(actual.Image)), nil)
		if err != nil {
			return out, "", err
		}
		if code != 200 {
			return out, "", fmt.Errorf("inspecting Portainer release image: HTTP %d", code)
		}
		var image struct {
			ID          string `json:"Id"`
			RepoDigests []string
		}
		if err := json.Unmarshal(raw, &image); err != nil {
			return out, "", err
		}
		if image.ID != actual.Image || !slices.Contains(image.RepoDigests, component.Image) {
			return out, "", fmt.Errorf("Portainer component %s actual image does not match admitted digest", component.Service)
		}
		if component.Init && actual.State.Status == "exited" && actual.State.ExitCode != 0 {
			return out, "", &predicateFailure{reason: fmt.Sprintf("Portainer init %s failed with exit %d", component.Service, actual.State.ExitCode), final: true}
		}
		if component.Init {
			if actual.State.Running || actual.State.Status != "exited" || actual.State.ExitCode != 0 {
				return out, "", fmt.Errorf("Portainer init %s has not completed successfully for this release", component.Service)
			}
		} else {
			if !actual.State.Running {
				return out, "", fmt.Errorf("Portainer component %s is not running", component.Service)
			}
			if component.ShellHealth && (actual.State.Health == nil || actual.State.Health.Status != "healthy") {
				return out, "", &predicateFailure{reason: fmt.Sprintf("Portainer component %s has not passed its declared healthcheck", component.Service)}
			}
		}
		if component.Service == "app" {
			out.Running = component.Image
			if !actual.State.StartedAt.IsZero() {
				out.RuntimeToken = id + "/" + actual.State.StartedAt.UTC().Format(time.RFC3339Nano)
			}
			publicID = id
		}
	}
	out.Rollout = "active"
	return out, publicID, nil
}

func observeRelease(ctx context.Context, cfg *Target, p deploy.Placement, stack string, expected *expectedRelease) (deploy.Observation, error) {
	observed, _, err := inspectRelease(ctx, cfg, p, stack, expected)
	return observed, err
}
