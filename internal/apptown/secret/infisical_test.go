package secret

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type infisicalHTTP func(*http.Request) (*http.Response, error)

func (f infisicalHTTP) Do(r *http.Request) (*http.Response, error) { return f(r) }
func secretResponse(status int, body string) (*http.Response, error) {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
}
func secretConfig() infisicalSettings {
	return infisicalSettings{URL: "https://secrets.test", ClientID: "identity", ClientSecret: "bootstrap", ProjectID: "project"}
}
func staleSecrets(s *infisical) { s.mu.Lock(); s.fetched = time.Time{}; s.mu.Unlock() }
func awaitSecretSignal(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(3 * time.Second):
		t.Fatal("secret operation did not finish")
	}
}

// A refresh that fails answers its error and no values: the values read
// before are not current. They are kept for masking alone.
//
// Kills: a failed refresh answering the last values it read.
func TestInfisicalCacheIdentityAndAFailedRefreshAnswersNoValues(t *testing.T) {
	t.Parallel()
	var s infisical
	var logins, reads atomic.Int32
	var status atomic.Int32
	client := infisicalHTTP(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			logins.Add(1)
			return secretResponse(200, `{"accessToken":"token","expiresIn":3600}`)
		}
		reads.Add(1)
		if status.Load() != 0 {
			return secretResponse(int(status.Load()), `{"secretValue":"must-not-leak"}`)
		}
		return secretResponse(200, `{"secrets":[{"secretKey":"TOKEN","secretValue":"value"}]}`)
	})
	cfg := secretConfig()
	first, err := s.resolve(context.Background(), cfg, client)
	if err != nil || first.Values["TOKEN"] != "value" || first.Transition != refreshUnchanged {
		t.Fatalf("initial: %+v %v", first, err)
	}
	first.Values["TOKEN"] = "caller-mutation"
	cached, err := s.resolve(context.Background(), cfg, client)
	if err != nil || cached.Values["TOKEN"] != "value" || reads.Load() != 1 || logins.Load() != 1 {
		t.Fatalf("cache alias or miss: %+v %v", cached, err)
	}
	staleSecrets(&s)
	status.Store(503)
	for i := range 2 {
		result, err := s.resolve(context.Background(), cfg, client)
		expected := refreshUnchanged
		if i == 0 {
			expected = refreshFailed
		}
		if err == nil || strings.Contains(err.Error(), "must-not-leak") || result.Values != nil || result.Transition != expected {
			t.Fatalf("failed refresh: %+v %v", result, err)
		}
	}
	if s.lastKnown(cfg)["TOKEN"] != "value" {
		t.Fatal("the last values read are not kept for masking")
	}
	status.Store(0)
	recovered, err := s.resolve(context.Background(), cfg, client)
	if err != nil || recovered.Transition != refreshRecovered || logins.Load() != 1 {
		t.Fatalf("recovery: %+v %v", recovered, err)
	}
	staleSecrets(&s)
	status.Store(401)
	if _, err := s.resolve(context.Background(), cfg, client); err == nil {
		t.Fatal("missing 401 failure")
	}
	status.Store(0)
	if _, err := s.resolve(context.Background(), cfg, client); err != nil || logins.Load() != 2 {
		t.Fatalf("401 did not reauthenticate: %v logins=%d", err, logins.Load())
	}
	// A new bootstrap identity must not receive values cached for the old one.
	cfg.ClientSecret = "rotated"
	status.Store(503)
	result, err := s.resolve(context.Background(), cfg, client)
	if err == nil || result.Values != nil || logins.Load() != 3 {
		t.Fatalf("identity reused old values: %+v %v", result, err)
	}
}

