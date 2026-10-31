package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSCMProviderRequestUsesOperationCancellation(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	finished := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(finished) }))
	defer server.Close()
	client, err := New(server.URL, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.GetMR(ctx, "project", 1); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("provider cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("provider request ignored cancellation")
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not observe cancellation")
	}
}

// Not parallel: the downgrade case installs only its test server's transport.
func TestGitLabCredentialsStayAtTheConfiguredEndpoint(t *testing.T) {
	cases := []struct {
		name            string
		status          int
		sameOrigin, tls bool
	}{
		{"success", http.StatusOK, false, false},
		{"provider-failure", http.StatusForbidden, false, false},
		{"same-origin-loop", http.StatusFound, true, false},
		{"https-downgrade", http.StatusFound, false, true},
	}
	for _, status := range []int{301, 302, 303, 307, 308} {
		cases = append(cases, struct {
			name            string
			status          int
			sameOrigin, tls bool
		}{fmt.Sprintf("cross-origin-%d", status), status, false, false})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var destinationCalls, sourceCalls atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				destinationCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer destination.Close()
			source := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sourceCalls.Add(1)
				if got := r.Header.Get("PRIVATE-TOKEN"); got != "fixture-secret" {
					t.Errorf("initial credential = %q", got)
				}
				if tc.status >= 300 && tc.status < 400 {
					target := destination.URL + "/redirected"
					if tc.sameOrigin {
						target = r.URL.Path
					}
					w.Header().Set("Location", target)
				}
				w.WriteHeader(tc.status)
			}))
			if tc.tls {
				source.StartTLS()
			} else {
				source.Start()
			}
			defer source.Close()
			previous := http.DefaultTransport
			if tc.tls {
				http.DefaultTransport = source.Client().Transport
				defer func() { http.DefaultTransport = previous }()
			}
			client, err := New(source.URL, "fixture-secret")
			if err != nil {
				t.Fatal(err)
			}
			err = client.Do(t.Context(), http.MethodPost, "/projects", map[string]any{"name": "fixture"}, nil)
			redirect := tc.status >= 300 && tc.status < 400
			if redirect {
				if err == nil {
					t.Error("redirect reported success")
				}
			} else {
				if tc.status == http.StatusOK && err != nil {
					t.Fatalf("normal response: %v", err)
				}
				if tc.status == http.StatusForbidden {
					var failure *HTTPError
					if !errors.As(err, &failure) || failure.Status != tc.status {
						t.Fatalf("provider failure: %v", err)
					}
				}
			}
			if got := destinationCalls.Load(); got != 0 {
				t.Errorf("redirect destination received %d requests", got)
			}
			if got := sourceCalls.Load(); got != 1 {
				t.Errorf("configured endpoint received %d requests; want one", got)
			}
		})
	}
}

func TestPartialConfigurationIsBrokenRatherThanAbsent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ base, token, missing string }{
		{"https://gitlab.example", "", "GITLAB_TOKEN"},
		{"", "token", "GITLAB_URL"},
	} {
		_, err := New(tc.base, tc.token)
		if err == nil || errors.Is(err, ErrNotConfigured) || !strings.Contains(err.Error(), tc.missing) {
			t.Errorf("partial configuration must identify missing %s as an error: %v", tc.missing, err)
		}
	}
	_, err := New("", "")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("empty configuration: %v", err)
	}
}
