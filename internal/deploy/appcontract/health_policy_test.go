package appcontract_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"gopkg.in/yaml.v3"
)

func policyDefaults() appcontract.HealthPolicy {
	return appcontract.HealthPolicy{Period: 15 * time.Second, Timeout: 2 * time.Second, FailureThreshold: 3, SuccessThreshold: 2, StartupGrace: 30 * time.Second}
}

func probePolicy(t *testing.T, text, purpose string) (appcontract.HealthPolicy, error) {
	t.Helper()
	var probe appcontract.Probe
	if err := yaml.Unmarshal([]byte(text), &probe); err != nil {
		t.Fatal(err)
	}
	return appcontract.NormalizeHealthPolicy(&probe, purpose, policyDefaults())
}

func TestHealthPolicyPreservesTargetDefaultsAndDeclaredOverrides(t *testing.T) {
	defaults := policyDefaults()
	for _, p := range []*appcontract.Probe{nil, {}, {HTTP: "/ready"}, {HTTP: "/ready", StartupGrace: "0s"}} {
		got, err := appcontract.NormalizeHealthPolicy(p, "readiness", defaults)
		if err != nil || got != defaults {
			t.Fatalf("omission changed defaults: %+v %v", got, err)
		}
	}
	got, err := probePolicy(t, "http: /ready\nperiod: 5s\ntimeout: 10s\nfailureThreshold: 50\nsuccessThreshold: 1\nchecksDeps: true\nstartupGrace: 120s", "readiness")
	want := appcontract.HealthPolicy{Explicit: true, Period: 5 * time.Second, Timeout: 10 * time.Second, FailureThreshold: 50, SuccessThreshold: 1, ChecksDeps: true, StartupGrace: 120 * time.Second}
	if err != nil || got != want {
		t.Fatalf("declared policy: %+v %v", got, err)
	}
	got, err = probePolicy(t, "http: /ready\nperiod: 300s\ntimeout: 1s\nfailureThreshold: 1\nsuccessThreshold: 50\nchecksDeps: false", "readiness")
	if err != nil || got.Period != 300*time.Second || got.Timeout != time.Second || got.FailureThreshold != 1 || got.SuccessThreshold != 50 || !got.Explicit || got.ChecksDeps {
		t.Fatalf("upper/lower bounds: %+v %v", got, err)
	}
	if defaults != policyDefaults() {
		t.Fatal("normalization mutated target defaults")
	}
}

func TestHealthPolicyRefusesInvalidWrittenValues(t *testing.T) {
	for _, declaration := range []string{"period: ''", "period: 0s", "period: 4s", "period: 301s", "period: 5.5s", "period: 5", "period: 99999999999999999999s", "timeout: ''", "timeout: 0s", "timeout: 11s", "timeout: 1.5s", "failureThreshold: 0", "failureThreshold: -1", "failureThreshold: 51", "successThreshold: 0", "successThreshold: 51", "startupGrace: -1s"} {
		t.Run(declaration, func(t *testing.T) {
			_, err := probePolicy(t, "http: /ready\n"+declaration, "readiness")
			if err == nil || !strings.Contains(err.Error(), "health.readiness.") {
				t.Fatalf("invalid written policy accepted: %v", err)
			}
		})
	}
	if _, err := probePolicy(t, "http: /live\nchecksDeps: true", "liveness"); err == nil {
		t.Fatal("dependency-driven liveness accepted")
	}
	got, err := probePolicy(t, "http: /live\nchecksDeps: false", "liveness")
	if err != nil || !got.Explicit || got.ChecksDeps {
		t.Fatalf("explicit false: %+v %v", got, err)
	}
	if _, err := appcontract.NormalizeHealthPolicy(nil, "other", policyDefaults()); err == nil {
		t.Fatal("unknown purpose accepted")
	}
	if _, err := appcontract.NormalizeHealthPolicy(nil, "readiness", appcontract.HealthPolicy{}); err == nil {
		t.Fatal("missing target defaults accepted")
	}
}

