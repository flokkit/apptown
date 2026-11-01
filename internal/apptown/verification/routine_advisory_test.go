package verification

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/routinechange"
)

const fixedRoutineAdvisory = `{"id":"GHSA-example","modified":"2026-10-01T00:00:00Z","affected":[{"package":{"ecosystem":"npm","name":"example"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"1.0.1"}]}]}]}`

func TestRoutineAdvisoryOnlyAnnotatesAlreadyEligibleUpdates(t *testing.T) {
	data, _, source := routineRepository(t)
	inspector := NewRoutineInspector(data, nil, routineExecutor(t, nil))
	// Reuse the admitted source-owner lock; this test changes advisory evidence only.
	inspector.locks = &scm.RepositoryLocks{}
	calls := 0
	inspector.advisoryRead = func(_ context.Context, method, path string, body []byte) ([]byte, error) {
		calls++
		if path == "query" {
			if method != "POST" || !strings.Contains(string(body), `"version":"1.0.0"`) {
				t.Fatalf("query scope %s %s %s", method, path, body)
			}
			return []byte(`{"vulns":[{"id":"GHSA-example"}]}`), nil
		}
		if method != "GET" || path != "vulns/GHSA-example" || len(body) != 0 {
			t.Fatalf("unbounded advisory request %s %s", method, path)
		}
		return []byte(fixedRoutineAdvisory), nil
	}
	categories := workflow.StandingCategories{Dependencies: "patch", SecurityFixes: true}
	got, err := inspector.InspectRoutine(t.Context(), source, categories)
	if err != nil || got.Classification != "routine-dependency" || got.Advisories == nil || got.Advisories.Status != "observed-fixes" || len(got.Advisories.Fixes) != 1 || got.Advisories.Fixes[0].BodyDigest != digestNPM([]byte(fixedRoutineAdvisory)) || calls != 2 {
		t.Fatalf("fixed advisory evidence %+v %v calls=%d", got, err, calls)
	}
	calls = 0
	categories.Dependencies = ""
	got, err = inspector.InspectRoutine(t.Context(), source, categories)
	if err != nil || got.Classification != "requires-person" || calls != 0 {
		t.Fatalf("advisory bypassed dependency permission %+v %v", got, err)
	}
	categories.Dependencies = "patch"
	inspector.advisoryRead = func(context.Context, string, string, []byte) ([]byte, error) {
		return nil, errors.New("service unavailable")
	}
	got, err = inspector.InspectRoutine(t.Context(), source, categories)
	if err != nil || got.Classification != "routine-dependency" || got.Advisories.Status != "unavailable" || len(got.Advisories.Fixes) != 0 {
		t.Fatalf("unavailable advisory was a clean bill of health %+v %v", got, err)
	}
}

func TestRoutineAdvisoryRejectsIdentityAndFixedBoundaryMismatch(t *testing.T) {
	candidate, err := routinechange.CompareNPM(routineFixture("1.0.0"), routineFixture("1.0.1"), "patch")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []string{"changed identity", "still affected", "withdrawn", "query null"} {
		t.Run(test, func(t *testing.T) {
			inspector := routineInspector{advisoryRead: func(_ context.Context, _ string, path string, _ []byte) ([]byte, error) {
				if path == "query" {
					if test == "query null" {
						return []byte("null"), nil
					}
					return []byte(`{"vulns":[{"id":"GHSA-example"}]}`), nil
				}
				raw := fixedRoutineAdvisory
				switch test {
				case "changed identity":
					raw = strings.Replace(raw, "GHSA-example", "GHSA-other", 1)
				case "still affected":
					raw = strings.Replace(raw, `"ranges":`, `"versions":["1.0.1"],"ranges":`, 1)
				case "withdrawn":
					raw = strings.Replace(raw, `"modified":`, `"withdrawn":"2026-10-02T00:00:00Z","modified":`, 1)
				}
				return []byte(raw), nil
			}}
			got := inspector.observeAdvisories(t.Context(), candidate)
			if got.Status == "observed-fixes" || len(got.Fixes) != 0 || got.Status == "no-supported-fix-observed" {
				t.Fatalf("untrustworthy advisory %+v", got)
			}
		})
	}
}

type advisoryRoundTrip func(*http.Request) (*http.Response, error)

func (f advisoryRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestRoutineAdvisoryHTTPRefusesRedirectAndOversizedAnswers(t *testing.T) {
	original := routineAdvisoryHTTP
	t.Cleanup(func() { routineAdvisoryHTTP = original })
	for _, status := range []int{http.StatusFound, http.StatusServiceUnavailable, http.StatusOK} {
		client := *original
		client.Transport = advisoryRoundTrip(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "api.osv.dev" || r.URL.Scheme != "https" || r.Header.Get("Authorization") != "" {
				t.Fatalf("wrong advisory authority: %s", r.URL)
			}
			body := "{}"
			if status == http.StatusOK {
				body = strings.Repeat("x", (2<<20)+1)
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://other.invalid/"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})
		routineAdvisoryHTTP = &client
		if _, err := readRoutineAdvisory(t.Context(), "POST", "query", nil); err == nil {
			t.Fatalf("HTTP%d/oversize accepted", status)
		}
	}
}
