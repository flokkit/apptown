// Package gitlab implements the provider protocol and Git credential transport.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/strs"
)

// ErrNotConfigured: no GitLab connection is set up on this AppTown. That is
// an ordinary state (an AppTown may keep code only on itself), so callers can
// tell it apart from a GitLab that is set up but failing.
var ErrNotConfigured = errors.New("GitLab not configured")

func New(base, token string) (*Client, error) {
	base = strings.TrimRight(base, "/")
	if base == "" || token == "" {
		return nil, fmt.Errorf("%w: set GITLAB_URL and GITLAB_TOKEN (secrets.env or Infisical)", ErrNotConfigured)
	}
	return &Client{base: base, token: token, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("GitLab API redirect refused: configure the final API URL")
	}}}, nil
}

// Do is DoContext for AppTown's own calls. A write, once sent, is
// answered though ctx has ended: GitLab makes the change either way, and
// AppTown must hear which (a project's id, a token).
// HTTPError is GitLab's answer with a failing status, so a caller can tell
// "not there" (404) from a refusal or an outage.
type HTTPError struct {
	Status int
	Text   string
}

func (e *HTTPError) Error() string { return e.Text }

func (g *Client) Do(ctx context.Context, method, path string, body any, out any) error {
	if method != http.MethodGet {
		ctx = context.WithoutCancel(ctx)
	}
	return g.DoContext(ctx, method, path, body, out)
}

