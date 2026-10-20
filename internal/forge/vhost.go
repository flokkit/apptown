package forge

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/worker"
)

// Tier domains: hostname routing for previews and standing deployments,
// in-process. Dropping files into Traefik was declined as non-portable:
// it assumed a specific proxy, a specific provider, a shared filesystem.
// The portable mechanism is the oldest one: apptownd routes by Host header.
// change-97.preview.<domain> and <slug>.main.<domain> arrive at apptownd
// however the operator likes — wildcard DNS straight at the box, any
// reverse proxy in front — and apptownd reverse-proxies to the app's local port.
//
// Stateless by construction: container names are deterministic
// (forge-preview-change-97, forge-env-<slug>-main), so the hostname IS
// the lookup key and `docker port` is the source of truth, behind a
// short cache. Nothing is registered, nothing is written, nothing
// dangles when a preview dies.
//
// Unset domains cost nothing: URLs mint as host:port exactly as before
// and the router passes every request straight through.

type tierDomain struct {
	Scheme string // "https" unless the flag value carried an explicit scheme
	Domain string
}

// parseTierDomains validates hostname routing from -tier-domain flags
// (tier=domain, optionally tier=scheme://domain). Known tiers: preview,
// main, and eval, where eval copies' previews and deployments are served
// and nothing else is.
func parseTierDomains(raw []string) (map[string]tierDomain, error) {
	tiers := map[string]tierDomain{}
	for _, kv := range raw {
		tier, val, ok := strings.Cut(kv, "=")
		if !ok || (tier != "preview" && tier != "main" && tier != "eval") {
			return nil, fmt.Errorf("-tier-domain wants preview=<domain>, main=<domain> or eval=<domain>, got %q", kv)
		}
		scheme := "https"
		if s, d, ok := strings.Cut(val, "://"); ok {
			scheme, val = s, d
		}
		tiers[tier] = tierDomain{Scheme: scheme, Domain: strings.TrimSuffix(val, "/")}
	}
	return tiers, nil
}

