package docker

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/poll"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestColdPolicyConsecutiveRuntimeAndDeadline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tokens  []string
		miss    int
		budget  time.Duration
		slow    bool
		calls   int
		success bool
	}{
		{"stable", []string{"a", "a"}, 0, 90 * time.Second, false, 2, true},
		{"replacement", []string{"a", "b", "b"}, 0, 90 * time.Second, false, 3, true},
		{"miss", []string{"a", "a", "a", "a"}, 2, 90 * time.Second, false, 4, true},
		{"budget", []string{"a", "a"}, 0, 5 * time.Second, false, 1, false},
		{"late", []string{"a", "a"}, 0, 6 * time.Second, true, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), tc.budget)
				defer cancel()
				p := &appcontract.HealthPolicy{Explicit: true, Period: 5 * time.Second, Timeout: time.Second, SuccessThreshold: 2, FailureThreshold: 1}
				calls := 0
				start := time.Now()
				got := coldPolicyReady(ctx, p, 90*time.Second, func(one context.Context) (string, bool) {
					calls++
					if calls > len(tc.tokens) {
						t.Fatal("unexpected sample")
					}
					if time.Since(start) != time.Duration(calls-1)*5*time.Second {
						t.Fatal("cadence drift")
					}
					if tc.slow && calls == 2 {
						_ = poll.Wait(context.Background(), 2*time.Second)
					}
					return tc.tokens[calls-1], calls != tc.miss
				})
				if got != tc.success || calls != tc.calls {
					t.Fatalf("ready%v calls%d", got, calls)
				}
			})
		})
	}
}

type coldClient func(context.Context, string, Command) ([]byte, error)

func (f coldClient) Run(ctx context.Context, host string, c Command) ([]byte, error) {
	return f(ctx, host, c)
}

func TestExplicitColdHTTPUsesPolicyTimeoutAndRuntime(t *testing.T) {
	for _, tc := range []struct {
		name           string
		timeout, delay time.Duration
		replace        bool
		success        bool
	}{
		{"six-seconds", 10 * time.Second, 6 * time.Second, false, true},
		{"identity-overhead", time.Second, 800 * time.Millisecond, false, true},
		{"short-timeout", time.Second, 2 * time.Second, false, false},
		{"changed-runtime", time.Second, 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-time.After(tc.delay):
					w.WriteHeader(204)
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			inspections := 0
			dk := Scope{Client: coldClient(func(ctx context.Context, _ string, c Command) ([]byte, error) {
				if c.Args[0] == "logs" {
					return []byte("not ready"), nil
				}
				if c.Args[0] != "inspect" {
					t.Fatalf("unexpected %v", c.Args)
				}
				inspections++
				if tc.name == "identity-overhead" {
					_ = poll.Wait(context.Background(), 200*time.Millisecond)
				}
				id := "a"
				if tc.replace && inspections > 1 {
					id = "b"
				}
				return []byte(fmt.Sprintf(`{"id":%q,"running":true,"state":{"startedAt":"2026-10-26T00:00:00Z"}}`, id)), nil
			})}
			ctx, cancel := context.WithTimeout(t.Context(), tc.timeout+2*time.Second)
			defer cancel()
			p := &appcontract.HealthPolicy{Explicit: true, Period: 5 * time.Second, Timeout: tc.timeout, SuccessThreshold: 1, FailureThreshold: 1}
			got, _ := (&Probe{}).Check(ctx, dk, "app", server.URL, 90*time.Second, RenderedProbe{Path: "/ready", Expect: 204, Policy: p})
			if got != tc.success {
				t.Fatalf("ready%v inspections%d", got, inspections)
			}
		})
	}
}

