package digitalocean

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/deploy"
)

// This mark travels in each deployment's immutable spec, including when the
// provider applied it but AppTown never received or saved the response.
const recoveryEnv = "APPTOWN_DEPLOYMENT_RECEIPT"

type recoveryMark struct {
	HealthReceipt *healthReceipt    `json:"healthReceipt,omitempty"`
	Generation    int64             `json:"generation"`
	Readiness     readinessSnapshot `json:"readiness"`
	Release       string            `json:"release"`
	ArtifactKind  string            `json:"artifactKind"`
}

func (d *Driver) rediscover(ctx context.Context, p deploy.Placement, generation int64, name, appID string) (map[string]any, error) {
	if name == "" {
		if p.ID.IsZero() {
			return nil, errors.New("App Platform apply identity is missing; provider inspection is required before trying again")
		}
		name = doAppName(deploy.HostKey(p.ID))
	}
	c, _, err := d.Client(ctx)
	if err != nil {
		return nil, err
	}
	app, err := d.findApp(ctx, c, name)
	if err != nil {
		return nil, err
	}
	if app == nil {
		return nil, deploy.ErrNotApplied
	}
	if app.ID == "" || (appID != "" && app.ID != appID) {
		return nil, errors.New("App Platform app identity differs; provider inspection is required before trying again")
	}
	deployments, err := listAll[doDeployment](ctx, c, "/v2/apps/"+app.ID+"/deployments?per_page=200", "deployments")
	if err != nil {
		return nil, err
	}
	var matched *doDeployment
	var mark recoveryMark
	for i := range deployments {
		dep := &deployments[i]
		if dep.Spec.Name != name {
			continue
		}
		for _, env := range dep.Spec.Envs {
			if env.Key != recoveryEnv {
				continue
			}
			var candidate recoveryMark
			if err := json.Unmarshal([]byte(env.Value), &candidate); err != nil {
				return nil, fmt.Errorf("reading provider generation mark: %w", err)
			}
			if candidate.Generation != generation {
				continue
			}
			if matched != nil {
				return nil, errors.New("multiple provider deployments carry this generation; provider inspection is required")
			}
			matched, mark = dep, candidate
		}
	}
	if matched == nil || matched.ID == "" {
		return nil, errors.New("the provider has not identified this generation; its apply outcome is uncertain — inspect the provider before trying again")
	}
	noted := map[string]any{"appId": app.ID, "appName": name, "deploymentId": matched.ID, "artifactKind": mark.ArtifactKind, "release": mark.Release}
	if mark.HealthReceipt != nil {
		mark.HealthReceipt.App, mark.HealthReceipt.Deployment = app.ID, matched.ID
		noted["healthReceipt"] = *mark.HealthReceipt
	}
	if mark.Readiness.Version == 0 {
		noted["readinessPath"] = mark.Readiness.Path
	} else {
		noted["readiness"] = mark.Readiness
	}
	return noted, nil
}