// Old-version previews: <slug>-release-<n>.preview.<domain> — a past
// release's exact image, booted for a look (handleReleasePreview).
var releasePreviewRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*-release-[0-9]+$`)

// vhostRoute is what a request Host names: an AppTown-owned container, the
// label it was named by, and whether the name was on the eval domain.
type vhostRoute struct {
	container, label string
	eval             bool
}

// vhostContainer maps a request Host to an AppTown-owned container name.
// Only deterministic AppTown names are reachable — a hostile Host header
// cannot aim the proxy at an arbitrary port. The eval domain names the
// same containers as the preview and main domains together; which project
// may be served where is vhostRules's to decide.
func (a *App) vhostContainer(host string) (vhostRoute, bool) {
	return tierRoute(a.cfg.tierDomains, host)
}

// tierRoute is vhostContainer's rule over a set of tier domains.
func tierRoute(tiers map[string]tierDomain, host string) (vhostRoute, bool) {
	host = hostnameOf(host)
	preview := func(label string) bool {
		return docker.IsWorkPreviewLabel(label) || releasePreviewRe.MatchString(label)
	}
	if d, ok := tiers["eval"]; ok {
		if label, found := strings.CutSuffix(host, "."+hostnameOf(d.Domain)); found {
			switch {
			case preview(label):
				return vhostRoute{container: docker.PreviewLabelName(label), label: label, eval: true}, true
			case slugRe.MatchString(label):
				return vhostRoute{container: docker.ContainerName(label, "main"), label: label, eval: true}, true
			}
		}
	}
	if d, ok := tiers["preview"]; ok {
		if label, found := strings.CutSuffix(host, "."+hostnameOf(d.Domain)); found && preview(label) {
			return vhostRoute{container: docker.PreviewLabelName(label), label: label}, true
		}
	}
	if d, ok := tiers["main"]; ok {
		if label, found := strings.CutSuffix(host, "."+hostnameOf(d.Domain)); found && slugRe.MatchString(label) {
			return vhostRoute{container: docker.ContainerName(label, "main"), label: label}, true
		}
	}
	return vhostRoute{}, false
}

// hostnameOf is a host without its port. Routing is by hostname only, on
// both sides: a request's Host may carry a port (change-….preview.localhost:7777,
// or :443), and so may a configured tier domain, whose port is for the
// URLs AppTown mints (preview=http://preview.localhost:7777).
func hostnameOf(host string) string {
	if h, _, ok := strings.Cut(host, ":"); ok {
		return h
	}
	return host
}

// vhostRules is the one routing rule for evals: the eval domain serves
// eval copies only, and the preview and main domains never serve one. It
// resolves a label to its project (a change's or run's preview by its
// record, a release preview or a standing deployment by its slug) and asks
// Project for its purpose. A label that names no project is not served; a record
// that cannot be read is an error, never a yes. Verdicts are kept briefly:
// a label's project and a project's purpose do not change.
type vhostRules struct {
	db       *database.DB
	projects *project.Projects
	now      func() time.Time // AppTown's clock, which ages verdicts; nil is the real one

	mu       sync.Mutex
	verdicts map[vhostRoute]vhostVerdict
}

type vhostVerdict struct {
	serves bool
	at     time.Time
}

func (v *vhostRules) serves(ctx context.Context, route vhostRoute) (bool, error) {
	v.mu.Lock()
	if got, ok := v.verdicts[route]; ok && clock.Or(v.now)().Sub(got.at) < 20*time.Second {
		v.mu.Unlock()
		return got.serves, nil
	}
	v.mu.Unlock()
	projectID, err := v.project(ctx, route.label)
	if err != nil {
		return false, err
	}
	serves := false
	if !projectID.IsZero() {
		purpose, err := v.projects.Purpose(ctx, projectID)
		if err != nil {
			return false, err
		}
		serves = purpose.EvalDomainOnly() == route.eval
	}
	v.mu.Lock()
	if v.verdicts == nil {
		v.verdicts = map[vhostRoute]vhostVerdict{}
	}
	v.verdicts[route] = vhostVerdict{serves: serves, at: clock.Or(v.now)()}
	v.mu.Unlock()
	return serves, nil
}

// project is the project a host label names, 0 when none.
func (v *vhostRules) project(ctx context.Context, label string) (ids.ID, error) {
	if docker.IsWorkPreviewLabel(label) {
		kind, rest, _ := strings.Cut(label, "-")
		id, _ := ids.Parse(rest)
		var projectID ids.ID
		var err error
		if kind == "change" {
			var ch *workflow.Change
			if ch, err = workflow.ReadChangeIn(ctx, v.db, id); ch != nil {
				projectID = ch.ProjectID
			}
		} else {
			var run *workflow.Run
			if run, err = workflow.ReadRunIn(ctx, v.db, id); run != nil {
				projectID = run.ProjectID
			}
		}
		found, err := database.Lookup(err)
		if err != nil || !found {
			return ids.Nil, err
		}
		return projectID, nil
	}
	slug := label
	if releasePreviewRe.MatchString(label) {
		slug = label[:strings.LastIndex(label, "-release-")]
	}
	p, err := v.projects.BySlug(ctx, slug)
	found, err := database.Lookup(err)
	if err != nil || !found {
		return ids.Nil, err
	}
	return p.ID, nil
}

type vhostEntry struct {
	port int
	at   time.Time
}

// vhostPort resolves a container's published host port via docker,
// cached briefly — one subprocess per name per TTL, not per asset load.
func (a *App) vhostPort(ctx context.Context, container string) int {
	a.vhostMu.Lock()
	if e, ok := a.vhostCache[container]; ok && a.cfg.clock.Now().Sub(e.at) < 20*time.Second {
		a.vhostMu.Unlock()
		return e.port
	}
	a.vhostMu.Unlock()
	port, _ := docker.ContainerHostPort(ctx, a.docker, container, 0)
	if port == 0 {
		// A miss is not a fact worth remembering: the container may be
		// seconds from having a port (complaint #40: `apptown preview` printed
		// port 0 twice, then the real one, because a miss was cached 20s).
		return 0
	}
	a.vhostMu.Lock()
	if a.vhostCache == nil {
		a.vhostCache = map[string]vhostEntry{}
	}
	a.vhostCache[container] = vhostEntry{port: port, at: a.cfg.clock.Now()}
	a.vhostMu.Unlock()
	return port
}

// VHost is the outermost handler: requests for a tier hostname are
// reverse-proxied to the app's local port (WebSockets included —
// httputil.ReverseProxy handles the upgrade); everything else falls
// through to AppTown itself. Proxied apps see their own traffic —
// AppTown auth does not apply inside an app's world.
func (a *App) VHost(next http.Handler) http.Handler {
	if len(a.cfg.tierDomains) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := a.vhostContainer(r.Host)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		container := route.container
		if serves, err := a.vhostRules.serves(r.Context(), route); err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "%s is unavailable: AppTown could not read which project it belongs to.\n", r.Host)
			return
		} else if !serves {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, "Nothing is served at %s.\n", r.Host)
			return
		}
		if docker.IsPreviewName(container) {
			// A request to a preview is a look at it: it stops eight hours
			// after the last one (preview.Service.Tend).
			a.Preview.Viewed(r.Context(), container)
		}
		var proxy *httputil.ReverseProxy
		if a.workerPreviews != nil && a.Preview.PreviewServedOnWorker(container) {
			// A change's or run's preview on the worker, or a release's the
			// AppTown booted there: forwarded over the worker's connection,
			// which reaches that preview's app only.
			proxy = a.workerPreviews.Forward(container, a.publicScheme())
		} else if a.docker.Absent() {
			// Never "nothing is running": this AppTown cannot see.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "%s is unavailable: this AppTown has no local Docker, so it serves no app from its own host. An app on an attached Docker host is reached at that host.\n", r.Host)
			return
		} else {
			port := a.vhostPort(r.Context(), container)
			if port == 0 {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprintf(w, "Nothing is running at %s right now.\n\nPreviews stop when their change closes; a standing deployment may still be starting. AppTown itself is at %s://%s.\n",
					r.Host, "https", a.cfg.publicHost)
				return
			}
			target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
			proxy = &httputil.ReverseProxy{Rewrite: func(req *httputil.ProxyRequest) {
				req.SetURL(target)
				req.Out.Host = req.In.Host
				// Reconstruct forwarding headers after hop-by-hop headers are
				// removed, and after every one a visitor sent. Apps need the
				// public HTTPS origin for form/CSRF checks.
				worker.StripForwarding(req.Out.Header)
				req.SetXForwarded()
				req.Out.Header.Set("X-Forwarded-Proto", a.publicScheme())
			}}
		}
		proxy.ModifyResponse = func(resp *http.Response) error {
			// Caching policy on AppTown tiers is topology, not app preference.
			// A preview exists to show the latest edit: always no-cache
			// (ETag revalidation still hands out 304s, so it stays fast).
			// A standing deployment respects an app that set its own
			// policy and fills the silence otherwise — bare file servers
			// send nothing, and browsers' heuristic caching then serves
			// stale files for hours after a deploy.
			if docker.IsPreviewName(container) {
				resp.Header.Set("Cache-Control", "no-cache")
			} else if resp.Header.Get("Cache-Control") == "" {
				resp.Header.Set("Cache-Control", "no-cache")
			}
			return nil
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			// A dead port means the cache outlived the container.
			a.vhostMu.Lock()
			delete(a.vhostCache, container)
			a.vhostMu.Unlock()
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, "The app at %s stopped answering.\n", r.Host)
		}
		proxy.ServeHTTP(w, r)
	})
}

// publicScheme: how apps are reached from a browser — the tier domains'
// scheme (https behind AppTown's TLS edge), else plain http.
func (a *App) publicScheme() string { return a.cfg.routing().Scheme }

// certificateNames answers a TLS proxy's on-demand certificate question (Caddy's
// on_demand_tls ask, in the one-server compose file): may it get a
// certificate for ?domain=? Yes (200) for AppTown's own public host and for
// a tier-domain name this router would serve; no (404) for anything else.
// The names it allows are bounded by AppTown's records, so a stranger who
// sends made-up names through wildcard DNS cannot make the proxy request
// certificates for them. Only a caller on this machine is answered (403
// otherwise); that is not the abuse boundary, since the proxy forwards
// public requests from loopback too: the name rule is. It is served at
// /tls/ask, outside /api/, so that no sign-in mode answers for it: the
// proxy has no credential.
type certificateNames struct {
	publicHost string
	tiers      map[string]tierDomain
	rules      *vhostRules
	projects   *project.Projects
	deployment *deployment.Service // its Releases, read when asked
}

func (c certificateNames) ask(w http.ResponseWriter, r *http.Request) {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err != nil || !isLoopback(host) {
		writeErr(w, http.StatusForbidden, "only a proxy on this machine may ask")
		return
	}
	ok, err := c.certifiable(r.Context(), r.URL.Query().Get("domain"))
	switch {
	case err != nil:
		writeErr(w, http.StatusServiceUnavailable, "AppTown could not read whether it serves that name")
	case ok:
		w.WriteHeader(http.StatusOK)
	default:
		writeErr(w, http.StatusNotFound, "AppTown serves nothing at that name")
	}
}

func isLoopback(host string) bool {
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// certifiable: name is AppTown's public host, or a tier-domain name this
// router serves. A release name (<slug>-release-<n>) must also name a
// recorded release of that project with its number written canonically:
// the router serves any number for an existing project, and a certificate
// for every number would be unbounded.
func (c certificateNames) certifiable(ctx context.Context, name string) (bool, error) {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if name == "" {
		return false, nil
	}
	if name == hostnameOf(c.publicHost) {
		return true, nil
	}
	route, ok := tierRoute(c.tiers, name)
	if !ok {
		return false, nil
	}
	if releasePreviewRe.MatchString(route.label) {
		i := strings.LastIndex(route.label, "-release-")
		slug, digits := route.label[:i], route.label[i+len("-release-"):]
		n, err := strconv.ParseInt(digits, 10, 64)
		if err != nil || strconv.FormatInt(n, 10) != digits {
			return false, nil
		}
		p, err := c.projects.BySlug(ctx, slug)
		if found, err := database.Lookup(err); err != nil || !found {
			return false, err
		}
		_, err = c.deployment.Releases.ByNumber(ctx, p.ID, n)
		if found, err := database.Lookup(err); err != nil || !found {
			return false, err
		}
	}
	return c.rules.serves(ctx, route)
}
