package digitalocean

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/flokkit/apptown/internal/deploy"

	"github.com/flokkit/apptown/internal/trust"
)

// Config is the provider's binding config: references and settings,
// never the token itself.
type Config struct {
	TokenFrom    string // the secret name holding the API token
	Region       string // App Platform region slug: syd, nyc, ...
	Registry     string // DOCR registry name
	InstanceSize string // App Platform instance size slug
	DBSize       string // managed database size slug
}

// Client talks to the API with the provider's token, resolved at use
// time (§2.1): the token lives in the secrets store, never in a row.
type Client struct {
	Token string
	Base  string
	HTTP  *http.Client
}

// apiTransport is the API client's pool of connections, one nothing else in
// the process closes: http.DefaultTransport's are closed by anything that
// calls CloseIdleConnections on it, every httptest.Server.Close among them.
// It trusts the AppTown use (internal/trust).
var apiTransport = trust.Lazy(trust.AppTown)

// NewClient talks to the API with a token resolved at use time: the token
// lives in the secrets store, never in a row. An empty base selects the public API.
func NewClient(token, base string) *Client {
	if base == "" {
		base = "https://api.digitalocean.com"
	}
	return &Client{Token: token, Base: base, HTTP: &http.Client{Timeout: 60 * time.Second, Transport: apiTransport}}
}

// RegistryName is the account's container registry, "" when it has none.
func (c *Client) RegistryName(ctx context.Context) (string, error) {
	var got struct {
		Registry struct {
			Name string `json:"name"`
		} `json:"registry"`
	}
	if err := c.Call(ctx, "GET", "/v2/registry", nil, &got); err != nil {
		if isStatus(err, 404) {
			return "", nil
		}
		return "", err
	}
	return got.Registry.Name, nil
}

type apiError struct {
	Status int
	ID     string
	Msg    string
}

// Refused says DigitalOcean answered that it would not do what was asked
// (a 4xx): the effect did not happen. A 5xx says nothing about that.
func (e *apiError) Refused() bool { return e.Status >= 400 && e.Status < 500 }

func (e *apiError) Error() string {
	return fmt.Sprintf("DigitalOcean answered %d (%s): %s", e.Status, e.ID, e.Msg)
}

// call does one JSON request; a non-2xx is a APIError with the API's words.
func (c *Client) Call(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		return deploy.ConnectionError(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 300 {
		var e struct {
			ID      string `json:"id"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		return &apiError{Status: res.StatusCode, ID: e.ID, Msg: e.Message}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// listAll reads every page of a list endpoint. DigitalOcean pages lists and
// names the next page in links.pages.next; a single call sees only the first
// page, so an account with more apps or databases than one page would look
// smaller than it is. Only the path and query of the next link are followed,
// so a page can never send the token to another host. maxListPages bounds a
// provider that keeps answering with a next link.
func listAll[T any](ctx context.Context, c *Client, path, key string) ([]T, error) {
	var all []T
	for page := 0; path != ""; page++ {
		if page == maxListPages {
			return nil, fmt.Errorf("listing %s: more than %d pages", key, maxListPages)
		}
		var raw map[string]json.RawMessage
		if err := c.Call(ctx, "GET", path, nil, &raw); err != nil {
			return nil, err
		}
		if items, ok := raw[key]; ok {
			var got []T
			if err := json.Unmarshal(items, &got); err != nil {
				return nil, fmt.Errorf("listing %s: %w", key, err)
			}
			all = append(all, got...)
		}
		var links struct {
			Pages struct {
				Next string `json:"next"`
			} `json:"pages"`
		}
		path = ""
		if l, ok := raw["links"]; ok && json.Unmarshal(l, &links) == nil && links.Pages.Next != "" {
			next, err := url.Parse(links.Pages.Next)
			if err != nil {
				return nil, fmt.Errorf("listing %s: bad next page link: %w", key, err)
			}
			path = next.RequestURI()
		}
	}
	return all, nil
}

const maxListPages = 100

func isStatus(err error, code int) bool {
	var e *apiError
	return errors.As(err, &e) && e.Status == code
}