func TestInfisicalEncodesNamespaceAndAuthenticatesRequests(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"clientSecret":"bootstrap"`) {
				t.Error("wrong login body")
			}
			fmt.Fprint(w, `{"accessToken":"token","expiresIn":3600}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer token" || r.URL.Query().Get("secretPath") != "/a & b" || r.URL.Query().Get("environment") != "preview&test" {
			t.Errorf("incorrect request: %s", r.URL)
		}
		fmt.Fprint(w, `{"secrets":[]}`)
	}))
	defer server.Close()
	cfg := secretConfig()
	cfg.URL = server.URL
	cfg.Path = "/a & b"
	cfg.Environment = "preview&test"
	var s infisical
	result, err := s.resolve(context.Background(), cfg, server.Client())
	if err != nil || result.Values == nil || len(result.Values) != 0 {
		t.Fatalf("empty namespace: %+v %v", result, err)
	}
}

type cancellingSecretBody struct {
	ctx     context.Context
	entered chan struct{}
}

func (b cancellingSecretBody) Read([]byte) (int, error) {
	close(b.entered)
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (cancellingSecretBody) Close() error { return nil }

func TestInfisicalCancellationDuringLoginOrSecretBody(t *testing.T) {
	for _, body := range []bool{false, true} {
		t.Run(fmt.Sprint(body), func(t *testing.T) {
			t.Parallel()
			var s infisical
			entered, done := make(chan struct{}), make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := infisicalHTTP(func(r *http.Request) (*http.Response, error) {
				if body && r.Method == "POST" {
					return secretResponse(200, `{"accessToken":"token","expiresIn":3600}`)
				}
				if body {
					return &http.Response{StatusCode: 200, Body: cancellingSecretBody{r.Context(), entered}}, nil
				}
				close(entered)
				<-r.Context().Done()
				return nil, r.Context().Err()
			})
			var result secretResolution
			var err error
			go func() { result, err = s.resolve(ctx, secretConfig(), client); close(done) }()
			awaitSecretSignal(t, entered)
			cancel()
			awaitSecretSignal(t, done)
			if !errors.Is(err, context.Canceled) || result.Values != nil || result.Transition != refreshUnchanged {
				t.Fatalf("cancelled refresh: %+v %v", result, err)
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.flight != nil || s.values != nil || s.failed {
				t.Fatal("cancelled refresh changed cache health or kept ownership")
			}
		})
	}
}

func TestInfisicalWaiterCancelsWithoutCancellingRefresh(t *testing.T) {
	t.Parallel()
	var s infisical
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	client := infisicalHTTP(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			return secretResponse(200, `{"accessToken":"token","expiresIn":3600}`)
		}
		requests.Add(1)
		close(entered)
		select {
		case <-release:
			return secretResponse(200, `{"secrets":[{"secretKey":"K","secretValue":"V"}]}`)
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})
	ownerCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	var owner secretResolution
	var ownerErr error
	go func() { owner, ownerErr = s.resolve(ownerCtx, secretConfig(), client); close(finished) }()
	awaitSecretSignal(t, entered)
	waiterCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	waiter, err := s.resolve(waiterCtx, secretConfig(), client)
	if !errors.Is(err, context.DeadlineExceeded) || waiter.Values != nil {
		t.Fatalf("cancelled waiter: %+v %v", waiter, err)
	}
	select {
	case <-finished:
		t.Fatal("waiter cancelled refresher")
	default:
	}
	close(release)
	awaitSecretSignal(t, finished)
	if ownerErr != nil || owner.Values["K"] != "V" || requests.Load() != 1 {
		t.Fatalf("refresher: %+v %v requests=%d", owner, ownerErr, requests.Load())
	}
}

func TestInfisicalLiveWaiterCanReplaceCancelledRefresher(t *testing.T) {
	t.Parallel()
	var s infisical
	entered, firstDone, waiterDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var logins atomic.Int32
	client := infisicalHTTP(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			if logins.Add(1) == 1 {
				close(entered)
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			return secretResponse(200, `{"accessToken":"token","expiresIn":3600}`)
		}
		return secretResponse(200, `{"secrets":[{"secretKey":"K","secretValue":"V"}]}`)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { s.resolve(ctx, secretConfig(), client); close(firstDone) }()
	awaitSecretSignal(t, entered)
	live, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	var result secretResolution
	var err error
	go func() { result, err = s.resolve(live, secretConfig(), client); close(waiterDone) }()
	cancel()
	awaitSecretSignal(t, firstDone)
	awaitSecretSignal(t, waiterDone)
	if err != nil || result.Values["K"] != "V" || result.Transition != refreshUnchanged || logins.Load() != 2 {
		t.Fatalf("live caller poisoned: %+v %v logins=%d", result, err, logins.Load())
	}
}

func TestInfisicalRejectsIncompleteResponsesWithoutReplacingCache(t *testing.T) {
	for _, body := range []string{`{}`, `{"secrets":null}`, `{"secrets":[`, strings.Repeat("x", (4<<20)+1)} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			t.Parallel()
			var s infisical
			cfg := secretConfig()
			client := infisicalHTTP(func(r *http.Request) (*http.Response, error) {
				if r.Method == "POST" {
					return secretResponse(200, `{"accessToken":"token","expiresIn":3600}`)
				}
				return secretResponse(200, body)
			})
			result, err := s.resolve(context.Background(), cfg, client)
			if err == nil || result.Values != nil || result.Transition != refreshFailed {
				t.Fatalf("accepted incomplete secrets: %+v %v", result, err)
			}
		})
	}
}