func (g *Client) DoContext(ctx context.Context, method, path string, body any, out any) error {
	status, raw, err := g.Raw(ctx, method, path, body)
	if err != nil {
		return err
	}
	if status >= 400 {
		return &HTTPError{Status: status, Text: fmt.Sprintf("gitlab %s %s: HTTP %d: %s", method, path, status, strs.Truncate(string(raw), 300))}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Raw sends JSON to an API-relative path and returns the complete bounded response.
// It honors ctx without detaching cancellation. Status remains available if reading
// fails; an incomplete response does not establish the outcome of a provider write.
func (g *Client) Raw(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+"/api/v4"+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", g.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := g.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	const limit = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return res.StatusCode, nil, fmt.Errorf("reading GitLab response: %w", err)
	}
	if len(raw) > limit {
		return res.StatusCode, nil, fmt.Errorf("GitLab response exceeds %d bytes", limit)
	}
	return res.StatusCode, raw, nil
}

// CreateProject creates a private project and returns its clone URL and path.
func (g *Client) CreateProject(ctx context.Context, name string, namespaceID int) (cloneURL, pathNS string, err error) {
	p, err := g.CreateRepository(ctx, name, namespaceID, "")
	return p.HTTPURL, p.PathNS, err
}

// CreateRepository returns the provider identity as well as its observed location.
func (g *Client) CreateRepository(ctx context.Context, name string, namespaceID int, association string) (ProjectRef, error) {
	var p ProjectRef
	body := map[string]any{
		"name": name, "path": name, "visibility": "private",
		"initialize_with_readme": false,
		"description":            association,
	}
	if namespaceID > 0 {
		body["namespace_id"] = namespaceID
	}
	err := g.Do(ctx, "POST", "/projects", body, &p)
	return p, err
}

// ProjectRef is a GitLab project's permanent id, current path and archive state.
type ProjectRef struct {
	DefaultBranch string `json:"default_branch"`
	Description   string `json:"description"`
	CreatorID     int64  `json:"creator_id"`
	ID            int64  `json:"id"`
	HTTPURL       string `json:"http_url_to_repo"`
	SSHURL        string `json:"ssh_url_to_repo"`
	PathNS        string `json:"path_with_namespace"`
	Archived      bool   `json:"archived"`
}

// Project reads a project by its id or its path with namespace.
func (g *Client) Project(ctx context.Context, ref string) (ProjectRef, error) {
	var p ProjectRef
	err := g.Do(ctx, "GET", "/projects/"+url.PathEscape(ref), nil, &p)
	return p, err
}

// RenameProject moves a project, addressed by its permanent id, to newPath
// within its namespace.
func (g *Client) RenameProject(ctx context.Context, id int64, newPath string) error {
	return g.Do(ctx, "PUT", fmt.Sprintf("/projects/%d", id), map[string]any{"name": newPath, "path": newPath}, nil)
}

// ArchiveProject makes a project read-only. Unlike ArchiveRename, a refusal is
// returned: the token needs Owner on the project to archive it.
func (g *Client) ArchiveProject(ctx context.Context, id int64) error {
	return g.Do(ctx, "POST", fmt.Sprintf("/projects/%d/archive", id), nil, nil)
}

// ArchiveRename frees the original path and archives the renamed remote.
// Archive failure is logged after a successful rename.
func (g *Client) ArchiveRename(ctx context.Context, pathNS, newPath string) error {
	id := url.PathEscape(pathNS)
	if err := g.Do(ctx, "PUT", "/projects/"+id, map[string]any{"name": newPath, "path": newPath}, nil); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	// The rename changed the path — address the archive call by the NEW path.
	ns := pathNS
	if i := strings.LastIndex(pathNS, "/"); i >= 0 {
		ns = pathNS[:i+1]
	} else {
		ns = ""
	}
	// Archiving needs Owner on the project; AppTown's token is typically
	// Maintainer (rename works, archive 403s). The rename is what frees the
	// name and protects against collisions — archive is belt-and-braces, so
	// its failure is reported in the log, not to the caller.
	if err := g.Do(ctx, "POST", "/projects/"+url.PathEscape(ns+newPath)+"/archive", nil, nil); err != nil {
		log.Printf("gitlab archive of %s (renamed ok): %v", newPath, err)
	}
	return nil
}

func (g *Client) GetMR(ctx context.Context, projectPath string, iid int) (*MergeRequest, error) {
	var mr MergeRequest
	err := g.DoContext(ctx, "GET", fmt.Sprintf("/projects/%s/merge_requests/%d", url.PathEscape(projectPath), iid), nil, &mr)
	return &mr, err
}

func (g *Client) CreateMR(ctx context.Context, projectPath, source, target, title, description string) (*MergeRequest, error) {
	var mr MergeRequest
	err := g.Do(ctx, "POST", "/projects/"+url.PathEscape(projectPath)+"/merge_requests", map[string]any{
		"source_branch": source, "target_branch": target,
		"title": title, "description": description,
	}, &mr)
	return &mr, err
}

// UpdateMRDescription replaces a merge request's whole description. GitLab
// runs quick actions found in a saved description as this token's owner, so
// a caller sends only text it has made inert.
func (g *Client) UpdateMRDescription(ctx context.Context, projectPath string, iid int, description string) error {
	return g.DoContext(ctx, "PUT", fmt.Sprintf("/projects/%s/merge_requests/%d", url.PathEscape(projectPath), iid),
		map[string]any{"description": description}, nil)
}

// MergeMR merges a merge request only if its head is sha, never squashed.
func (g *Client) MergeMR(ctx context.Context, projectPath string, iid int, sha, message string) error {
	return g.DoContext(ctx, "PUT", fmt.Sprintf("/projects/%s/merge_requests/%d/merge", url.PathEscape(projectPath), iid),
		struct {
			Message string `json:"merge_commit_message"`
			SHA     string `json:"sha"`
			Squash  bool   `json:"squash"`
		}{message, sha, false}, nil)
}

// MergeSettings is how a GitLab project merges: its merge method ("merge",
// "rebase_merge" for semi-linear history, or "ff"), its squash option
// ("never", "always", "default_on" or "default_off"), and whether it rebases
// automatically before merging.
type MergeSettings struct {
	MergeMethod  string `json:"merge_method"`
	SquashOption string `json:"squash_option"`
	// AutomaticRebase: GitLab rebases the source onto the target itself
	// before merging, landing commits other than the source's. Nil when the
	// project does not say.
	AutomaticRebase *bool `json:"automatic_rebase_enabled"`
}

func (g *Client) MergeSettings(ctx context.Context, projectPath string) (MergeSettings, error) {
	var s MergeSettings
	err := g.DoContext(ctx, "GET", "/projects/"+url.PathEscape(projectPath), nil, &s)
	return s, err
}

// BranchHead is the commit a project's branch points at.
func (g *Client) BranchHead(ctx context.Context, projectPath, branch string) (string, error) {
	var b struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	err := g.DoContext(ctx, "GET", "/projects/"+url.PathEscape(projectPath)+"/repository/branches/"+url.PathEscape(branch), nil, &b)
	return b.Commit.ID, err
}

// AskpassEnv keeps the token out of Git configuration. The helper is published
// atomically so concurrent projects never observe a partial script.
func (g *Client) AskpassEnv(dataDir string) ([]string, error) {
	helper := filepath.Join(dataDir, "git-askpass.sh")
	if _, err := os.Stat(helper); err != nil {
		// Publish a complete helper atomically; different projects may prepare
		// provider credentials concurrently.
		temp, err := os.CreateTemp(dataDir, ".git-askpass-*")
		if err != nil {
			return nil, err
		}
		defer os.Remove(temp.Name())
		if err := temp.Chmod(0o700); err != nil {
			temp.Close()
			return nil, err
		}
		if _, err := temp.WriteString("#!/bin/sh\necho \"$APPTOWN_GIT_TOKEN\"\n"); err != nil {
			temp.Close()
			return nil, err
		}
		if err := temp.Close(); err != nil {
			return nil, err
		}
		if err := os.Rename(temp.Name(), helper); err != nil {
			return nil, err
		}
	}
	return []string{"GIT_ASKPASS=" + helper, "APPTOWN_GIT_TOKEN=" + g.token, "GIT_TERMINAL_PROMPT=0"}, nil
}

type MergeRequest struct {
	IID             int    `json:"iid"`
	WebURL          string `json:"web_url"`
	State           string `json:"state"`
	SHA             string `json:"sha"`
	MergeCommitSHA  string `json:"merge_commit_sha"`
	SquashCommitSHA string `json:"squash_commit_sha"`
	SourceBranch    string `json:"source_branch"`
	TargetBranch    string `json:"target_branch"`
	// Squash: the request squashes its commits when merged.
	Squash bool `json:"squash"`
}

type Client struct {
	http  *http.Client
	base  string
	token string
}

// BaseURL is the public provider location, without credentials.
func (g *Client) BaseURL() string { return g.base }
