package gitlab

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"
)

// CanonicalBase is the credential-free provider namespace used in durable
// repository identity. Ambiguous addresses are refused, not normalized across
// hosts, protocols or reverse-proxy mount points.
func CanonicalBase(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || strings.ContainsAny(u.Path, "\\\x00\r\n") {
		return "", fmt.Errorf("GitLab needs an unambiguous HTTP(S) provider address without credentials, query or fragment")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path != "" && path.Clean(u.Path) != u.Path {
		return "", fmt.Errorf("GitLab provider address has an ambiguous path")
	}
	return u.String(), nil
}

// AccountID observes the authenticated account using this client's credential
// snapshot. It must not be followed by resolving a different credential for
// the effect it admits.
func (g *Client) AccountID(ctx context.Context) (int64, error) {
	var account struct {
		ID int64 `json:"id"`
	}
	if err := g.Do(ctx, "GET", "/user", nil, &account); err != nil {
		return 0, err
	}
	if account.ID <= 0 {
		return 0, fmt.Errorf("GitLab did not identify the authenticated account")
	}
	return account.ID, nil
}

// RepositoryURL checks a clone URL against the exact provider namespace and
// returns its credential-free location and relative project path. A username
// is transport metadata; a password is never accepted or retained.
func RepositoryURL(base, raw string) (location, projectPath string, err error) {
	canonical, err := CanonicalBase(base)
	if err != nil {
		return "", "", err
	}
	b, _ := url.Parse(canonical)
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return "", "", fmt.Errorf("unreadable GitLab repository address")
	}
	if u.User != nil {
		if _, password := u.User.Password(); password {
			return "", "", fmt.Errorf("GitLab repository address must not contain a password")
		}
		u.User = nil
	}
	u.Host = strings.ToLower(u.Host)
	if u.Scheme != b.Scheme || u.Host != b.Host || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || strings.ContainsAny(u.Path, "\\\x00\r\n") || path.Clean(u.Path) != u.Path || !strings.HasPrefix(u.Path, b.Path+"/") {
		return "", "", fmt.Errorf("repository address is outside its recorded GitLab provider")
	}
	projectPath = strings.TrimSuffix(strings.TrimPrefix(u.Path, b.Path+"/"), ".git")
	if projectPath == "" || !strings.Contains(projectPath, "/") || strings.HasSuffix(projectPath, "/") {
		return "", "", fmt.Errorf("GitLab repository address needs a namespace and project")
	}
	return u.String(), projectPath, nil
}
