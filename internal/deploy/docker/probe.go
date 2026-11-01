package docker

import (
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"time"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// renderedProbe is one deployment's concrete readiness probe, derived from a
// part's intent (or a synthesized default) shaped by the ingress contract. It is
// what probeApp actually executes — the render's whole job is turning topology-
// neutral intent into this.
type renderedProbe struct {
	Policy      *appcontract.HealthPolicy
	PolicyError error
	Path        string            // HTTP path to hit (default "/")
	Headers     map[string]string // front-door shaping (ingress.go), nil = probe naked
	Expect      int               // required HTTP status; 0 = below 400 (appcontract.Ready)
	Release     string            // a release identity the answer must serve (appcontract.ServesRelease); "" = none
	Grace       time.Duration     // startupGrace floor — extends the probe deadline
	// The controller's liveness variants (probeApp/cold-boot uses only the HTTP
	// fields above): a TCP connect, or a command run inside the container.
	TCP  bool     // dial the port instead of an HTTP GET
	Exec []string // `docker exec` this command; exit 0 = healthy
}

// renderColdBootProbe derives the cold-boot READINESS probe for the public part
// (design/subsystems/healthchecks.md §4.3, cold-boot row). Cold-boot readiness is what bit
// us: an app hardened for behind-the-proxy 301s a naked hit, so the probe must
// reproduce the front-door signal (frontDoor) the ingress contract supplies.
//
// Inputs are pure: the public part (nil when the contract could not be read)
// and the deployment's front-door headers (ingress.go). Declaring nothing
// yields the §8 default — GET / shaped by the front door — so a naive app boots
// without writing a probe line. Topology (the header) never comes from the app.
func RenderColdBootProbe(pub *appcontract.Part, frontDoor map[string]string) renderedProbe {
	rp := renderedProbe{Path: "/"}
	policy, err := readinessPolicy(pub)
	rp.PolicyError = err
	if policy.Explicit {
		rp.Policy = &policy
	}
	var r *appcontract.Probe
	if pub != nil && pub.Intent != nil {
		r = pub.Intent.Readiness
	}
	// A part with no readiness intent gets the bootstrap default (§8): GET /,
	// shaped by the front door because the public part is, by definition, fronted.
	if r == nil {
		rp.Headers = frontDoor
		return rp
	}
	if r.StartupGrace != "" {
		if d, err := appcontract.ParseHealthDuration(r.StartupGrace); err == nil {
			rp.Grace = d
		}
	}
	// Readiness is an HTTP probe: the contract refuses tcp and exec readiness
	// at parse (appcontract.validateProbe), so there is no weaker probe to
	// stand in for a declared one.
	rp.Path = r.HTTP
	rp.Expect = r.Expect
	// Shape unless the app explicitly declared it needs no front-door signal
	// (requestContext: direct). Default for a fronted part is frontDoor (§4.1).
	if r.RequestContext != "direct" {
		rp.Headers = frontDoor
	}
	return rp
}

// staticPageServes: GET / as a visitor opens it must answer below 400 (a
// missing index.html is a 404). What the page holds is the project's: a
// static site need not be a script app, so nothing is inferred from its
// markup: requiring a <script> or <canvas> would be a guess no declaration
// backs.
func staticPageServes(url string) (status int, ok bool, verdict string) {
	client := &http.Client{Transport: check.ProbeTransport, Timeout: 4 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return 0, false, err.Error()
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 400 {
		return resp.StatusCode, false, fmt.Sprintf("GET / returned %d — is index.html committed at the static root?", resp.StatusCode)
	}
	return resp.StatusCode, true, fmt.Sprintf("GET / answered %d", resp.StatusCode)
}

// hostOf is the host a request to u carries when nothing sets another.
func hostOf(u string) string {
	if parsed, err := neturl.Parse(u); err == nil {
		return parsed.Host
	}
	return ""
}
