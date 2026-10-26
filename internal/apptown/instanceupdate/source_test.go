package instanceupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestDiscoverySelectsCompleteExplicitChannelWithoutPrivateData(t *testing.T) {
	for _, tc := range []struct{ channel, want string }{{"stable", "v1.2.0"}, {"prerelease", "v2.0.0-rc.1"}} {
		t.Run(tc.channel, func(t *testing.T) {
			requests := 0
			s := releaseSource{http: newPublicHTTP(transportFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.URL.String() != "https://api.github.com/repos/flokkit/apptown/releases?per_page=100&page=1" || r.Method != "GET" || r.Body != nil || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Fatalf("unexpected discovery request: %v", r)
				}
				return response(200, `[{"tag_name":"v1.1.0"},{"tag_name":"v2.0.0-rc.1","prerelease":true},{"tag_name":"v1.2.0"},{"tag_name":"v99.0.0","draft":true}]`), nil
			}))}
			got, pages, err := s.discover(context.Background(), tc.channel, nil)
			if err != nil || got != tc.want || requests != 1 || len(pages) != 1 {
				t.Fatalf("got %s pages %d requests %d: %v", got, len(pages), requests, err)
			}
		})
	}
}

func TestDiscoveryNeverReportsNoUpdateFromPartialOrInvalidEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		code       int
	}{
		{"null", "null", 200}, {"invalid", "not JSON", 200}, {"duplicate", `[{"tag_name":"v1.0.0"},{"tag_name":"v1.0.0"}]`, 200},
		{"ambiguous", `[{"tag_name":"v1.0.0+a"},{"tag_name":"v1.0.0+b"}]`, 200}, {"rate limited", "[]", 429}, {"uncached unchanged", "", 304},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := releaseSource{http: newPublicHTTP(transportFunc(func(*http.Request) (*http.Response, error) { return response(tc.code, tc.body), nil }))}
			if _, _, err := s.discover(context.Background(), "stable", nil); err == nil {
				t.Fatal("accepted incomplete release evidence")
			}
		})
	}
	t.Run("pagination cap", func(t *testing.T) {
		page := 0
		s := releaseSource{http: newPublicHTTP(transportFunc(func(*http.Request) (*http.Response, error) {
			rows := make([]releaseMetadata, 100)
			for i := range rows {
				rows[i].Tag = fmt.Sprintf("v1.%d.%d", page, i)
			}
			page++
			raw, _ := json.Marshal(rows)
			return response(200, string(raw)), nil
		}))}
		if _, _, err := s.discover(context.Background(), "stable", nil); err == nil || page != 5 {
			t.Fatalf("partial discovery accepted: pages %d, %v", page, err)
		}
	})
}

func TestDiscoveryConditionalRequestRequiresRetainedBody(t *testing.T) {
	body := json.RawMessage(`[{"tag_name":"v1.2.0"}]`)
	s := releaseSource{http: newPublicHTTP(transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("If-None-Match") != "retained" {
			t.Fatal("lost conditional validator")
		}
		return response(304, ""), nil
	}))}
	got, _, err := s.discover(context.Background(), "stable", map[string]pageCache{"1": {ETag: "retained", Body: body}})
	if err != nil || got != "v1.2.0" {
		t.Fatalf("retained metadata: %s %v", got, err)
	}
}

func TestPublicReleaseHTTPRefusesForeignRedirectAndOversizedDocument(t *testing.T) {
	calls := 0
	h := newPublicHTTP(transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		r := response(302, "")
		r.Header.Set("Location", "https://foreign.invalid/leak")
		return r, nil
	}))
	if _, _, _, err := h.get(context.Background(), "https://api.github.com/repos/flokkit/apptown/releases", "", 100); err == nil || calls != 1 {
		t.Fatalf("foreign redirect: calls %d, %v", calls, err)
	}
	h = newPublicHTTP(transportFunc(func(*http.Request) (*http.Response, error) { return response(200, strings.Repeat("x", 101)), nil }))
	if _, _, _, err := h.get(context.Background(), "https://api.github.com/repos/flokkit/apptown/releases", "", 100); err == nil {
		t.Fatal("accepted oversized document")
	}
}

func TestMissingMetadataIsDistinctFromFailedAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name       string
		code       int
		body, want string
	}{
		{"legacy metadata missing", http.StatusNotFound, "", "metadata-missing"},
		{"invalid authenticated metadata", http.StatusOK, "attacker text", "authentication-failed"},
		{"source unavailable", http.StatusServiceUnavailable, "attacker text", "check-unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := releaseSource{http: newPublicHTTP(transportFunc(func(*http.Request) (*http.Response, error) { return response(tc.code, tc.body), nil }))}
			_, err := s.fetch(context.Background(), "v1.2.0")
			if err == nil {
				t.Fatal("incomplete metadata accepted")
			}
			code, message := failureReason(err)
			if code != tc.want || strings.Contains(message, "attacker") {
				t.Fatalf("safe distinct reason: %q %q", code, message)
			}
		})
	}
}
