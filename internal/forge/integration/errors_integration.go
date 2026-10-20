package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/strs"
)

// Errors capability: Sentry-compatible error
// reporting via GlitchTip, platform pattern — the instance binds an admin
// token; AppTown provisions a provider-side project per ATTACHED AppTown project
// and stores the DSN as the attachment's resource. Contributions: DSN env
// into attached projects' app containers, a generated steering line, the
// observability/error-reporting guidance block, docs, receipts.

const CapErrors = "errors"

type errorsBinding struct {
	Name      string   `json:"-"`
	URL       string   // e.g. https://errors.example.com
	Org       string   // provider-side organization slug
	Team      string   // optional team slug; empty = discover (create "apptown" if none)
	TokenFrom []string // secret ref holding the admin API token
}

func errorsBindingConfig(b Binding) *errorsBinding {
	var cfg errorsBinding
	if json.Unmarshal([]byte(b.ConfigJSON), &cfg) != nil || cfg.URL == "" {
		return nil
	}
	cfg.Name, cfg.URL = b.Name, strings.TrimSuffix(cfg.URL, "/")
	return &cfg
}

// An empty name selects the first configured errors connector in binding order.
func (s *Service) errorsBindingNamed(ctx context.Context, name string) *errorsBinding {
	if name != "" {
		b, err := Selected(ctx, s.Bindings.db, CapErrors, name)
		if err != nil {
			return nil
		}
		return errorsBindingConfig(b)
	}
	bindings, err := ForCapability(ctx, s.Bindings.db, CapErrors)
	if err != nil {
		return nil
	}
	for _, b := range bindings {
		if cfg := errorsBindingConfig(b); cfg != nil {
			return cfg
		}
	}
	return nil
}

func errorsToken(ctx context.Context, secrets *secret.Service, b *errorsBinding) string {
	all := secrets.All(ctx)
	for _, k := range b.TokenFrom {
		if v := all[k]; v != "" {
			return v
		}
	}
	return ""
}

func glitchtipReq(method, url, token string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw, nil
}

// TestErrorsBinding: list orgs (auth check) and verify the configured org.
func (s *Service) TestErrorsBinding(ctx context.Context, config map[string]any) Health {
	url, _ := config["url"].(string)
	org, _ := config["org"].(string)
	if url == "" || org == "" {
		return Health{OK: false, Detail: "url and org are required"}
	}
	tmp := &errorsBinding{URL: strings.TrimSuffix(url, "/"), Org: org, TokenFrom: ConfigStrings(config["tokenFrom"])}
	token := errorsToken(ctx, s.secrets, tmp)
	if token == "" {
		return Health{OK: false, Detail: "token secret not resolved (" + strings.Join(tmp.TokenFrom, ", ") + ")"}
	}
	code, _, err := glitchtipReq("GET", tmp.URL+"/api/0/organizations/"+org+"/", token, nil)
	if err != nil {
		return Health{OK: false, Detail: "unreachable: " + err.Error()}
	}
	if code != 200 {
		return Health{OK: false, Detail: fmt.Sprintf("GET org %q → %d", org, code)}
	}
	return Health{OK: true, Detail: "org " + org + " reachable, token accepted"}
}

// --- attachments ---

// ProvisionErrors: the linked construction path for the errors capability —
// creates (or adopts) a provider-side project named after AppTown project
// and stores its DSN as the integration's resource, provenance
// 'provisioned'. Idempotent. Returns an HTTP-ish status for the caller.
func (s *Service) ProvisionErrors(ctx context.Context, p *project.Project, connector, who string) (map[string]any, int, error) {
	if err := refusesIntegrations(ctx, s.Bindings.db, p.ID); err != nil {
		return nil, 422, err
	}
	b := s.errorsBindingNamed(ctx, connector)
	if b == nil {
		return nil, 422, fmt.Errorf("no errors connector%s — add the GlitchTip integration first", nameSuffix(connector))
	}

	token := errorsToken(ctx, s.secrets, b)
	if token == "" {
		return nil, 422, fmt.Errorf("errors connector token secret does not resolve")
	}
	team, err := s.errorsTeam(b, token)
	if err != nil {
		return nil, 502, fmt.Errorf("resolving team: %w", err)
	}
	// Provision: create the provider project (409/400 name-taken → adopt).
	code, raw, err := glitchtipReq("POST",
		fmt.Sprintf("%s/api/0/teams/%s/%s/projects/", b.URL, b.Org, team),
		token, map[string]any{"name": p.Slug, "platform": "other"})
	if err != nil {
		return nil, 502, fmt.Errorf("provision: %w", err)
	}
	if code != 201 && code != 200 && code != 409 && code != 400 {
		return nil, 502, fmt.Errorf("provision: HTTP %d: %s", code, strs.Truncate(string(raw), 200))
	}
	// DSN: first client key of the provider project.
	code, raw, err = glitchtipReq("GET",
		fmt.Sprintf("%s/api/0/projects/%s/%s/keys/", b.URL, b.Org, p.Slug), token, nil)
	if err != nil || code != 200 {
		return nil, 502, fmt.Errorf("fetching DSN: HTTP %d %v: %s", code, err, strs.Truncate(string(raw), 200))
	}
	var keys []struct {
		DSN struct {
			Public string `json:"public"`
		} `json:"dsn"`
	}
	if json.Unmarshal(raw, &keys) != nil || len(keys) == 0 || keys[0].DSN.Public == "" {
		return nil, 502, fmt.Errorf("no client key on provider project %s", p.Slug)
	}
	dsn := keys[0].DSN.Public
	// The provider made it: its attachment is recorded though ctx has ended.
	if err := s.Attachments.Attach(context.WithoutCancel(ctx), p.ID, CapErrors, b.Name, encodeResource(map[string]string{"dsn": dsn, "providerProject": p.Slug}), Provisioned, who, map[string]any{"providerProject": p.Slug}); err != nil {
		return nil, 500, err
	}
	return map[string]any{"ok": true, "capability": CapErrors, "connector": b.Name, "provenance": "provisioned",
		"providerProject": p.Slug, "issues": b.URL + "/" + b.Org + "/" + p.Slug}, 200, nil
}

