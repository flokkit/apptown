package docker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// ReadinessSample is the selected generation's retained intent and image.
// The application owner resolves its immutable source; no working tree is read.
type ReadinessSample struct {
	Placement       deploy.Placement
	Artifact, Image string
	Part            *appcontract.Part
	Timeout         time.Duration
}

// Sample asks one bounded readiness question of the exact running container.
// It neither settles a release nor restarts an application.
func (d *Driver) Sample(ctx context.Context, in ReadinessSample, allowed func(context.Context) error) (deploy.Observation, error) {
	p := in.Placement
	unknown := deploy.Observation{Generation: p.Generation, Health: "unknown", Handle: p.Handle}
	if allowed == nil || d.Scope == nil || in.Part == nil || in.Artifact == "" || in.Image == "" || p.Gate != nil || p.HostPort <= 0 {
		return unknown, errors.New("Docker readiness lacks a permitted retained standing workload")
	}
	timeout := in.Timeout
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	if timeout < time.Second || timeout > 10*time.Second {
		return unknown, errors.New("Docker readiness timeout is unsupported")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()
	if err := allowed(ctx); err != nil {
		return unknown, err
	}
	dk, err := d.Scope(ctx)
	if err != nil {
		return unknown, err
	}
	names, err := NamesOf(p.ID, p.Name)
	if err != nil {
		return unknown, err
	}
	if p.Container != names.App() {
		return unknown, errors.New("Docker readiness container differs from retained placement")
	}
	id := deploy.DockerIdentity{Deployment: p.Label, Generation: p.Generation, Artifact: in.Artifact}
	before, err := Inspect(ctx, dk, p.Container)
	if err != nil {
		return unknown, err
	}
	if err := VerifyIdentity(id, before, in.Image); err != nil {
		return unknown, err
	}
	token, err := readinessRuntimeToken(before)
	if err != nil {
		return unknown, err
	}
	port, err := ContainerHostPort(ctx, dk, before.ID, in.Part.Port)
	if err != nil || port != p.HostPort {
		return unknown, errors.New("Docker readiness port differs from the captured container mapping")
	}
	origin, err := url.Parse(p.URL)
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" || origin.User != nil {
		return unknown, errors.New("Docker readiness has no retained public address")
	}
	probe := RenderColdBootProbe(in.Part, map[string]string{"X-Forwarded-Proto": origin.Scheme})
	if probe.Path == "" {
		return unknown, errors.New("Docker readiness has no declared HTTP path")
	}
	if err := allowed(ctx); err != nil {
		return unknown, err
	}
	probeCtx, stopProbe := context.WithTimeout(ctx, timeout)
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, strings.TrimRight(dk.ProbeURL(p.HostPort), "/")+"/"+strings.TrimLeft(probe.Path, "/"), nil)
	if err != nil {
		stopProbe()
		return unknown, err
	}
	for key, value := range probe.Headers {
		req.Header.Set(key, value)
	}
	if in.Part.Intent == nil || in.Part.Intent.Readiness == nil || in.Part.Intent.Readiness.RequestContext != "direct" {
		req.Host = origin.Host
	}
	client := &http.Client{Transport: check.ProbeTransport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		stopProbe()
		return unknown, fmt.Errorf("Docker readiness probe: %w", err)
	}
	healthy := probe.Expect == 0 || resp.StatusCode == probe.Expect
	drainAndClose(resp.Body)
	stopProbe()
	after, err := Inspect(ctx, dk, p.Container)
	if err != nil {
		return unknown, err
	}
	if err := VerifyIdentity(id, after, in.Image); err != nil {
		return unknown, err
	}
	afterToken, err := readinessRuntimeToken(after)
	if err != nil || afterToken != token {
		return unknown, errors.New("Docker runtime changed during readiness sample")
	}
	afterPort, err := ContainerHostPort(ctx, dk, after.ID, in.Part.Port)
	if err != nil || afterPort != port {
		return unknown, errors.New("Docker readiness port changed during sampling")
	}
	if err := allowed(ctx); err != nil {
		return unknown, err
	}
	health := "unhealthy"
	if healthy {
		health = "healthy"
	}
	return deploy.Observation{Generation: p.Generation, ArtifactHash: in.Artifact, ImageDigest: in.Image, RuntimeToken: token, HealthObservedAt: clock.Or(d.Now)(), Health: health, Rollout: "active", URL: p.URL, Handle: p.Handle}, nil
}

func readinessRuntimeToken(rt Runtime) (string, error) {
	if rt.ID == "" || rt.State == nil || rt.State.StartedAt.IsZero() {
		return "", errors.New("Docker runtime has no complete probe incarnation")
	}
	return rt.ID + "/" + rt.State.StartedAt.UTC().Format(time.RFC3339Nano), nil
}
