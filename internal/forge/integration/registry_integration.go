package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"flokkit.io/forge/internal/database"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/strs"
)

// Container-registry integration: the image is the unit of release. The
// generic service-kind machinery supplies the config surface and steering;
// this adapter supplies the provider behavior the generic layer can't:
//
//   - Provisioning (linked path, GitLab provider): at attach, the engine
//     mints a PER-PROJECT deploy token (read_registry+write_registry —
//     Maintainer suffices) using the same GitLab
//     credential the SCM machinery holds. {repo, username, token} land in
//     resource_json — the errors-DSN precedent: project-scoped, listed
//     redacted, never echoed into steps.
//   - The agent's private docker daemon is logged into the registry BY THE
//     ENGINE (dind.go), so the agent can `docker push` without ever
//     holding the credential in its environment.
//   - Detach stops contributions but never revokes the provider-side
//     token: deprovisioning is a destructive act deserving its own verb.

const CapContainerRegistry = "container-registry"

// ErrNoOfferedRegistry is a project with no container registry for which
// AppTown cannot choose one: no container-registry connector, or
// several. Its message names the choice a person makes.
var ErrNoOfferedRegistry = fault.New(fault.Unprocessable, "no container registry is attached")

// AttachOfferedRegistry attaches the one container-registry connector to a
// project that has no container registry, as a landing that publishes its
// release needs (the rule deployment.LandingTarget follows for Docker
// hosts). Connectors are the instance's, offered to every project. A
// GitLab connector provisions, as an attach by its linked path does; any
// other is attached with the repository <url>/<group>/<slug>, the naming
// provisioning uses, and no credential of its own: the connector's
// (username, tokenFrom) is read when used. With none or several AppTown
// cannot choose, and ErrNoOfferedRegistry names the choice. A project that
// has an attachment keeps it. The attachment and its receipt are recorded
// as any other, by who.
func (s *Service) AttachOfferedRegistry(ctx context.Context, projectID ids.ID, who string, receipt map[string]any) error {
	if attached, err := s.Attachments.IsAttached(ctx, projectID, CapContainerRegistry); err != nil || attached {
		return err
	}
	p, err := s.projects.ByID(ctx, projectID)
	if err != nil {
		return err
	}
	connectors, err := ForCapability(ctx, s.Bindings.db, CapContainerRegistry)
	if err != nil {
		return err
	}
	switch len(connectors) {
	case 0:
		return fmt.Errorf("%w: no container-registry connector is offered; add one under Integrations, or attach a registry to %s, then land again", ErrNoOfferedRegistry, p.Slug)
	case 1:
	default:
		names := make([]string, len(connectors))
		for i, c := range connectors {
			names[i] = c.Name
		}
		return fmt.Errorf("%w: several container-registry connectors are offered (%s); attach the one %s publishes to, then land again", ErrNoOfferedRegistry, strings.Join(names, ", "), p.Slug)
	}
	connector := connectors[0]
	cfg := parseJSONMap(connector.ConfigJSON)
	if provider, _ := cfg["provider"].(string); provider == "gitlab" {
		_, _, err := s.ProvisionRegistry(ctx, &p, connector.Name, who)
		return err
	}
	host, _ := cfg["url"].(string)
	group, _ := cfg["group"].(string)
	if host == "" {
		return fmt.Errorf("%w: the container-registry connector %s names no url; give it one, then land again", ErrNoOfferedRegistry, connector.Name)
	}
	repo := strings.Join(slices.DeleteFunc([]string{strings.TrimSuffix(host, "/"), strings.Trim(group, "/"), p.Slug}, func(s string) bool { return s == "" }), "/")
	payload := map[string]any{"repo": repo, "offered": true}
	for k, v := range receipt {
		payload[k] = v
	}
	return s.Attachments.Attach(ctx, projectID, CapContainerRegistry, connector.Name, encodeResource(map[string]any{"repo": repo}), Provisioned, who, payload)
}