func TestHealthPolicyBootFitIncludesLastProbeAndCannotOverflow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy appcontract.HealthPolicy
		fits   bool
	}{
		{"seventy", appcontract.HealthPolicy{Explicit: true, Period: 30 * time.Second, Timeout: 10 * time.Second, SuccessThreshold: 3}, true},
		{"hundred", appcontract.HealthPolicy{Explicit: true, Period: 30 * time.Second, Timeout: 10 * time.Second, SuccessThreshold: 4}, false},
		{"exact", appcontract.HealthPolicy{Explicit: true, Period: 40 * time.Second, Timeout: 10 * time.Second, SuccessThreshold: 3}, true},
		{"one nanosecond over", appcontract.HealthPolicy{Explicit: true, Period: 40 * time.Second, Timeout: 10*time.Second + 1, SuccessThreshold: 3}, false},
		{"grace floor", appcontract.HealthPolicy{Explicit: true, Period: 30 * time.Second, Timeout: 10 * time.Second, SuccessThreshold: 4, StartupGrace: 100 * time.Second}, true},
		{"overflow", appcontract.HealthPolicy{Explicit: true, Period: time.Duration(math.MaxInt64), Timeout: time.Second, SuccessThreshold: math.MaxInt}, false},
		{"omitted", appcontract.HealthPolicy{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.policy.CheckBootFit(); (err == nil) != tc.fits {
				t.Fatalf("fit=%v, want %v: %v", err == nil, tc.fits, err)
			}
		})
	}
}

func TestHealthPolicyPresenceSurvivesCapturedJSONWithoutChangingLegacyBytes(t *testing.T) {
	var legacy appcontract.Probe
	if err := yaml.Unmarshal([]byte("http: /ready\nstartupGrace: 90s"), &legacy); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(legacy)
	if err != nil || string(body) != `{"http":"/ready","startupGrace":"90s"}` {
		t.Fatalf("legacy encoding changed: %s %v", body, err)
	}
	for _, declaration := range []string{"checksDeps: false", "period: ''", "timeout: ''", "failureThreshold: 0", "successThreshold: 0"} {
		t.Run(declaration, func(t *testing.T) {
			var p, captured appcontract.Probe
			if err := yaml.Unmarshal([]byte("http: /ready\n"+declaration), &p); err != nil {
				t.Fatal(err)
			}
			before, beforeErr := appcontract.NormalizeHealthPolicy(&p, "readiness", policyDefaults())
			body, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(body, &captured); err != nil {
				t.Fatal(err)
			}
			after, afterErr := appcontract.NormalizeHealthPolicy(&captured, "readiness", policyDefaults())
			if (beforeErr == nil) != (afterErr == nil) || before != after {
				t.Fatalf("captured presence changed: %s before%+v/%v after%+v/%v", body, before, beforeErr, after, afterErr)
			}
			if declaration == "checksDeps: false" && (!after.Explicit || string(body) != `{"http":"/ready","checksDeps":false}`) {
				t.Fatalf("explicit false disappeared: %s %+v", body, after)
			}
		})
	}
	// Supported policy survives the real contract parser as well as captured JSON.
	if _, err := appcontract.Parse([]byte("app:\n  web:\n    image: app:1\n    public: true\n    health:\n      readiness: {http: /ready, period: 5s}\n")); err != nil {
		t.Fatalf("supported policy rejected: %v", err)
	}
}

// A custom probe decoder must not bypass a captured request's strict decoder.
func TestCapturedHealthPolicyStillRefusesUnknownProbeFields(t *testing.T) {
	for _, body := range []string{`{"probe":{"http":"/ready","timeuot":"10s"}}`, `{"probe":{"http":"/ready","written":{"period":true}}}`} {
		var request struct {
			Probe appcontract.Probe `json:"probe"`
		}
		decoder := json.NewDecoder(strings.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err == nil {
			t.Fatalf("unknown probe field bypassed strict request: %s", body)
		}
	}
}
