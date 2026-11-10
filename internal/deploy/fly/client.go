// Package fly is the Fly.io Machines target: AppTown places one Machine per
// part of an app in a Fly app of the deployment's own, from the release's
// images copied into Fly's registry, and reads Fly's answers about them.
//
// The driver talks to the Machines API (https://api.machines.dev) with an
// org token resolved at use time from AppTown's secrets; the token is never
// stored, logged or put in a receipt. It is safe in an org shared with other
// installations and with apps people made: every Machine it makes carries
// this installation's id in its metadata, names carry an installation part,
// and it deletes only what its records name, after checking the live
// resource still is what was recorded (design/subsystems/deploy-targets.md).
package fly

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/trust"
)

const (
	// Platform is the one platform Fly Machines run, in OCI form.
	Platform = "linux/amd64"
	// apiBase is the public Machines API.
	apiBase = "https://api.machines.dev"
	// registryHost is Fly's registry: a Machine pulls only from here or
	// from a public registry, so release images are copied into it.
	registryHost = "registry.fly.io"
	// appDomain is the domain Fly issues app addresses under.
	appDomain = "fly.dev"
)

// Config is a Fly target's connector configuration. The token is named, not
// held: TokenFrom is the AppTown secret that holds it.
type Config struct {
	TokenFrom string // the secret holding an org token; default FLY_API_TOKEN
	Org       string // the Fly org slug the token reaches
	Region    string // where Machines run
}

// Client talks to the Machines API with a token resolved at use time.
type Client struct {
	Token string
	Base  string
	HTTP  *http.Client
}

var apiTransport = trust.Lazy(trust.AppTown)

// NewClient talks to the API with token; an empty base is the public API.
func NewClient(token, base string) *Client {
	if base == "" {
		base = apiBase
	}
	return &Client{Token: token, Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 60 * time.Second, Transport: apiTransport}}
}

// apiError is a non-2xx answer, in Fly's words.
type apiError struct {
	Status int
	Code   string // Fly's machine-readable status, such as name_taken
	Msg    string
}

func (e *apiError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("Fly answered %d (%s): %s", e.Status, e.Code, e.Msg)
	}
	return fmt.Sprintf("Fly answered %d: %s", e.Status, e.Msg)
}

func isStatus(err error, code int) bool {
	var e *apiError
	return errors.As(err, &e) && e.Status == code
}

// do does one JSON request. A non-2xx is an apiError carrying Fly's words;
// a request that got no answer is a connection error, never a refusal. The
// token never enters an error.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	_, err := c.call(ctx, method, path, body, out)
	return err
}

func (c *Client) call(ctx context.Context, method, path string, body, out any) (http.Header, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, deploy.ConnectionError(errors.New(redact(err.Error(), c.Token)))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 300 {
		var e struct {
			Error  string `json:"error"`
			Status string `json:"status"`
		}
		if json.Unmarshal(raw, &e) != nil || e.Error == "" {
			e.Error = strings.TrimSpace(string(raw))
		}
		return res.Header, &apiError{Status: res.StatusCode, Code: e.Status, Msg: redact(e.Error, c.Token)}
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return res.Header, fmt.Errorf("reading Fly's answer to %s %s: %w", method, path, err)
		}
	}
	return res.Header, nil
}

// redact keeps a token out of any words AppTown records.
func redact(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "<token>")
}

// maxPages bounds a list the provider keeps paging.
const maxPages = 100

// appsIn lists every app in org, following Fly's cursor.
func (c *Client) appsIn(ctx context.Context, org string) ([]flyApp, error) {
	var all []flyApp
	cursor := ""
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("listing apps: more than %d pages", maxPages)
		}
		q := url.Values{"org_slug": {org}, "limit": {"500"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var got struct {
			Apps       []flyApp `json:"apps"`
			NextCursor string   `json:"next_cursor"`
		}
		if err := c.do(ctx, "GET", "/v1/apps?"+q.Encode(), nil, &got); err != nil {
			return nil, err
		}
		all = append(all, got.Apps...)
		if got.NextCursor == "" {
			return all, nil
		}
		cursor = got.NextCursor
	}
}

// orgMachines lists every Machine in org with its app and metadata.
func (c *Client) orgMachines(ctx context.Context, org string) ([]orgMachine, error) {
	var all []orgMachine
	cursor := ""
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("listing Machines: more than %d pages", maxPages)
		}
		q := url.Values{"limit": {"500"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var got struct {
			Machines   []orgMachine `json:"machines"`
			NextCursor string       `json:"next_cursor"`
		}
		if err := c.do(ctx, "GET", "/v1/orgs/"+url.PathEscape(org)+"/machines?"+q.Encode(), nil, &got); err != nil {
			return nil, err
		}
		all = append(all, got.Machines...)
		if got.NextCursor == "" {
			return all, nil
		}
		cursor = got.NextCursor
	}
}

// machines lists every Machine of one app, following Fly's cursor header.
func (c *Client) machines(ctx context.Context, app string) ([]machine, error) {
	var all []machine
	cursor := ""
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("listing %s's Machines: more than %d pages", app, maxPages)
		}
		q := url.Values{"limit": {"500"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var got []machine
		h, err := c.call(ctx, "GET", "/v1/apps/"+url.PathEscape(app)+"/machines?"+q.Encode(), nil, &got)
		if err != nil {
			return nil, err
		}
		all = append(all, got...)
		cursor = h.Get("fly-next-cursor")
		if cursor == "" {
			return all, nil
		}
	}
}
