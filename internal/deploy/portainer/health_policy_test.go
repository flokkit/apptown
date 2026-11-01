package portainer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/flokkit/apptown/internal/poll"

	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

func TestPolicyReadinessRequiresConsecutiveSameRuntime(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tokens []string
		miss   int
		want   int
	}{
		{"consecutive", []string{"a", "a"}, 0, 2},
		{"replacement", []string{"a", "b", "b"}, 0, 3},
		{"miss", []string{"a", "a", "a", "a"}, 2, 4},
		{"unknown-runtime", []string{"", "a", "a"}, 0, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
				defer cancel()
				policy := &appcontract.HealthPolicy{Explicit: true, Period: 5 * time.Second, Timeout: time.Second, SuccessThreshold: 2, FailureThreshold: 1}
				calls := 0
				start := time.Now()
				out, err := awaitPolicy(ctx, policy, func(context.Context) (deploy.Observation, error) {
					calls++
					if calls > len(tc.tokens) {
						t.Fatal("unexpected extra sample")
					}
					if time.Since(start) != time.Duration(calls-1)*5*time.Second {
						t.Fatal("period was not measured from sample start")
					}
					_ = poll.Wait(context.Background(), time.Second)
					o := deploy.Observation{RuntimeToken: tc.tokens[calls-1], Health: "healthy", HealthObservedAt: time.Now(), ArtifactHash: "artifact"}
					if calls == tc.miss {
						return o, errors.New("unavailable")
					}
					return o, nil
				})
				if err != nil || calls != tc.want || out.Health != "healthy" {
					t.Fatalf("calls%d out%+v err%v", calls, out, err)
				}
			})
		})
	}
}

func TestPolicyReadinessDeadlineBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		budget    time.Duration
		slow      bool
		wantCalls int
		success   bool
	}{
		{"exact-fit", 90 * time.Second, false, 3, true},
		{"insufficient-last-probe", 89 * time.Second, false, 2, false},
		{"late-success", 90 * time.Second, true, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), tc.budget)
				defer cancel()
				p := &appcontract.HealthPolicy{Explicit: true, Period: 40 * time.Second, Timeout: 10 * time.Second, SuccessThreshold: 3, FailureThreshold: 1}
				if err := p.CheckBootFit(); err != nil {
					t.Fatal(err)
				}
				calls := 0
				out, err := awaitPolicy(ctx, p, func(context.Context) (deploy.Observation, error) {
					calls++
					if tc.slow && calls == 3 {
						_ = poll.Wait(context.Background(), 11*time.Second)
					}
					return deploy.Observation{RuntimeToken: "a", Health: "healthy", HealthObservedAt: time.Now(), ArtifactHash: "artifact", Handle: map[string]any{"readinessObservedAt": time.Now()}}, nil
				})
				if calls != tc.wantCalls || (err == nil) != tc.success {
					t.Fatalf("calls%d err%v", calls, err)
				}
				if !tc.success && (out.Health == "healthy" || !out.HealthObservedAt.IsZero() || out.ArtifactHash != "") {
					t.Fatalf("incomplete streak retained health: %+v", out)
				}
			})
		})
	}
}

func TestReadinessPolicyArtifactVersionAndDefaults(t *testing.T) {
	compose, r := readinessFixture(t)
	p, err := PublicHealthPolicy(nil)
	if err != nil || p.Explicit || p.Period != time.Second || p.Timeout != 8*time.Second || p.SuccessThreshold != 1 {
		t.Fatalf("defaults %+v %v", p, err)
	}
	p.Explicit = true
	p.SuccessThreshold = 2
	r.Public.Policy = &p
	if _, err := WithReadiness(compose, r); err == nil {
		t.Fatal("v1 accepted policy")
	}
	r.Version = 2
	body, err := WithReadiness(compose, r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := artifactReadiness(body)
	if err != nil || got.Public.Policy == nil || *got.Public.Policy != p {
		t.Fatalf("roundtrip %+v %v", got, err)
	}
	for _, mutate := range []func(*appcontract.HealthPolicy){func(p *appcontract.HealthPolicy) { p.Period = 2 * time.Second }, func(p *appcontract.HealthPolicy) { p.Timeout = 11 * time.Second }, func(p *appcontract.HealthPolicy) { p.SuccessThreshold = 0 }, func(p *appcontract.HealthPolicy) { p.Explicit = false }} {
		bad := p
		mutate(&bad)
		r.Public.Policy = &bad
		if _, err := WithReadiness(compose, r); err == nil {
			t.Fatalf("accepted invalid %+v", bad)
		}
	}
	r.Public.Policy = nil
	if _, err := WithReadiness(compose, r); err == nil {
		t.Fatal("v2 accepted absent policy")
	}
}

func TestPolicyDiagnosticSharesTimeoutWithStaticIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		delay   time.Duration
		static  bool
		success bool
	}{
		{"six-second-response", 6 * time.Second, false, true},
		{"two-requests-one-budget", 6 * time.Second, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, r := readinessFixture(t)
				p := r.Public
				p.Policy = &appcontract.HealthPolicy{Explicit: true, Period: time.Second, Timeout: 10 * time.Second, SuccessThreshold: 1, FailureThreshold: 1}
				if tc.static {
					p.StaticRelease = "release"
				}
				calls := 0
				daemon := containerresulttest.Daemon{Answers: func(c containerresulttest.Command) (containerresulttest.Answer, bool) {
					calls++
					if !slices.Contains(c.Env, "PROBE_TIMEOUT=10") {
						t.Errorf("admitted timeout absent: %v", c.Env)
					}
					return containerresulttest.Answer{Answer: []byte("204\n")}, true
				}}
				start := time.Now()
				d := Diagnostic{allowed: func(ctx context.Context) error { return ctx.Err() }, Command: func(ctx context.Context, env []string, args ...string) ([]byte, error) {
					if args[0] == "exec" {
						select {
						case <-time.After(tc.delay):
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					}
					out, err, ok := daemon.RunEnv(args, env)
					if !ok {
						return nil, fmt.Errorf("unexpected %v", args)
					}
					return out, err
				}}
				err := d.sample(t.Context(), strings.Repeat("a", 64), "run-1", "dep-1", p)
				if (err == nil) != tc.success {
					t.Fatalf("calls%d elapsed%v err%v", calls, time.Since(start), err)
				}
				if tc.static && time.Since(start) != 10*time.Second {
					t.Fatalf("identity request received a new budget: %v", time.Since(start))
				}
			})
		})
	}
}

func TestPortainerRefusesUnsupportedHealthOnEveryPart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		public  bool
		health  *appcontract.Health
		allowed bool
	}{
		{"public-http", true, &appcontract.Health{Readiness: &appcontract.Probe{HTTP: "/ready", Timeout: "10s"}}, true},
		{"public-tcp", true, &appcontract.Health{Readiness: &appcontract.Probe{TCP: 80}}, false},
		{"private-http", false, &appcontract.Health{Readiness: &appcontract.Probe{HTTP: "/ready"}}, false},
		{"worker-liveness", false, &appcontract.Health{Liveness: &appcontract.Probe{HTTP: "/live"}}, false},
		{"public-liveness", true, &appcontract.Health{Liveness: &appcontract.Probe{HTTP: "/live"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &appcontract.Contract{Parts: []appcontract.Part{{Name: "app", Public: true}, {Name: "checked", Public: tc.public, Intent: tc.health}}}
			if tc.public {
				c.Parts = c.Parts[1:]
			}
			err := RefusesHealth(c)
			if (err == nil) != tc.allowed {
				t.Fatalf("err%v", err)
			}
		})
	}
}