// ProvisionRegistry is the linked-construction path: create a per-project
// deploy token through the connector's provider and record the resource.
func (s *Service) ProvisionRegistry(ctx context.Context, p *project.Project, connector, who string) (map[string]any, int, error) {
	if err := refusesIntegrations(ctx, s.Bindings.db, p.ID); err != nil {
		return nil, 422, err
	}
	if connector == "" {
		name, _, err := s.Bindings.FirstName(ctx, CapContainerRegistry)
		if err != nil {
			return nil, 0, err
		}
		connector = name
	}
	cfg := s.ConnectorConfig(ctx, connector, CapContainerRegistry)
	if cfg == nil {
		return nil, 422, fmt.Errorf("no %s connector%s — add one first", CapContainerRegistry, nameSuffix(connector))
	}
	provider, _ := cfg["provider"].(string)
	host, _ := cfg["url"].(string)
	group, _ := cfg["group"].(string)
	if provider != "gitlab" {
		return nil, 422, fmt.Errorf("connector %q cannot provision (provider %q) — attach with direct resource fields instead", connector, provider)
	}
	if host == "" || group == "" {
		return nil, 422, fmt.Errorf("connector %q needs url and group to provision", connector)
	}
	base, tok := s.secrets.Value(ctx, "GITLAB_URL"), s.secrets.Value(ctx, "GITLAB_TOKEN")
	if base == "" || tok == "" {
		return nil, 422, fmt.Errorf("engine GitLab credentials (GITLAB_URL/GITLAB_TOKEN) do not resolve")
	}

	// Revoke the token this project was provisioned before, or mint
	// nothing: the attachment holds one token, and minting over one GitLab
	// still honours would leave a live registry credential AppTown no
	// longer knows. Only a project with no attachment provisions its first;
	// a token GitLab no longer has (404) is gone already.
	oldRes, err := Resource(ctx, s.Bindings.db, p.ID, CapContainerRegistry)
	if found, err := database.Lookup(err); err != nil {
		return nil, 0, fmt.Errorf("reading the registry attachment: %w; nothing new was minted", err)
	} else if found {
		old := map[string]any{}
		if err := json.Unmarshal([]byte(oldRes), &old); err != nil {
			return nil, 0, fmt.Errorf("the registry attachment could not be read: %w; nothing new was minted", err)
		}
		if old["token_id"] != nil {
			status, body, err := gitlabJSON("DELETE", fmt.Sprintf("%s/api/v4/projects/%s%%2F%s/deploy_tokens/%v",
				strings.TrimSuffix(base, "/"), group, p.Slug, old["token_id"]), tok, nil)
			if err == nil && status >= 400 && status != http.StatusNotFound {
				err = fmt.Errorf("HTTP %d: %s", status, strs.Truncate(strings.TrimSpace(string(body)), 200))
			}
			if err != nil {
				return nil, 502, fmt.Errorf("revoking the deploy token it replaces: %w; nothing new was minted", err)
			}
		}
	}

	mint := func() (int, []byte, error) {
		return gitlabJSON("POST",
			fmt.Sprintf("%s/api/v4/projects/%s%%2F%s/deploy_tokens", strings.TrimSuffix(base, "/"), group, p.Slug),
			tok, map[string]any{
				"name":   "forge-registry-" + p.Slug,
				"scopes": []string{"read_registry", "write_registry"},
			})
	}
	code, raw, err := mint()
	if err != nil {
		return nil, 502, fmt.Errorf("minting deploy token: %w", err)
	}
	if code == 404 {
		// Forge-born project with no GitLab repo yet: the registry hangs off
		// a GitLab project, so provisioning creates one (empty repo is fine —
		// the registry works regardless, and a later SCM attach adopts it).
		gcode, graw, gerr := gitlabJSON("GET",
			fmt.Sprintf("%s/api/v4/groups/%s", strings.TrimSuffix(base, "/"), group), tok, nil)
		if gerr != nil || gcode != 200 {
			return nil, 502, fmt.Errorf("resolving group %q: HTTP %d %v", group, gcode, gerr)
		}
		var g struct {
			ID int64 `json:"id"`
		}
		_ = json.Unmarshal(graw, &g)
		ccode, craw, cerr := gitlabJSON("POST", strings.TrimSuffix(base, "/")+"/api/v4/projects", tok,
			map[string]any{"name": p.Slug, "path": p.Slug, "namespace_id": g.ID, "visibility": "private"})
		if cerr != nil || (ccode != 200 && ccode != 201) {
			return nil, 502, fmt.Errorf("creating GitLab project %s/%s: HTTP %d %v: %s",
				group, p.Slug, ccode, cerr, strs.Truncate(string(craw), 200))
		}
		audit.At(s.now()).BestEffort(ctx, s.Bindings.db, who, p.ID, "registry.project-created", group+"/"+p.Slug, nil)
		code, raw, err = mint()
		if err != nil {
			return nil, 502, fmt.Errorf("minting deploy token: %w", err)
		}
	}
	if code != 200 && code != 201 {
		return nil, 502, fmt.Errorf("minting deploy token: HTTP %d: %s", code, strs.Truncate(string(raw), 200))
	}
	var dt struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Token    string `json:"token"`
	}
	if json.Unmarshal(raw, &dt) != nil || dt.Token == "" {
		return nil, 502, fmt.Errorf("deploy token response unparseable")
	}

	resource := map[string]any{
		"repo":     host + "/" + group + "/" + p.Slug,
		"username": dt.Username,
		"token":    dt.Token,
		"token_id": dt.ID,
	}
	// The provider made it: its attachment is recorded though ctx has ended.
	if err := s.Attachments.Attach(context.WithoutCancel(ctx), p.ID, CapContainerRegistry, connector, encodeResource(resource), Provisioned, who, map[string]any{"repo": resource["repo"]}); err != nil {
		return nil, 500, err
	}
	return map[string]any{"ok": true, "capability": CapContainerRegistry, "connector": connector,
		"provenance": "provisioned", "repo": resource["repo"]}, 200, nil
}

func gitlabJSON(method, url, token string, body map[string]any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw, nil
}