func TestInfisicalCancelledRefreshPreservesWarmCache(t *testing.T) {
	t.Parallel()
	var s infisical
	cfg := secretConfig()
	ready := infisicalHTTP(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			return secretResponse(200, `{"accessToken":"token","expiresIn":3600}`)
		}
		return secretResponse(200, `{"secrets":[{"secretKey":"K","secretValue":"kept"}]}`)
	})
	if _, err := s.resolve(context.Background(), cfg, ready); err != nil {
		t.Fatal(err)
	}
	staleSecrets(&s)
	entered, done := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blocked := infisicalHTTP(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	var cancelled secretResolution
	var err error
	go func() { cancelled, err = s.resolve(ctx, cfg, blocked); close(done) }()
	awaitSecretSignal(t, entered)
	cancel()
	awaitSecretSignal(t, done)
	if !errors.Is(err, context.Canceled) || cancelled.Values != nil || cancelled.Transition != refreshUnchanged {
		t.Fatalf("cancelled: %+v %v", cancelled, err)
	}
	if s.lastKnown(cfg)["K"] != "kept" {
		t.Fatal("cancellation dropped the values read before")
	}
	unavailable := infisicalHTTP(func(*http.Request) (*http.Response, error) { return secretResponse(503, "private") })
	failed, err := s.resolve(context.Background(), cfg, unavailable)
	if err == nil || failed.Values != nil || failed.Transition != refreshFailed {
		t.Fatalf("cancellation changed cache or health: %+v %v", failed, err)
	}
}

// The cache and the login token age on the cache's clock: moving it past
// the minute refreshes the values, and past the token's life logs in again,
// whatever the machine's clock says.
func TestInfisicalAgesOnItsClock(t *testing.T) {
	t.Parallel()
	var at atomic.Int64
	at.Store(time.Now().Add(-72 * time.Hour).Unix())
	s := infisical{clock: func() time.Time { return time.Unix(at.Load(), 0) }}
	var logins, reads atomic.Int32
	client := infisicalHTTP(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			logins.Add(1)
			return secretResponse(200, `{"accessToken":"token","expiresIn":3600}`)
		}
		reads.Add(1)
		return secretResponse(200, `{"secrets":[{"secretKey":"TOKEN","secretValue":"value"}]}`)
	})
	resolve := func(wantLogins, wantReads int32) {
		t.Helper()
		if _, err := s.resolve(context.Background(), secretConfig(), client); err != nil || logins.Load() != wantLogins || reads.Load() != wantReads {
			t.Fatalf("logins %d reads %d, want %d %d: %v", logins.Load(), reads.Load(), wantLogins, wantReads, err)
		}
	}
	resolve(1, 1)
	resolve(1, 1) // cached
	at.Add(2 * 60)
	resolve(1, 2) // the cache is two minutes old; the token is still good
	at.Add(2 * 3600)
	resolve(2, 3) // the token outlived its hour
}
