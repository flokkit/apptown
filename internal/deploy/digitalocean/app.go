package digitalocean

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/strs"
)

// --- the driver ------------------------------------------------------------------------------

type doApp struct {
	ID             string `json:"id"`
	LiveURL        string `json:"live_url"`
	DefaultIngress string `json:"default_ingress"`
	// CreatedAt is when the platform made the app, which is when its
	// provider-issued name was given (zero: the platform did not say).
	CreatedAt        time.Time `json:"created_at"`
	ActiveDeployment *struct {
		ID    string `json:"id"`
		Phase string `json:"phase"`
	} `json:"active_deployment"`
	InProgressDeployment *struct {
		ID    string `json:"id"`
		Phase string `json:"phase"`
	} `json:"in_progress_deployment"`
	PendingDeployment *struct {
		ID    string `json:"id"`
		Phase string `json:"phase"`
	} `json:"pending_deployment"`
	Spec struct {
		Name string `json:"name"`
		// Services are read for a gated app's mark (FindMade).
		Services []struct {
			Name string `json:"name"`
			Envs []struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"envs"`
		} `json:"services"`
	} `json:"spec"`
	Region struct {
		Slug string `json:"slug"`
	} `json:"region"`
}

type doDeployment struct {
	ID       string `json:"id"`
	Phase    string `json:"phase"`
	Cause    string `json:"cause"`
	Progress struct {
		Steps []doStep `json:"steps"`
	} `json:"progress"`
}

type doStep struct {
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Reason *doErr   `json:"reason,omitempty"`
	Steps  []doStep `json:"steps,omitempty"`
}

type doErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (d *Driver) findApp(ctx context.Context, c *Client, name string) (*doApp, error) {
	apps, err := listAll[doApp](ctx, c, "/v2/apps?per_page=200", "apps")
	if err != nil {
		return nil, err
	}
	for i := range apps {
		if apps[i].Spec.Name == name {
			return &apps[i], nil
		}
	}
	return nil, nil
}

// reconcile creates or updates the app with the spec and waits for the
// deployment to be ACTIVE. Admission permits one execution for the key;
// subsequent recovery uses Resume with the recorded provider identifiers.
func (d *Driver) reconcile(ctx context.Context, p deploy.Placement, generation int64, art deploy.Artifact, key string, logf func(string, ...any), checkpoint deploy.Checkpoint) (deploy.Observation, error) {
	decoded, err := decodeAppArtifact(art)
	if err != nil {
		return deploy.Observation{}, deploy.Terminal(err)
	}
	c, _, err := d.Client(ctx)
	if err != nil {
		return deploy.Observation{}, err
	}
	spec := decoded.Wire
	name := decoded.Spec.Name
	app, err := d.findApp(ctx, c, name)
	if err != nil {
		return deploy.Observation{}, err
	}
	var got struct {
		App doApp `json:"app"`
	}
	if app == nil {
		logf("creating app %s on App Platform", name)
		if err := c.Call(ctx, "POST", "/v2/apps", map[string]any{"spec": spec}, &got); err != nil {
			return deploy.Observation{}, classifyDO(err)
		}
	} else {
		logf("updating app %s (%s) with the new spec", name, app.ID)
		if err := c.Call(ctx, "PUT", "/v2/apps/"+app.ID, map[string]any{"spec": spec}, &got); err != nil {
			return deploy.Observation{}, classifyDO(err)
		}
	}
	app = &got.App
	// The deployment this spec caused is named in the response; wait for
	// THAT one. Never the app's active deployment, which is the previous
	// generation still serving (§14.7).
	depID := ""
	switch {
	case app.PendingDeployment != nil:
		depID = app.PendingDeployment.ID
	case app.InProgressDeployment != nil:
		depID = app.InProgressDeployment.ID
	}
	if app.ID == "" {
		return deploy.Observation{}, errors.New("the platform accepted the spec without naming its app; provider inspection is required")
	}
	question := decoded.Readiness
	ids := map[string]any{"appId": app.ID, "appName": name, "artifactKind": art.Kind}
	if question.Version == 0 {
		ids["readinessPath"] = question.Path
	} else {
		ids["readiness"] = question
	}
	release := expectedRelease(art.Source)
	if release != "" {
		ids["release"] = release
	}
	if depID != "" {
		ids["deploymentId"] = depID
	}
	if err := checkpoint(ids); err != nil {
		return deploy.Observation{}, fmt.Errorf("persisting the provider response: %w", err)
	}
	if depID == "" {
		return deploy.Observation{}, errors.New("the platform accepted the spec without naming its deployment; cannot adopt another pending deployment")
	}
	logf("the platform's deployment for this generation is %s", strs.ShortSHA(depID))
	dep, err := d.waitDeployment(ctx, c, app.ID, depID, p.Gate == nil, logf)
	if err != nil {
		return deploy.Observation{}, err
	}
	return d.finish(ctx, c, app.ID, name, dep, generation, question, release, p.Gate != nil, logf)
}

// finish: after the deployment is ACTIVE, re-read the app, refuse a cut-over
// on a guess, ask the front door (§4.3), and witness that it serves this
// release: a public static part's file server answers its release identity
// at ReleasePath. A public part that declares no identity leaves which
// release answers unwitnessed, and the log says so; it is never assumed.
//
// A gated deployment's front door is AppTown's gate, which answers 404 to
// anyone without a credential: it is not asked here. AppTown's own checks
// ask the app through the gate with the gate secret, and record only status
// codes; nothing here reads a response body of a gated deployment.
func (d *Driver) finish(ctx context.Context, c *Client, appID, name string, dep *doDeployment, generation int64, question readinessSnapshot, release string, gated bool, logf func(string, ...any)) (deploy.Observation, error) {
	var got struct {
		App doApp `json:"app"`
	}
	if err := c.Call(ctx, "GET", "/v2/apps/"+appID, nil, &got); err != nil {
		return deploy.Observation{}, err
	}
	app := &got.App
	if app.ID != appID {
		return deploy.Observation{}, fmt.Errorf("provider returned app %q while observing %q", app.ID, appID)
	}
	url := app.LiveURL
	if url == "" && app.DefaultIngress != "" {
		url = app.DefaultIngress
	}
	// Done means reachable (§4.3): the front door answers the readiness
	// path, with bounded retries. The platform switches traffic per
	// deployment, and the active one is the one we waited for.
	if app.ActiveDeployment == nil || app.ActiveDeployment.ID != dep.ID {
		return deploy.Observation{}, fmt.Errorf("deployment %s finished but the app's active deployment is %s — not cutting over on a guess", strs.ShortSHA(dep.ID), activeID(app))
	}
	until := providerNameGrace(url, app.CreatedAt)
	if gated {
		if err := d.awaitProviderName(ctx, url, until, logf); err != nil {
			return deploy.Observation{}, err
		}
		logf("the front door is AppTown's gate; AppTown asks the app through it with the gate's secret")
		return deploy.Observation{Generation: generation, Rollout: "active", Health: "unknown", URL: url,
			Handle: map[string]any{"appId": app.ID, "appName": name, "deploymentId": dep.ID}}, nil
	}
	// Healthy is what AppTown saw: the front door answered the readiness
	// path, or served this release's identity. Neither asked is unknown,
	// whatever the platform's own probe says.
	witnessed := false
	if path := question.Path; path != "" && url != "" {
		if err := d.options.ProbeFrontDoor(ctx, strings.TrimRight(url, "/")+path, question.Expect, until, logf); err != nil {
			return deploy.Observation{}, err
		}
		witnessed = true
	}
	switch {
	case url == "":
		logf("the platform names no URL for the app: which release it serves is not witnessed")
	case release == "":
		logf("which release the front door serves is not witnessed: the public part's image declares no release identity")
	default:
		if err := witnessRelease(ctx, d.frontDoor, strings.TrimRight(url, "/")+appcontract.ReleasePath, release, until, logf, d.options.PollInterval, d.options.Now); err != nil {
			return deploy.Observation{}, err
		}
		witnessed = true
	}
	if witnessed {
		var current struct {
			App doApp `json:"app"`
		}
		if err := c.Call(ctx, "GET", "/v2/apps/"+appID, nil, &current); err != nil {
			return deploy.Observation{}, err
		}
		if current.App.ID != appID || activeID(&current.App) != dep.ID {
			return deploy.Observation{}, errors.New("the app's active deployment changed during readiness observation")
		}
	}
	health := "unknown"
	if witnessed {
		health = "healthy"
	}
	return deploy.Observation{Generation: generation, Rollout: "active", Health: health, URL: url,
		Handle: map[string]any{"appId": app.ID, "appName": name, "deploymentId": dep.ID}}, nil
}

// Resume only observes the exact app and deployment checkpointed by the
// original response. An app name or whichever deployment is pending is not proof.
func (d *Driver) resume(ctx context.Context, p deploy.Placement, generation int64, noted map[string]any, logf func(string, ...any)) (deploy.Observation, error) {
	appID, _ := noted["appId"].(string)
	name, _ := noted["appName"].(string)
	depID, _ := noted["deploymentId"].(string)
	if appID == "" {
		return deploy.Observation{}, errors.New("AppTown restarted before the platform named the app; inspect the provider before resolving this attempt")
	}
	if depID == "" {
		return deploy.Observation{}, errors.New("AppTown has no recorded deployment identity; cannot adopt the app's current or pending deployment")
	}
	question, err := checkpointReadiness(noted)
	if err != nil {
		return deploy.Observation{}, deploy.Terminal(err)
	}
	c, _, err := d.Client(ctx)
	if err != nil {
		return deploy.Observation{}, fmt.Errorf("reading provider access during recovery: %w", err)
	}
	logf("resuming after a restart: waiting on deployment %s of app %s", strs.ShortSHA(depID), strs.ShortSHA(appID))
	dep, err := d.waitDeployment(ctx, c, appID, depID, p.Gate == nil, logf)
	if err != nil {
		return deploy.Observation{}, err
	}
	release, _ := noted["release"].(string)
	return d.finish(ctx, c, appID, name, dep, generation, question, release, p.Gate != nil, logf)
}

func activeID(app *doApp) string {
	if app != nil && app.ActiveDeployment != nil {
		return app.ActiveDeployment.ID
	}
	return ""
}

// readinessPathOf: the public service's health path from the rendered
// spec, "" when it declares none.
func readinessPathOf(spec AppSpec) string {
	for _, s := range spec.Services {
		if s.HealthCheck != nil && s.HealthCheck.HTTPPath != "" {
			return s.HealthCheck.HTTPPath
		}
	}
	return ""
}

// expectedRelease is the release identity the front door must answer, from
// the selected source itself, never from a component's environment: the
// public part's, when it is a static site (its file server answers it), and
// "" when the public part is not static, whose identity is unwitnessed.
func expectedRelease(src *deploy.Source) string {
	if src == nil {
		return ""
	}
	return src.Contract.PublicRelease(src.Revision)
}

// witnessRelease asks the front door which release it serves, six tries
// apart, until it answers this release's public part. Anything else (the
// file server's stock page, an older release, another part, no answer) is
// not this release, and the release fails saying what answered instead.
// Before until, a try the platform's new name did not answer is not
// counted (notYet).
func witnessRelease(ctx context.Context, client *http.Client, url, release string, until time.Time, logf func(string, ...any), interval time.Duration, now func() time.Time) error {
	var last string
	waiting := false
	for tries := 0; tries < 6; {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if !notYet(err, now(), until, &waiting, logf) {
			tries++ // a request the new name did not answer yet is not a try
		}
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
			res.Body.Close()
			got := strings.TrimSpace(string(body))
			if res.StatusCode == http.StatusOK && got == release {
				logf("the front door serves this release: %s answers %s", url, release)
				return nil
			}
			last = fmt.Sprintf("%d %q", res.StatusCode, strs.Truncate(got, 80))
		} else {
			last = err.Error()
		}
		if err := waitDeploymentPoll(ctx, interval); err != nil {
			return err
		}
	}
	return deploy.Terminal(fmt.Errorf("the front door does not serve this release: %s should answer %s, and answered %s after six tries", url, release, last))
}

// platformDomain is the domain App Platform issues app names under.
const platformDomain = ".ondigitalocean.app"

// ProviderNameGrace is how long after an app is created its
// provider-issued name may fail to answer without that being a failure. A
// resolver that asked for the name before the platform published it keeps
// "no such host" for the zone's negative-cache time (ondigitalocean.app's
// SOA minimum is 1800 seconds), and DigitalOcean's VPC resolver answers from
// several caches that disagree, so in that window the name, its A and its
// AAAA records come and go from one look-up to the next. The grace is that
// half hour plus five minutes for the platform to publish the name; after
// it, a name that does not answer is asked like any other.
const ProviderNameGrace = 35 * time.Minute

// providerNameGrace is when the grace of url's host ends, when the host is
// the platform's own name for an app created at created; zero for any other
// host or an unknown creation time.
func providerNameGrace(rawURL string, created time.Time) time.Time {
	u, err := url.Parse(rawURL)
	if err != nil || created.IsZero() || !strings.HasSuffix(u.Hostname(), platformDomain) {
		return time.Time{}
	}
	return created.Add(ProviderNameGrace)
}

// notYet reports whether a front door request failed, before until, only
// because the platform's new name did not answer: DNS said it does not
// exist, or it gave no address AppTown has a route to (an IPv6-only
// answer on a host without IPv6). Go's dialer already tries every address
// it is given, IPv4 and IPv6, so an unreachable error means none connected.
// It says so once per check (waiting).
func notYet(err error, now, until time.Time, waiting *bool, logf func(string, ...any)) bool {
	if err == nil || !now.Before(until) {
		return false
	}
	var dnsErr *net.DNSError
	if !(errors.As(err, &dnsErr) && dnsErr.IsNotFound || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH)) {
		return false
	}
	if !*waiting {
		logf("the platform's new name does not answer yet (%v); asking again, without counting it as a try, until %s", err, until.UTC().Format(time.RFC3339))
		*waiting = true
	}
	return true
}

// awaitProviderName waits, for a gated copy whose front door AppTown
// does not ask here, while url's host is the platform's new name, DNS says
// it does not exist and until has not passed. It asks every three poll
// intervals (30 seconds by default), so a resolver's cached negative answer
// can expire between asks. The checks through the gate are asked later
// through the system resolver and are not covered by the grace.
func (d *Driver) awaitProviderName(ctx context.Context, rawURL string, until time.Time, logf func(string, ...any)) error {
	u, err := url.Parse(rawURL)
	if err != nil || until.IsZero() {
		return nil
	}
	host := u.Hostname()
	waited := false
	for {
		err := d.options.Resolve(ctx, host)
		var dnsErr *net.DNSError
		if err == nil || !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
			if waited && err == nil {
				logf("the platform's name %s is in DNS now", host)
			}
			return nil
		}
		if !d.options.Now().Before(until) {
			logf("the platform's name %s is still not in DNS at %s; handing it to the checks anyway", host, until.UTC().Format(time.RFC3339))
			return nil
		}
		if !waited {
			logf("the platform's name %s is not in DNS yet; waiting for it until %s", host, until.UTC().Format(time.RFC3339))
			waited = true
		}
		if err := waitDeploymentPoll(ctx, 3*d.options.PollInterval); err != nil {
			return err
		}
	}
}

// probeFrontDoor asks the real front door for the readiness path: six
// tries, ten seconds apart by default. An explicit status must match without
// following redirects; omitted expectations retain the successful-response
// predicate and redirect policy. Before until,
// a try the platform's new name did not answer is not counted (notYet).
func probeFrontDoor(ctx context.Context, client *http.Client, url string, expect int, until time.Time, logf func(string, ...any), interval time.Duration, now func() time.Time) error {
	if expect != 0 {
		copy := *client
		copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client = &copy
	}
	var last string
	waiting := false
	for tries := 0; tries < 6; {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if !notYet(err, now(), until, &waiting, logf) {
			tries++ // a request the new name did not answer yet is not a try
		}
		if err == nil {
			res.Body.Close()
			if (expect == 0 && res.StatusCode < 300) || (expect != 0 && res.StatusCode == expect) {
				logf("the front door answers %s (%d)", url, res.StatusCode)
				return nil
			}
			last = fmt.Sprintf("%d", res.StatusCode)
		} else {
			last = err.Error()
		}
		if err := waitDeploymentPoll(ctx, interval); err != nil {
			return err
		}
	}
	return fmt.Errorf("the front door did not answer %s after six tries (last: %s)", url, last)
}

func classifyDO(err error) error {
	var e *APIError
	if errors.As(err, &e) && (e.Status == 400 || e.Status == 401 || e.Status == 403 || e.Status == 422) {
		return deploy.Terminal(err)
	}
	return err
}

// WaitDeployment polls the app's in-progress deployment to ACTIVE, or
// explains the failing step in the platform's words.
func (d *Driver) WaitDeployment(ctx context.Context, c *Client, appID, depID string, logf func(string, ...any)) (*doDeployment, error) {
	return d.waitDeployment(ctx, c, appID, depID, true, logf)
}

// waitDeployment is WaitDeployment; tails says whether a failing
// component's last log lines go into the log. A gated deployment's never
// do: its app runs on production data, so its logs can hold rows of it.
// The platform's own sentence for the failing step is kept.
func (d *Driver) waitDeployment(ctx context.Context, c *Client, appID, depID string, tails bool, logf func(string, ...any)) (*doDeployment, error) {
	deadline := d.options.Now().Add(20 * time.Minute)
	var lastPhase string
	for {
		{
			var dg struct {
				Deployment doDeployment `json:"deployment"`
			}
			if err := c.Call(ctx, "GET", "/v2/apps/"+appID+"/deployments/"+depID, nil, &dg); err != nil {
				return nil, err
			}
			dep := dg.Deployment
			if dep.ID != depID {
				return nil, fmt.Errorf("provider returned deployment %q while observing %q", dep.ID, depID)
			}
			if dep.Phase != lastPhase {
				logf("deployment %s is %s", strs.ShortSHA(dep.ID), strings.ToLower(dep.Phase))
				lastPhase = dep.Phase
			}
			switch dep.Phase {
			case "ACTIVE":
				return &dep, nil
			case "ERROR", "CANCELED", "SUPERSEDED":
				// The failing component's last words, into the release log:
				// the platform's sentence alone says "exited non-zero".
				for _, comp := range failingComponents(dep.Progress.Steps) {
					if !tails {
						logf("%s failed; its log is not copied here because it can hold production data", comp)
						continue
					}
					if tail := d.componentLogTail(ctx, c, appID, dep.ID, comp); tail != "" {
						logf("%s's last lines:\n%s", comp, tail)
					}
				}
				return nil, deploy.Terminal(fmt.Errorf("deployment %s: %s", strings.ToLower(dep.Phase), failingStep(dep.Progress.Steps)))
			}
		}
		if d.options.Now().After(deadline) {
			return nil, fmt.Errorf("the deployment did not become active within 20 minutes (last seen %s)", strings.ToLower(lastPhase))
		}
		if err := waitDeploymentPoll(ctx, d.options.PollInterval); err != nil {
			return nil, err
		}
	}
}

// failingComponents names the components whose deploy step errored.
func failingComponents(steps []doStep) []string {
	var out []string
	var walk func(ss []doStep, underComponents bool)
	walk = func(ss []doStep, underComponents bool) {
		for _, s := range ss {
			if underComponents && s.Status == "ERROR" {
				out = append(out, s.Name)
				continue
			}
			walk(s.Steps, underComponents || s.Name == "components")
		}
	}
	walk(steps, false)
	return out
}

// componentLogTail fetches a component's deploy-time log (the platform
// hands back URLs to gzip-compressed text) and returns its last lines.
func (d *Driver) componentLogTail(ctx context.Context, c *Client, appID, depID, comp string) string {
	var got struct {
		LiveURL      string   `json:"live_url"`
		HistoricURLs []string `json:"historic_urls"`
	}
	if err := c.Call(ctx, "GET", "/v2/apps/"+appID+"/deployments/"+depID+"/components/"+comp+"/logs?type=DEPLOY", nil, &got); err != nil {
		return ""
	}
	urls := got.HistoricURLs
	if len(urls) == 0 && got.LiveURL != "" {
		urls = []string{got.LiveURL}
	}
	for _, u := range urls {
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			continue
		}
		res, err := c.HTTP.Do(req)
		if err != nil {
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		res.Body.Close()
		if zr, err := gzip.NewReader(bytes.NewReader(raw)); err == nil {
			if un, err := io.ReadAll(io.LimitReader(zr, 4<<20)); err == nil {
				raw = un
			}
		}
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		if len(lines) > 30 {
			lines = lines[len(lines)-30:]
		}
		return strings.Join(lines, "\n")
	}
	return ""
}

func failingStep(steps []doStep) string {
	for _, s := range steps {
		if s.Status == "ERROR" {
			if s.Reason != nil && s.Reason.Message != "" {
				return s.Name + ": " + s.Reason.Message
			}
			if inner := failingStep(s.Steps); inner != "" {
				return s.Name + " › " + inner
			}
			return s.Name + " failed"
		}
	}
	return "a step failed; the platform gave no reason"
}

func (d *Driver) observe(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
	o := deploy.Observation{Generation: p.Generation, Rollout: "absent", Health: "unknown", URL: p.URL, Handle: p.Handle}
	appID, _ := p.Handle["appId"].(string)
	if appID == "" {
		return o, nil
	}
	c, _, err := d.Client(ctx)
	if err != nil {
		return o, err
	}
	var got struct {
		App doApp `json:"app"`
	}
	if err := c.Call(ctx, "GET", "/v2/apps/"+appID, nil, &got); err != nil {
		if IsStatus(err, 404) {
			return o, nil
		}
		return o, err
	}
	if got.App.LiveURL != "" {
		o.URL = got.App.LiveURL
	}
	switch {
	case got.App.InProgressDeployment != nil:
		o.Rollout = "rolling"
	case got.App.ActiveDeployment != nil && got.App.ActiveDeployment.Phase == "ACTIVE":
		o.Rollout, o.Health = "active", "healthy"
	case got.App.ActiveDeployment != nil:
		o.Rollout, o.Health = "failed", "unhealthy"
	default:
		// The app is there with nothing deployed yet: not absent.
		o.Rollout = "pending"
	}
	if got.App.ActiveDeployment != nil {
		o.Running = got.App.ActiveDeployment.ID
	}
	return o, nil
}

// teardown deletes the app — never the database (§2.6).
func (d *Driver) teardown(ctx context.Context, p deploy.Placement, generation int64) error {
	if p.Generation != generation {
		return fmt.Errorf("deployment is at generation %d, not %d — refusing to tear down what was not recorded", p.Generation, generation)
	}
	appID, _ := p.Handle["appId"].(string)
	if appID == "" {
		return nil
	}
	c, _, err := d.Client(ctx)
	if err != nil {
		return err
	}
	// Verify the live provider selection too. This is a preflight check, not
	// an atomic provider-side generation fence: DELETE still addresses the app.
	var got struct {
		App doApp `json:"app"`
	}
	if err := c.Call(ctx, "GET", "/v2/apps/"+appID, nil, &got); err != nil {
		if IsStatus(err, 404) {
			return nil
		}
		return err
	}
	expected, _ := p.Handle["deploymentId"].(string)
	if expected == "" || got.App.ID != appID || got.App.ActiveDeployment == nil || got.App.ActiveDeployment.ID != expected || (got.App.PendingDeployment != nil && got.App.PendingDeployment.ID != expected) || (got.App.InProgressDeployment != nil && got.App.InProgressDeployment.ID != expected) {
		return errors.New("provider deployment differs from the recorded selection; refusing app removal")
	}
	if err := c.Call(ctx, "DELETE", "/v2/apps/"+appID, nil, nil); err != nil && !IsStatus(err, 404) {
		return err
	}
	return nil
}

// Provider polling belongs to the caller's cancellation deadline.
func waitDeploymentPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
