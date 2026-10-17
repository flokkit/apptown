package integration

import (
	"context"
	"encoding/json"
	"errors"
	"flokkit.io/forge/internal/database"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/gitlab"
)

// Scoped SCM access for agent steps (design/subsystems/composition.md, multi-repo
// position): a PROJECT-scoped GitLab access token, minted with the
// forge-bot's owner rights, stored as an attachment resource, and injected
// ONLY into steps that declare `with: {scm: true}`. The instance token never
// enters a container; the scoped token cannot reach other repos — the
// credential boundary is the multi-repo policy.

// scmProfiles: least privilege by default. `scm: true` in a workflow means
// READ — a step that only monitors CI must not hold a token that can push.
var scmProfiles = map[string]struct {
	Scopes      []string
	AccessLevel int
	Label       string
}{
	"read":  {Scopes: []string{"read_api"}, AccessLevel: 20, Label: "read (reporter): CI status, MRs, issues — no writes"},
	"write": {Scopes: []string{"api", "write_repository"}, AccessLevel: 30, Label: "write (developer): comments, MR operations, push"},
}

const scmTokenTTL = 3 * 24 * time.Hour // short-lived; refreshed lazily at grant

type scmToken struct {
	Token     string `json:"token"`
	TokenID   int64  `json:"tokenId"`
	ExpiresAt string `json:"expiresAt"`
}

