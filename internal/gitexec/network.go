package gitexec

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// PushVerifiedURL pushes to an already identity-verified HTTP(S) destination.
// It refuses local configuration that can redirect transport or read another
// configuration file before supplying credentials. The caller holds the hub's
// repository lock and retains the same credential snapshot used for identity.
func PushVerifiedURL(ctx context.Context, dir, destination, branch string, credentials []string) (string, error) {
	if _, err := HubRun(ctx, dir, "check-ref-format", "refs/heads/"+branch); err != nil {
		return "", fmt.Errorf("invalid push branch")
	}
	return verifiedNetworkURL(ctx, dir, destination, credentials, "push", "refs/heads/"+branch+":refs/heads/"+branch)
}

// PushVerifiedRevision publishes a pinned commit, never a caller-provided refspec.
func PushVerifiedRevision(ctx context.Context, dir, destination, revision, branch string, credentials []string) (string, error) {
	if len(revision) != 40 && len(revision) != 64 {
		return "", fmt.Errorf("invalid pinned push revision")
	}
	for _, c := range revision {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", fmt.Errorf("invalid pinned push revision")
		}
	}
	if _, err := HubRun(ctx, dir, "check-ref-format", "refs/heads/"+branch); err != nil {
		return "", fmt.Errorf("invalid push branch")
	}
	return verifiedNetworkURL(ctx, dir, destination, credentials, "push", revision+":refs/heads/"+branch)
}

// FetchVerifiedURL fetches one branch into FETCH_HEAD under the same destination
// and credential rules as publication. It does not use origin's fetch mapping.
func FetchVerifiedURL(ctx context.Context, dir, destination, branch string, credentials []string) (string, error) {
	if _, err := HubRun(ctx, dir, "check-ref-format", "refs/heads/"+branch); err != nil {
		return "", fmt.Errorf("invalid fetch branch")
	}
	return verifiedNetworkURL(ctx, dir, destination, credentials, "fetch", "refs/heads/"+branch)
}

func verifiedNetworkURL(ctx context.Context, dir, destination string, credentials []string, operation, refspec string) (string, error) {
	u, err := url.Parse(destination)
	if err != nil || u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("verified Git push requires an HTTP(S) destination")
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			return "", fmt.Errorf("verified Git push refuses URL credentials")
		}
	}
	if err := checkNetworkConfig(ctx, dir, destination); err != nil {
		return "", err
	}
	args := append(append([]string{}, hubFlags...), "-c", "protocol.http.allow=always", "-c", "protocol.https.allow=always", "-c", "http.followRedirects=false", "-c", "credential.helper=", operation, "--", destination, refspec)
	cmd, done, err := homeCommand(ctx, args)
	if err != nil {
		return "", err
	}
	defer done()
	cmd.Dir = dir
	cmd.Env = append(cmd.Env, trustEnv()...)
	for _, entry := range credentials {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || (name != "GIT_ASKPASS" && name != "APPTOWN_GIT_TOKEN" && name != "GIT_TERMINAL_PROMPT") {
			return "", fmt.Errorf("unexpected Git credential environment")
		}
		cmd.Env = append(cmd.Env, entry)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func checkNetworkConfig(ctx context.Context, dir, destination string) error {
	gitdir := filepath.Join(dir, ".git")
	st, err := os.Lstat(gitdir)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("verified Git push needs the hub's own Git directory")
	}
	for _, name := range []string{"commondir", "gitdir", "config.worktree"} {
		if _, err := os.Lstat(filepath.Join(gitdir, name)); err == nil || !os.IsNotExist(err) {
			return fmt.Errorf("verified Git push refuses alternate Git configuration")
		}
	}
	config := filepath.Join(gitdir, "config")
	st, err = os.Lstat(config)
	if err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("verified Git push needs a regular local configuration")
	}
	entries, err := readConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("reading Git push configuration: %w", err)
	}
	for _, entry := range entries {
		key := strings.ToLower(entry.key)
		switch key {
		case "core.repositoryformatversion", "core.filemode", "core.bare", "core.logallrefupdates", "core.ignorecase", "core.precomposeunicode", "user.name", "user.email", "init.defaultbranch", "extensions.objectformat", "remote.origin.url", "remote.origin.fetch":
			// Ordinary hub identity and object-format facts do not select
			// another network destination or run a credential helper.
		case "remote.origin.pushurl":
			if entry.value != destination {
				return fmt.Errorf("Git origin has a push destination different from its verified attachment")
			}
		default:
			if strings.HasPrefix(key, "branch.") && (strings.HasSuffix(key, ".remote") || strings.HasSuffix(key, ".merge")) {
				continue // the explicit refspec and URL do not consult these
			}
			return fmt.Errorf("Git configuration %s is not supported for a verified push", key)
		}
	}
	return nil
}
