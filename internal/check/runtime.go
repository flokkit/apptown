package check

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"flokkit.io/forge/internal/strs"
)

// The runtime evaluator (design/subsystems/conformance.md §2 "runtime";
// healthchecks.md §5.2): a check judged against the app as it actually RUNS,
// not its tree, through the same two doors production traffic and probes use —
// the FRONT DOOR (the public URL, TLS terminated by a proxy, X-Forwarded-* set)
// and NAKED (loopback to the mapped port, no front-door signal at all). A
// readiness endpoint that answers 200 both ways is redirect-safe, which is the
// portability contract §5.2 states; one that 301s the naked probe is the
// office-poll ten-commit thrash waiting to happen on any target whose probe
// cannot carry headers.
//
// Which addresses those two doors have — a standing deployment, a change's
// preview — is the caller's to resolve; this package is given them.
//
// Probe grammar (a check's `probe` field):
//
//	GET <path> [expect <status>]   probe that path
//	readiness                      probe the path the contract declares as the
//	                               public part's readiness (the caller resolves it)
//	browser-env                    a static app's page loads /_apptown/env.js
//	                               whenever the forge gives it values
//	page                           the root a visitor opens, through the front
//	                               door only, is not refused (page.go)
//	memory-declared                each part the apptown runs from a build or
//	                               image declares its memory, in the contract
//	                               being judged (the caller parses it); needs
//	                               no running app
//	memory-within [N%]             each part of the standing deployment uses
//	                               under N% (default 90) of the memory limit
//	                               the forge applied to it, as the caller
//	                               observes it; judged on a standing
//	                               deployment only

// ProbeSpec is a parsed `probe` field.
type ProbeSpec struct {
	Path       string
	Expect     int
	Readiness  bool // resolve the path from the contract's declared readiness
	BrowserEnv bool // judge the page against the forge's /_apptown/env.js
	Page       bool // judge the front page through the front door (JudgePage)
	// MemoryDeclared judges the contract, not the running app.
	MemoryDeclared bool
	// MemoryWithin is the share of its applied limit, in percent, a part of
	// the standing deployment must stay under; 0 when the probe is another.
	MemoryWithin int
}

// DefaultMemoryWithin is memory-within's share when the probe names none.
const DefaultMemoryWithin = 90

// NeedsApp says the probe is answered by a running app. memory-declared is
// answered by the contract alone, so the gate judges it with no preview.
func (ps ProbeSpec) NeedsApp() bool { return !ps.MemoryDeclared }

// Standing says the probe is answered only by a standing deployment: what
// the forge applied to it and observed of it. A freshly booted preview has
// no steady use to judge, so the gate skips it.
func (ps ProbeSpec) Standing() bool { return ps.MemoryWithin > 0 }

func ParseProbe(s string) (ProbeSpec, error) {
	f := strings.Fields(strings.TrimSpace(s))
	switch {
	case len(f) == 1 && strings.EqualFold(f[0], "readiness"):
		return ProbeSpec{Readiness: true, Expect: 200}, nil
	case len(f) == 1 && strings.EqualFold(f[0], "browser-env"):
		return ProbeSpec{BrowserEnv: true}, nil
	case len(f) == 1 && strings.EqualFold(f[0], "page"):
		return ProbeSpec{Page: true}, nil
	case len(f) == 1 && strings.EqualFold(f[0], "memory-declared"):
		return ProbeSpec{MemoryDeclared: true}, nil
	case len(f) == 1 && strings.EqualFold(f[0], "memory-within"):
		return ProbeSpec{MemoryWithin: DefaultMemoryWithin}, nil
	case len(f) == 2 && strings.EqualFold(f[0], "memory-within"):
		n, err := strconv.Atoi(strings.TrimSuffix(f[1], "%"))
		if err != nil || !strings.HasSuffix(f[1], "%") || n < 1 || n > 100 {
			return ProbeSpec{}, fmt.Errorf("probe %q: memory-within takes a share of the limit from 1%% to 100%%, such as 90%%", s)
		}
		return ProbeSpec{MemoryWithin: n}, nil
	case len(f) >= 2 && strings.EqualFold(f[0], "GET") && strings.HasPrefix(f[1], "/"):
		ps := ProbeSpec{Path: f[1], Expect: 200}
		if len(f) == 4 && strings.EqualFold(f[2], "expect") {
			n, err := strconv.Atoi(f[3])
			if err != nil || n < 100 || n > 599 {
				return ps, fmt.Errorf("probe %q: expect wants an HTTP status", s)
			}
			ps.Expect = n
		} else if len(f) != 2 {
			return ps, fmt.Errorf("probe %q: use \"GET /path [expect N]\", \"readiness\", \"browser-env\", \"page\", \"memory-declared\" or \"memory-within [N%%]\"", s)
		}
		return ps, nil
	}
	return ProbeSpec{}, fmt.Errorf("probe %q: use \"GET /path [expect N]\", \"readiness\", \"browser-env\", \"page\", \"memory-declared\" or \"memory-within [N%%]\"", s)
}

