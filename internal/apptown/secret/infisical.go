package secret

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/clock"
)

// infisicalSettings identifies both the credential and the namespace it may read.
// It must never be logged: ClientSecret is a bootstrap credential.
type infisicalSettings struct{ URL, ClientID, ClientSecret, ProjectID, Environment, Path string }

func (c infisicalSettings) configured() bool {
	return c.URL != "" && c.ClientID != "" && c.ClientSecret != "" && c.ProjectID != ""
}
func (c infisicalSettings) normalized() infisicalSettings {
	c.URL = strings.TrimRight(c.URL, "/")
	if c.Environment == "" {
		c.Environment = "prod"
	}
	if c.Path == "" {
		c.Path = "/"
	}
	return c
}

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}
type refreshTransition uint8

const (
	refreshUnchanged refreshTransition = iota
	refreshFailed
	refreshRecovered
)

// secretResolution is a successful read's values, or none with the
// provider's error: a failed refresh never answers the last values it read.
// Only the refreshing caller receives a health transition, for one receipt.
type secretResolution struct {
	Values     map[string]string
	Transition refreshTransition
}

type secretFlight struct {
	done      chan struct{}
	config    infisicalSettings
	values    map[string]string
	err       error
	cancelled bool
}

// infisical owns one cache and one synchronous refresh. Its zero value is ready
// to use. No network or receipt I/O holds mu, and it starts no goroutines.
type infisical struct {
	mu          sync.Mutex
	config      infisicalSettings
	token       string
	tokenExpiry time.Time
	values      map[string]string
	fetched     time.Time
	failed      bool
	flight      *secretFlight
	// clock ages the cache and the login token (the real clock when nil).
	clock func() time.Time
}

func (s *infisical) now() time.Time { return clock.Or(s.clock)() }

// resolve bounds the entire call, including shared-refresh waits. Values
// read under a minute ago are answered as current; after that a refresh
// decides, and a failed one answers its error and no values. Caller
// cancellation returns no values.
// A cancelled refresh does not poison the cache or another caller's operation.
func (s *infisical) resolve(ctx context.Context, config infisicalSettings, client HTTPClient) (secretResolution, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return secretResolution{}, err
	}
	if client == nil {
		return secretResolution{}, errors.New("Infisical HTTP client is required")
	}
	config = config.normalized()
	if !config.configured() {
		return secretResolution{}, errors.New("Infisical is not configured")
	}
	for {
		if err := ctx.Err(); err != nil {
			return secretResolution{}, err
		}
		s.mu.Lock()
		if flight := s.flight; flight != nil {
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return secretResolution{}, ctx.Err()
			case <-flight.done:
			}
			if err := ctx.Err(); err != nil {
				return secretResolution{}, err
			}
			if flight.config != config || flight.cancelled {
				continue
			}
			return secretResolution{Values: maps.Clone(flight.values)}, flight.err
		}
		if s.config != config {
			s.config = config
			s.token = ""
			s.tokenExpiry = time.Time{}
			s.values = nil
			s.fetched = time.Time{}
			s.failed = false
		}
		if s.values != nil && s.now().Sub(s.fetched) < time.Minute {
			result := secretResolution{Values: maps.Clone(s.values)}
			s.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return secretResolution{}, err
			}
			return result, nil
		}
		flight := &secretFlight{done: make(chan struct{}), config: config}
		s.flight = flight
		token, expiry := s.token, s.tokenExpiry
		s.mu.Unlock()

		values, token, expiry, err := fetchInfisical(ctx, client, config, token, expiry, s.now)
		s.mu.Lock()
		result := secretResolution{}
		if ctx.Err() != nil {
			err = ctx.Err()
			flight.cancelled = true
		} else {
			s.token, s.tokenExpiry = token, expiry
			if err != nil {
				if !s.failed {
					result.Transition = refreshFailed
				}
				s.failed = true
			} else {
				if s.failed {
					result.Transition = refreshRecovered
				}
				s.failed = false
				s.values = values
				s.fetched = s.now()
				flight.values = s.values
				result.Values = maps.Clone(s.values)
			}
		}
		flight.err = err
		s.flight = nil
		close(flight.done)
		s.mu.Unlock()
		return result, err
	}
}

// lastKnown is the values the last successful read of config answered,
// however old: masking hides them in text, and nothing else reads them.
func (s *infisical) lastKnown(config infisicalSettings) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.config != config.normalized() {
		return nil
	}
	return maps.Clone(s.values)
}

func fetchInfisical(ctx context.Context, client HTTPClient, c infisicalSettings, token string, expiry time.Time, now func() time.Time) (map[string]string, string, time.Time, error) {
	if token == "" || now().After(expiry) {
		body, _ := json.Marshal(struct {
			ClientID     string `json:"clientId"`
			ClientSecret string `json:"clientSecret"`
		}{c.ClientID, c.ClientSecret})
		var login struct {
			AccessToken string `json:"accessToken"`
			ExpiresIn   int    `json:"expiresIn"`
		}
		_, err := infisicalRequest(ctx, client, "POST", c.URL+"/api/v1/auth/universal-auth/login", "", body, 1<<20, &login)
		if err != nil {
			return nil, token, expiry, err
		}
		if login.AccessToken == "" || login.ExpiresIn <= 0 {
			return nil, token, expiry, errors.New("Infisical login returned invalid token metadata")
		}
		token = login.AccessToken
		expiry = now().Add(time.Duration(login.ExpiresIn)*time.Second - 30*time.Second)
	}
	query := url.Values{"workspaceId": {c.ProjectID}, "environment": {c.Environment}, "secretPath": {c.Path}}
	var out struct {
		Secrets []struct {
			Key   string `json:"secretKey"`
			Value string `json:"secretValue"`
		} `json:"secrets"`
	}
	status, err := infisicalRequest(ctx, client, "GET", c.URL+"/api/v3/secrets/raw?"+query.Encode(), token, nil, 4<<20, &out)
	if status == http.StatusUnauthorized {
		token = ""
	}
	if err != nil {
		return nil, token, expiry, err
	}
	if out.Secrets == nil {
		return nil, token, expiry, errors.New("Infisical returned no secrets collection")
	}
	values := make(map[string]string, len(out.Secrets))
	for _, v := range out.Secrets {
		values[v.Key] = v.Value
	}
	return values, token, expiry, nil
}

// Bounded, checked reads. Response bodies can contain secrets and are never
// copied into errors or health receipts.
func infisicalRequest(ctx context.Context, client HTTPClient, method, address, token string, body []byte, limit int64, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if method == "POST" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return res.StatusCode, fmt.Errorf("Infisical %s: HTTP %d", method, res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return res.StatusCode, err
	}
	if int64(len(raw)) > limit {
		return res.StatusCode, errors.New("Infisical response exceeds size limit")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return res.StatusCode, errors.New("Infisical returned invalid JSON")
	}
	return res.StatusCode, nil
}