// ErrorsIssueSummary enriches an errors integration's resource with a live
// unresolved-issues summary via the connector token (engine-side only).
func (s *Service) ErrorsIssueSummary(ctx context.Context, p *project.Project, resource map[string]any) {
	b := s.errorsBindingNamed(ctx, "")
	if b == nil {
		return
	}
	token := errorsToken(ctx, s.secrets, b)
	if token == "" {
		return
	}
	code, raw, err := glitchtipReq("GET",
		fmt.Sprintf("%s/api/0/projects/%s/%s/issues/?query=is:unresolved&limit=5", b.URL, b.Org, p.Slug), token, nil)
	if err != nil {
		resource["issuesError"] = err.Error()
		return
	}
	if code != 200 {
		resource["issuesError"] = fmt.Sprintf("HTTP %d: %s", code, strs.Truncate(string(raw), 160))
		return
	}
	var issues []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Culprit   string `json:"culprit"`
		Permalink string `json:"permalink"`
		Count     string `json:"count"`
		Last      string `json:"lastSeen"`
	}
	if json.Unmarshal(raw, &issues) == nil {
		sum := make([]map[string]string, 0, len(issues))
		for _, i := range issues {
			row := map[string]string{"title": i.Title, "count": i.Count, "lastSeen": i.Last}
			if i.Culprit != "" {
				row["culprit"] = i.Culprit
			}
			if i.Permalink != "" {
				row["link"] = i.Permalink
			}
			// Where it broke, from the latest event's innermost frame, so a
			// person can go from AppTown's card to the line without
			// leaving (AppTown's own UI faults arrive here too).
			if i.ID != "" {
				if where := s.errorsIssueWhere(b, token, i.ID); where != "" {
					row["where"] = where
				}
			}
			sum = append(sum, row)
		}
		resource["unresolved"] = sum
	}
}

// errorsTeam resolves the team projects are provisioned under: explicit
// binding config, else the org's first team, else a created "apptown" team.
// Projects live under TEAMS in GlitchTip — a same-named team is not
// guaranteed to exist (needs team:read, and team:write for the create path).
func (s *Service) errorsTeam(b *errorsBinding, token string) (string, error) {
	if b.Team != "" {
		return b.Team, nil
	}
	code, raw, err := glitchtipReq("GET", b.URL+"/api/0/organizations/"+b.Org+"/teams/", token, nil)
	if err != nil {
		return "", err
	}
	if code == 200 {
		var teams []struct {
			Slug string `json:"slug"`
		}
		if json.Unmarshal(raw, &teams) == nil && len(teams) > 0 {
			return teams[0].Slug, nil
		}
	}
	code, raw, err = glitchtipReq("POST", b.URL+"/api/0/organizations/"+b.Org+"/teams/",
		token, map[string]any{"slug": "apptown"})
	if err != nil {
		return "", err
	}
	if code != 201 && code != 200 && code != 409 {
		return "", fmt.Errorf("creating team: HTTP %d: %s", code, strs.Truncate(string(raw), 160))
	}
	return "apptown", nil
}

// errorsIssueWhere reads an issue's latest event and names its innermost
// exception frame: file:line:col in function. Empty when the provider has
// no frames (a message without a stack) or the call fails.
func (s *Service) errorsIssueWhere(b *errorsBinding, token, issueID string) string {
	code, raw, err := glitchtipReq("GET", fmt.Sprintf("%s/api/0/issues/%s/events/latest/", b.URL, issueID), token, nil)
	if err != nil || code != 200 {
		return ""
	}
	var ev struct {
		Entries []struct {
			Type string `json:"type"`
			Data struct {
				Values []struct {
					Stacktrace struct {
						Frames []struct {
							Filename string `json:"filename"`
							AbsPath  string `json:"absPath"`
							Function string `json:"function"`
							LineNo   int    `json:"lineNo"`
							ColNo    int    `json:"colNo"`
						} `json:"frames"`
					} `json:"stacktrace"`
				} `json:"values"`
			} `json:"data"`
		} `json:"entries"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return ""
	}
	for _, e := range ev.Entries {
		if e.Type != "exception" {
			continue
		}
		for _, v := range e.Data.Values {
			if n := len(v.Stacktrace.Frames); n > 0 {
				f := v.Stacktrace.Frames[n-1] // Sentry orders frames outermost first
				file := f.Filename
				if file == "" {
					file = f.AbsPath
				}
				where := fmt.Sprintf("%s:%d:%d", file, f.LineNo, f.ColNo)
				if f.Function != "" {
					where += " in " + f.Function
				}
				return where
			}
		}
	}
	return ""
}