// Prober makes one probe request and reports the status it got.
type Prober interface {
	Probe(ctx context.Context, url string) (int, error)
}

// ProberFunc adapts a function to Prober.
type ProberFunc func(ctx context.Context, url string) (int, error)

func (f ProberFunc) Probe(ctx context.Context, url string) (int, error) { return f(ctx, url) }

// probeClient does one probe request without following redirects — a 301 is
// the finding, not something to chase — and with a short timeout.
var probeClient = &http.Client{
	Transport:     ProbeTransport,
	Timeout:       8 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// HTTPProber is the real prober: a plain GET, redirects unfollowed.
var HTTPProber = ProberFunc(func(_ context.Context, url string) (int, error) {
	resp, err := probeClient.Get(url)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
})

// ProbeBothDoors judges a path through the front door and naked. Both must
// answer the expected status: the front door because that is what serves,
// naked because that is what any header-less probe sees (§5.2).
func ProbeBothDoors(ctx context.Context, p Prober, frontDoor, naked string, ps ProbeSpec) (state Outcome, detail string) {
	front := strings.TrimRight(frontDoor, "/") + ps.Path
	nakedURL := strings.TrimRight(naked, "/") + ps.Path
	fc, ferr := p.Probe(ctx, front)
	nc, nerr := p.Probe(ctx, nakedURL)
	line := func(name, url string, code int, err error) string {
		if err != nil {
			return fmt.Sprintf("%s %s → no answer (%s)", name, url, strs.Truncate(err.Error(), 90))
		}
		return fmt.Sprintf("%s %s → %d", name, url, code)
	}
	summary := line("front door", front, fc, ferr) + " · " + line("naked", nakedURL, nc, nerr)
	switch {
	case nerr != nil && (ferr != nil || fc >= 500):
		// Nothing answered by address and the front door is down or failing
		// upstream: the app was not running when the review looked. That is
		// something to fix, but it is not this check's finding (reading-list
		// was once blamed for a redirect it never got the chance to send).
		return CannotAssess, "the app was not running when the review looked, so the probe could not be judged: " + summary
	case nerr != nil:
		return DoesNotConform, "the running app did not answer on its own port: " + summary
	case ferr != nil:
		// The reviewer could not reach the public door — the app may be fine.
		return CannotAssess, "the front door was unreachable from the review: " + summary
	case fc != ps.Expect && nc != ps.Expect:
		return DoesNotConform, fmt.Sprintf("expected %d from both doors: %s", ps.Expect, summary)
	case nc != ps.Expect:
		return DoesNotConform, NakedRefusal(nc, ps.Expect, summary)
	case fc != ps.Expect:
		return DoesNotConform, fmt.Sprintf("the front door answers %d, not %d: %s", fc, ps.Expect, summary)
	}
	return Conforms, summary
}

// NakedRefusal names the cause a header-less probe most likely hit, so the
// finding tells the fixing agent what to exempt. A redirect is the HTTPS
// redirect; a 403 is a host allow-list refusing a request that arrived by
// address (Rails config.hosts, Django ALLOWED_HOSTS); anything else is
// reported as what it is.
func NakedRefusal(code, expect int, summary string) string {
	switch {
	case code >= 300 && code < 400:
		return fmt.Sprintf("not redirect-safe: a probe without the front-door signal gets %d, not %d — exempt this path from the HTTPS redirect (%s)", code, expect, summary)
	case code == 403:
		return fmt.Sprintf("not probe-safe: a probe that arrives by address, not by hostname, gets 403, not %d — the app's host allow-list refuses it; exempt this path from host authorization (Rails: config.host_authorization exclude; Django: allow the probe path or the container address) (%s)", expect, summary)
	}
	return fmt.Sprintf("not probe-safe: a probe without the front-door signal gets %d, not %d — the path must answer %d to a plain request by address, which is how every cloud target's health probe reaches the app (%s)", code, expect, expect, summary)
}
