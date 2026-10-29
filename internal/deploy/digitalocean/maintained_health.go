package digitalocean

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// Sample observes one retained generation without changing provider resources.
// The provider health report has no deployment ID or sample time: exact app
// reads bracket it, and HealthObservedAt is our observation completion time.
func (d *Driver) Sample(ctx context.Context, p deploy.Placement, artifact string, allowed func(context.Context) error) (deploy.Observation, error) {
	unknown := deploy.Observation{Generation: p.Generation, Health: "unknown", Handle: p.Handle}
	if allowed == nil {
		return unknown, errors.New("App Platform health needs the current deployment permit")
	}
	if p.Gate != nil {
		return unknown, errors.New("gated placement cannot use standing App Platform health")
	}
	r, err := readHealthReceipt(p.Handle)
	if err != nil {
		return unknown, err
	}
	if r == nil {
		return unknown, errors.New("App Platform generation has no maintained health receipt")
	}
	budget := 5 * time.Second
	if r.Readiness.Policy != nil {
		budget = r.Readiness.Policy.Timeout + 5*time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if err := allowed(ctx); err != nil {
		return unknown, err
	}
	if err := d.receiptConnection(r, p.Generation); err != nil {
		return unknown, err
	}
	if artifact == "" || r.Artifact != artifact {
		return unknown, errors.New("App Platform admitted health artifact changed")
	}
	c, _, err := d.Client(ctx)
	if err != nil {
		return unknown, err
	}
	if err := receiptClient(r, c); err != nil {
		return unknown, err
	}
	// This bounded diagnostic never follows provider redirects with credentials.
	snapshot := *c
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	snapshot.HTTP = &client
	c = &snapshot
	before, err := sampleApp(ctx, c, r, allowed)
	if err != nil {
		return unknown, err
	}
	if err := allowed(ctx); err != nil {
		return unknown, err
	}
	var raw json.RawMessage
	if err := c.Call(ctx, http.MethodGet, "/v2/apps/"+url.PathEscape(r.App)+"/health", nil, &raw); err != nil {
		return unknown, err
	}
	health, err := componentHealth(raw, r.Components)
	if err != nil {
		return unknown, err
	}
	front := before.LiveURL
	if front == "" {
		front = before.DefaultIngress
	}
	if r.Readiness.Path != "" {
		probeContext := ctx
		cancelProbe := func() {}
		if r.Readiness.Policy != nil {
			probeContext, cancelProbe = context.WithTimeout(ctx, r.Readiness.Policy.Timeout)
		}
		ok, err := d.sampleFrontDoor(probeContext, strings.TrimRight(front, "/")+r.Readiness.Path, r.Readiness.Expect, "", allowed)
		cancelProbe()
		if err != nil {
			return unknown, err
		}
		if !ok {
			health = "unhealthy"
		}
	}
	if r.Release != "" {
		ok, err := d.sampleFrontDoor(ctx, strings.TrimRight(front, "/")+appcontract.ReleasePath, 0, r.Release, allowed)
		if err != nil {
			return unknown, err
		}
		if !ok {
			health = "unhealthy"
		}
	}
	after, err := sampleApp(ctx, c, r, allowed)
	if err != nil {
		return unknown, err
	}
	if before.LiveURL != after.LiveURL || before.DefaultIngress != after.DefaultIngress {
		return unknown, errors.New("App Platform front door changed during health observation")
	}
	if err := allowed(ctx); err != nil {
		return unknown, err
	}
	return deploy.Observation{Generation: p.Generation, ArtifactHash: artifact, HealthObservedAt: d.options.Now(), Rollout: "active", Health: health, URL: front, Handle: p.Handle, Running: r.Deployment, RuntimeToken: r.Deployment, ReadinessPolicy: r.Readiness.Policy}, nil
}

func sampleApp(ctx context.Context, c *Client, r *healthReceipt, allowed func(context.Context) error) (doApp, error) {
	var got struct {
		App doApp `json:"app"`
	}
	if err := allowed(ctx); err != nil {
		return got.App, err
	}
	if err := c.Call(ctx, http.MethodGet, "/v2/apps/"+url.PathEscape(r.App), nil, &got); err != nil {
		return got.App, err
	}
	a := got.App
	if a.ID != r.App || a.ActiveDeployment == nil || a.ActiveDeployment.ID != r.Deployment || a.ActiveDeployment.Phase != "ACTIVE" ||
		(a.PendingDeployment != nil && a.PendingDeployment.ID != r.Deployment) || (a.InProgressDeployment != nil && a.InProgressDeployment.ID != r.Deployment) {
		return a, errors.New("App Platform no longer runs the exact recorded deployment")
	}
	return a, nil
}

func (d *Driver) sampleFrontDoor(ctx context.Context, rawURL string, expect int, release string, allowed func(context.Context) error) (bool, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false, err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return false, errors.New("App Platform health has no usable public URL")
	}
	if err := allowed(ctx); err != nil {
		return false, err
	}
	if release == "" {
		a, err := appcontract.SampleReadiness(ctx, d.frontDoor.Transport, appcontract.ReadinessRequest{URL: rawURL, Expect: expect, Timeout: d.frontDoor.Timeout})
		return a.Ready, err
	}
	// The release identity (a static site's) follows the platform's
	// redirects and wants 200 with the release's own words.
	client := *d.frontDoor
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many health redirects")
		}
		return allowed(req.Context())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false, err
	}
	response, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, appcontract.MaxReleaseAnswer))
	if err != nil {
		return false, err
	}
	return appcontract.ServesRelease(response.StatusCode, body, release), nil
}