// mintSCMToken returns a live token for (project, profile), minting or
// rotating as needed. Refresh is LAZY: every grant passes through here, so a
// token near expiry (<24h) is replaced on use and an unused token simply
// dies — no scheduler, minimal live-credential surface.
func (s *Service) mintSCMToken(ctx context.Context, projectID ids.ID, remote, profile string) (*scmToken, string, error) {
	if err := refusesIntegrations(ctx, s.Bindings.db, projectID); err != nil {
		return nil, "", err
	}
	prof, ok := scmProfiles[profile]
	if !ok {
		return nil, "", fmt.Errorf("unknown scm profile %q (read|write)", profile)
	}
	// The recorded tokens are what a rotation revokes: only a project with
	// no record mints its first; a record that cannot be read mints nothing,
	// or the token it holds would be overwritten while still live.
	tokens := map[string]*scmToken{}
	res, err := Resource(ctx, s.Bindings.db, projectID, CapSCM)
	if found, err := database.Lookup(err); err != nil {
		return nil, "", fmt.Errorf("reading the recorded tokens: %w; nothing new was minted", err)
	} else if found {
		var r struct {
			Profiles map[string]*scmToken `json:"profiles"`
			Path     string               `json:"gitlabProject"`
		}
		if err := json.Unmarshal([]byte(res), &r); err != nil {
			return nil, "", fmt.Errorf("the recorded tokens could not be read: %w; nothing new was minted", err)
		}
		if r.Profiles != nil {
			tokens = r.Profiles
		}
	}
	if t := tokens[profile]; t != nil && t.Token != "" {
		if exp, err := time.Parse(time.RFC3339, t.ExpiresAt); err == nil && exp.Sub(s.now()) > 24*time.Hour {
			return t, gitlabPathFromRemote(remote), nil
		}
	}

	gl, err := gitlab.New(s.secrets.Value(ctx, "GITLAB_URL"), s.secrets.Value(ctx, "GITLAB_TOKEN"))
	if err != nil {
		return nil, "", err
	}
	path := gitlabPathFromRemote(remote)
	if path == "" {
		return nil, "", fmt.Errorf("project has no GitLab remote")
	}
	var proj struct {
		ID            int64  `json:"id"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := gl.Do(ctx, "GET", "/projects/"+url.PathEscape(path), nil, &proj); err != nil {
		return nil, "", err
	}
	if profile == "write" {
		if err := pushProtected(ctx, gl, proj.ID, proj.DefaultBranch); err != nil {
			return nil, "", err
		}
	}
	// Revoke the predecessor before minting its replacement. The record
	// holds one token per profile, so minting over a token GitLab still
	// honours would leave a live credential the forge no longer knows:
	// rotation stops unless the old one is revoked or already gone.
	if old := tokens[profile]; old != nil && old.TokenID != 0 {
		var gone *gitlab.HTTPError
		if err := gl.Do(ctx, "DELETE", fmt.Sprintf("/projects/%d/access_tokens/%d", proj.ID, old.TokenID), nil, nil); err != nil && !(errors.As(err, &gone) && gone.Status == http.StatusNotFound) {
			return nil, "", fmt.Errorf("revoking the %s token it replaces: %w; nothing new was minted", profile, err)
		}
	}
	expires := s.now().Add(scmTokenTTL)
	var tok struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	// Once asked, the mint is answered though ctx has ended: GitLab makes the
	// token either way, and the forge must hear which.
	if err := gl.Do(context.WithoutCancel(ctx), "POST", fmt.Sprintf("/projects/%d/access_tokens", proj.ID), map[string]any{
		"name":         "forge-agent-" + profile,
		"scopes":       prof.Scopes,
		"access_level": prof.AccessLevel,
		"expires_at":   expires.Format(time.DateOnly),
	}, &tok); err != nil {
		return nil, "", err
	}
	nt := &scmToken{Token: tok.Token, TokenID: tok.ID, ExpiresAt: expires.Format(time.RFC3339)}
	tokens[profile] = nt
	resource, err := json.Marshal(map[string]any{"profiles": tokens, "gitlabProject": path})
	if err != nil {
		return nil, "", err
	}
	// GitLab has made the token: it is recorded though the caller stops, or
	// a live token would exist that the forge cannot rotate or revoke. One
	// that cannot be recorded is revoked at once.
	if err := s.Attachments.Remember(context.WithoutCancel(ctx), projectID, CapSCM, "gitlab", string(resource)); err != nil {
		log.Printf("remembering the SCM access token: %v", err)
		if rerr := gl.Do(ctx, "DELETE", fmt.Sprintf("/projects/%d/access_tokens/%d", proj.ID, tok.ID), nil, nil); rerr != nil {
			log.Printf("revoking the unrecorded SCM access token %d: %v", tok.ID, rerr)
		}
		return nil, "", err
	}
	audit.At(s.now()).BestEffort(ctx, s.Bindings.db, "forge-system", projectID, "scm.token-rotated", profile,
		map[string]any{"project": path, "scopes": prof.Scopes, "expires": nt.ExpiresAt, "ttl": "72h"})
	return nt, path, nil
}

// SCMEnvironment: the runtime contribution — glab/git env for a step that opted
// in, at the requested profile. Empty (with a reason) when unavailable.
func (s *Service) SCMEnvironment(ctx context.Context, projectID ids.ID, remote, profile string) ([]string, string) {
	t, path, err := s.mintSCMToken(ctx, projectID, remote, profile)
	if err != nil {
		return nil, err.Error()
	}
	gl, err := gitlab.New(s.secrets.Value(ctx, "GITLAB_URL"), s.secrets.Value(ctx, "GITLAB_TOKEN"))
	if err != nil {
		return nil, err.Error()
	}
	return []string{
		// Full URL with scheme — glab treats a bare hostname as gitlab.com.
		"GITLAB_HOST=" + gl.BaseURL(),
		"GITLAB_TOKEN=" + t.Token,
		"GLAB_REPO=" + path,
	}, ""
}

// SCMGrant accepts `scm: true` (= read, least privilege), `scm: read`,
// `scm: write`, or absent/false.
type SCMGrant string

func (g *SCMGrant) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case bool:
		if t {
			*g = "read"
		}
	case string:
		if t != "" && t != "read" && t != "write" {
			return fmt.Errorf("scm must be true, read or write (got %q)", t)
		}
		*g = SCMGrant(t)
	}
	return nil
}

func gitlabPathFromRemote(remote string) string {
	u, err := url.Parse(remote)
	if err != nil || u.Path == "" {
		return ""
	}
	p := u.Path
	if len(p) > 0 && p[0] == '/' {
		p = p[1:]
	}
	if len(p) > 4 && p[len(p)-4:] == ".git" {
		p = p[:len(p)-4]
	}
	return p
}

// Label describes the permissions promised by this declared grant.
func (g SCMGrant) Label() string { return scmProfiles[string(g)].Label }

// pushProtected: a write token is Developer level. On GitLab a Developer may
// push to a branch no rule protects, and every protection rule matching a
// branch applies, the most permissive winning; merging a merge request is a
// separate permission. A step's token must be able to neither push to the
// default branch nor merge into it, around landing. So at least one rule
// must match the default branch (by name, or a wildcard), and in every
// matching rule each push and merge entry must be a role of Maintainer or
// higher, or No one. An entry naming a user, group or deploy key is refused
// rather than judged. Checked when a write token is minted; one minted under
// protection lives at most scmTokenTTL if the rules change afterwards
// (accepted, design/subsystems/integration-runtime.md).
func pushProtected(ctx context.Context, gl *gitlab.Client, projectID int64, branch string) error {
	refuse := func(why string) error {
		return fmt.Errorf("%s, so a step's write token could change it around landing; allow only Maintainers to push and merge, then try again; no write token was minted", why)
	}
	if branch == "" {
		return errors.New("the GitLab project has no default branch to protect; no write token was minted")
	}
	type entry struct {
		Level   *int   `json:"access_level"`
		User    *int64 `json:"user_id"`
		Group   *int64 `json:"group_id"`
		DeployK *int64 `json:"deploy_key_id"`
	}
	var rules []struct {
		Name  string  `json:"name"`
		Push  []entry `json:"push_access_levels"`
		Merge []entry `json:"merge_access_levels"`
	}
	if err := gl.Do(ctx, "GET", fmt.Sprintf("/projects/%d/protected_branches?per_page=%d", projectID, protectionRulesRead), nil, &rules); err != nil {
		return fmt.Errorf("reading the protection of %s on GitLab: %w; no write token was minted", branch, err)
	}
	// Every matching rule applies, so a rule not read could be the
	// permissive one: a full page may not be the whole list, and is refused.
	if len(rules) >= protectionRulesRead {
		return fmt.Errorf("the project has %d or more branch protection rules, more than the forge reads in one page, so it cannot tell whether a step's write token could change %s around landing; no write token was minted", protectionRulesRead, branch)
	}
	matched := false
	for _, rule := range rules {
		if !branchRuleMatches(rule.Name, branch) {
			continue
		}
		matched = true
		for kind, entries := range map[string][]entry{"push": rule.Push, "merge": rule.Merge} {
			for _, e := range entries {
				switch {
				case e.User != nil || e.Group != nil || e.DeployK != nil:
					return refuse(fmt.Sprintf("the protection rule %q lets a named user, group or deploy key %s to %s, which the forge does not judge", rule.Name, kind, branch))
				case e.Level == nil || (*e.Level != 0 && *e.Level < 40):
					return refuse(fmt.Sprintf("the protection rule %q lets Developers %s to %s", rule.Name, kind, branch))
				}
			}
		}
	}
	if !matched {
		return refuse(fmt.Sprintf("%s is not protected on GitLab", branch))
	}
	return nil
}

// protectionRulesRead is the most protection rules one read returns; a
// project with that many is refused, not judged from part of its list.
const protectionRulesRead = 100

// branchRuleMatches: a GitLab protection rule's name matches a branch
// exactly, or as a wildcard where * stands for any run of characters.
func branchRuleMatches(rule, branch string) bool {
	if !strings.Contains(rule, "*") {
		return rule == branch
	}
	parts := strings.Split(rule, "*")
	if !strings.HasPrefix(branch, parts[0]) {
		return false
	}
	rest := branch[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(rest, part)
		if i < 0 {
			return false
		}
		rest = rest[i+len(part):]
	}
	return strings.HasSuffix(rest, parts[len(parts)-1])
}