func TestExplicitSealedColdUsesOwnedProbeAndRuntimeStreak(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, miss := range []bool{false, true} {
			synctest.Test(t, func(t *testing.T) {
				_, cancel := context.WithCancel(t.Context())
				defer cancel()
				probes := 0
				daemon := containerresulttest.Daemon{Answers: func(c containerresulttest.Command) (containerresulttest.Answer, bool) {
					probes++
					if !slices.Contains(c.Env, "PROBE_TIMEOUT=10") {
						t.Errorf("timeout absent: %v", c.Env)
					}
					at := slices.Index(c.Args, "--network")
					if at < 0 || c.Args[at+1] != "isolated" || c.Image != "pinned-probe" {
						t.Errorf("wrong diagnostic scope: %+v", c)
					}
					if miss && probes == 1 {
						return containerresulttest.Answer{Code: 3}, true
					}
					return containerresulttest.Answer{Answer: []byte("204")}, true
				}}
				dk := Scope{Client: coldClient(func(ctx context.Context, _ string, c Command) ([]byte, error) {
					if c.Args[0] == "inspect" && slices.Contains(c.Args, RuntimeFormat) {
						return []byte(`{"id":"a","running":true,"state":{"startedAt":"2026-10-26T00:00:00Z"}}`), nil
					}
					if c.Args[0] == "ps" {
						t.Fatal("sealed diagnostic swept unrelated containers")
					}
					if cleanup && c.Args[0] == "rm" {
						return nil, fmt.Errorf("owned cleanup unavailable")
					}
					out, err, ok := daemon.RunEnv(c.Args, c.Env)
					if !ok {
						return nil, fmt.Errorf("unexpected %v", c.Args)
					}
					return out, err
				})}
				p := &appcontract.HealthPolicy{Explicit: true, Period: 5 * time.Second, Timeout: 10 * time.Second, SuccessThreshold: 2, FailureThreshold: 1}
				s := &Sealed{ProbeImage: "pinned-probe", Host: "app.example"}
				ready, err := s.awaitReady(t.Context(), dk, "isolated", "app", 80, RenderedProbe{Path: "/ready", Expect: 204, Policy: p}, 90*time.Second)
				if cleanup {
					if ready || err == nil || !strings.Contains(err.Error(), "owned cleanup unavailable") || probes != 1 {
						t.Fatalf("cleanup falsely settled ready%v probes%d err%v", ready, probes, err)
					}
					return
				}
				want := 2
				if miss {
					want = 3
				}
				if !ready || err != nil || probes != want {
					t.Fatalf("probes%d", probes)
				}
			})
		}
	}
}

// A sealed boot (a restore rehearsal's) is judged by the readiness rule
// every target applies (appcontract.Ready): with nothing declared, a 500
// at the health path is not ready, in either branch. It used to be.
func TestASealedBootJudgesByTheReadinessRule(t *testing.T) {
	for _, policy := range []*appcontract.HealthPolicy{nil, {Explicit: true, Period: 5 * time.Second, Timeout: 10 * time.Second, SuccessThreshold: 1, FailureThreshold: 1}} {
		synctest.Test(t, func(t *testing.T) {
			daemon := containerresulttest.Daemon{Answers: func(containerresulttest.Command) (containerresulttest.Answer, bool) {
				return containerresulttest.Answer{Answer: []byte("500")}, true
			}}
			dk := Scope{Client: coldClient(func(ctx context.Context, _ string, c Command) ([]byte, error) {
				if c.Args[0] == "inspect" {
					if slices.Contains(c.Args, RuntimeFormat) {
						return []byte(`{"id":"a","running":true,"state":{"startedAt":"2026-10-26T00:00:00Z"}}`), nil
					}
					return []byte("true"), nil
				}
				out, err, ok := daemon.RunEnv(c.Args, c.Env)
				if !ok {
					return nil, fmt.Errorf("unexpected %v", c.Args)
				}
				return out, err
			})}
			s := &Sealed{ProbeImage: "pinned-probe", Host: "app.example"}
			ready, _ := s.awaitReady(t.Context(), dk, "isolated", "app", 80, RenderedProbe{Path: "/healthz", Policy: policy}, 20*time.Second)
			if ready {
				t.Fatalf("a 500 at the health path (policy %v) read as ready", policy != nil)
			}
		})
	}
}
