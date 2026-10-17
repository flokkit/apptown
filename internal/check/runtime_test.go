package check_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/check"
)

// The runtime evaluator judges the app as it RUNS: the declared readiness path
// must answer through the front door and naked (healthchecks.md §5.2).
func TestProbeBothDoors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	defer front.Close()
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.test"+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer redirecting.Close()
	fine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer fine.Close()

	ps := check.ProbeSpec{Path: "/healthz", Expect: 200}
	// Naked door redirects → not redirect-safe, the finding names the fix.
	st, detail := check.ProbeBothDoors(ctx, check.HTTPProber, front.URL, redirecting.URL, ps)
	if st != check.DoesNotConform || !strings.Contains(detail, "not redirect-safe") || !strings.Contains(detail, "301") {
		t.Fatalf("a 301 on the naked probe is the §5.2 finding: %s %q", st, detail)
	}
	// Both doors 200 → conforms, with both answers in the evidence.
	st, detail = check.ProbeBothDoors(ctx, check.HTTPProber, front.URL, fine.URL, ps)
	if st != check.Conforms || !strings.Contains(detail, "front door") || !strings.Contains(detail, "naked") {
		t.Fatalf("both doors answering is conformant: %s %q", st, detail)
	}
	// The app not answering on its own port is the app's problem.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	st, _ = check.ProbeBothDoors(ctx, check.HTTPProber, front.URL, dead.URL, ps)
	if st != check.DoesNotConform {
		t.Fatalf("no answer on the naked port → does-not-conform, got %s", st)
	}
	// The front door unreachable from the reviewer is not the app's fault.
	st, _ = check.ProbeBothDoors(ctx, check.HTTPProber, dead.URL, fine.URL, ps)
	if st != check.CannotAssess {
		t.Fatalf("an unreachable front door is cannot-assess, got %s", st)
	}
}

func TestParseProbeGrammar(t *testing.T) {
	t.Parallel()
	if ps, err := check.ParseProbe("GET /healthz"); err != nil || ps.Path != "/healthz" || ps.Expect != 200 {
		t.Fatalf("GET /healthz: %+v %v", ps, err)
	}
	if ps, err := check.ParseProbe("GET /ready expect 204"); err != nil || ps.Expect != 204 {
		t.Fatalf("expect clause: %+v %v", ps, err)
	}
	if ps, err := check.ParseProbe("readiness"); err != nil || !ps.Readiness {
		t.Fatalf("readiness: %+v %v", ps, err)
	}
	for _, bad := range []string{"", "POST /x", "GET x", "GET /x expect lots",
		"memory-within 0%", "memory-within 101%", "memory-within lots", "memory-within 80", "memory-declared 80%"} {
		if _, err := check.ParseProbe(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

// The memory probes say what answers them: memory-declared is answered by
// the contract, with no running app; memory-within only by a standing
// deployment, at the share it names or 90%. The catalogue says where each
// is judged.
func TestTheMemoryProbesSayWhatAnswersThem(t *testing.T) {
	t.Parallel()
	ps, err := check.ParseProbe("memory-declared")
	if err != nil || !ps.MemoryDeclared || ps.NeedsApp() || ps.Standing() {
		t.Fatalf("memory-declared: %+v %v", ps, err)
	}
	if ps, err = check.ParseProbe("memory-within"); err != nil || ps.MemoryWithin != 90 || !ps.Standing() || !ps.NeedsApp() {
		t.Fatalf("memory-within: %+v %v", ps, err)
	}
	if ps, err = check.ParseProbe("memory-within 80%"); err != nil || ps.MemoryWithin != 80 {
		t.Fatalf("memory-within 80%%: %+v %v", ps, err)
	}
	if ps, err = check.ParseProbe("readiness"); err != nil || !ps.NeedsApp() || ps.Standing() {
		t.Fatalf("readiness needs a running app and no standing deployment: %+v %v", ps, err)
	}
	within := check.Declaration{Name: "w", Evaluator: "runtime", Probe: "memory-within"}
	declared := check.Declaration{Name: "d", Evaluator: "runtime", Probe: "memory-declared"}
	if got := check.JudgedWhere(within); got != "in every review, on the running deployment" {
		t.Errorf("memory-within is judged: %q", got)
	}
	if got := check.JudgedWhere(declared); got != "at landing and in every review" {
		t.Errorf("memory-declared is judged: %q", got)
	}
	// Only the forge holds the contract and the deployment record: the
	// library's own judge says so, and never probes an app for either.
	j := &check.Judge{Probe: check.ProberFunc(func(context.Context, string) (int, error) {
		t.Error("a memory probe reached the app")
		return 200, nil
	})}
	for _, d := range []check.Declaration{within, declared} {
		ev, err := j.Evaluate(t.Context(), []check.Declared{{Declaration: d}}, check.Source{App: "http://app"}, check.Gate)
		if err != nil || len(ev) != 1 || ev[0].Outcome != check.CannotAssess || !ev[0].Fault {
			t.Errorf("%s through the library's judge: %+v", d.Probe, ev)
		}
	}
}

// The finding names the CAUSE a header-less probe hit, so the fixing agent
// knows what to exempt (a redirect, a host allow-list, anything else), and an
// app that was not running at all is "could not judge", never a finding
// against the check.
func TestProbeBothDoorsNamesTheCause(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	serve := func(front, naked int) (string, string) {
		f := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(front) }))
		n := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if naked >= 300 && naked < 400 {
				w.Header().Set("Location", "https://example/")
			}
			w.WriteHeader(naked)
		}))
		t.Cleanup(f.Close)
		t.Cleanup(n.Close)
		return f.URL, n.URL
	}
	ps := check.ProbeSpec{Path: "/healthz", Expect: 200}
	probe := func(front, naked int) (check.Outcome, string) {
		f, n := serve(front, naked)
		return check.ProbeBothDoors(ctx, check.HTTPProber, f, n, ps)
	}
	if st, why := probe(200, 403); st != check.DoesNotConform || !strings.Contains(why, "host allow-list") {
		t.Fatalf("403 by address should name the host allow-list: %s %q", st, why)
	}
	if st, why := probe(200, 301); st != check.DoesNotConform || !strings.Contains(why, "HTTPS redirect") {
		t.Fatalf("a redirect by address should name the HTTPS redirect: %s %q", st, why)
	}
	if st, why := probe(200, 500); st != check.DoesNotConform || !strings.Contains(why, "not probe-safe") {
		t.Fatalf("another status by address is still a finding: %s %q", st, why)
	}
	if st, why := probe(200, 200); st != check.Conforms {
		t.Fatalf("both doors 200 conforms: %s %q", st, why)
	}
	// Nothing listening by address and the front door answering 502: the app
	// was down, and that is not this check's finding.
	downFront, _ := serve(502, 200)
	if st, why := check.ProbeBothDoors(ctx, check.HTTPProber, downFront, "http://127.0.0.1:1", ps); st != check.CannotAssess || !strings.Contains(why, "not running") {
		t.Fatalf("an app that was down is cannot-assess: %s %q", st, why)
	}
	// Nothing listening by address but the front door serving 200: the port
	// is wrong, which a probe would hit — still a finding.
	upFront, _ := serve(200, 200)
	if st, _ := check.ProbeBothDoors(ctx, check.HTTPProber, upFront, "http://127.0.0.1:1", ps); st != check.DoesNotConform {
		t.Fatalf("front door up, nothing by address: still a finding, got %s", st)
	}
}
