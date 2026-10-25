package gitexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/durable"
)

// LocalURL is a Git remote URL that reads the local filesystem. Remote HTTPS,
// SSH and scp URLs are independent upstream identity and are never returned.
type LocalURL struct{ Key, Value, Path string }

func LocalURLs(ctx context.Context, repository string) ([]LocalURL, error) {
	path, err := localConfig(repository)
	if err != nil {
		return nil, err
	}
	entries, err := readConfig(ctx, path)
	if err != nil {
		return nil, err
	}
	var out []LocalURL
	for _, entry := range entries {
		if strings.HasPrefix(entry.key, "include.") || strings.HasPrefix(entry.key, "includeif.") {
			return nil, errors.New("included Git config must be resolved before repository relocation")
		}
		if !strings.HasPrefix(entry.key, "remote.") || !(strings.HasSuffix(entry.key, ".url") || strings.HasSuffix(entry.key, ".pushurl")) {
			continue
		}
		value := entry.value
		if strings.HasPrefix(value, "file:") {
			u, err := url.Parse(value)
			if err != nil || u.Scheme != "file" || u.Host != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !filepath.IsAbs(u.Path) {
				return nil, fmt.Errorf("ambiguous local Git URL in %s", entry.key)
			}
			value = u.Path
		} else if strings.Contains(value, ":") {
			continue
		}
		if value == "" {
			return nil, fmt.Errorf("empty Git remote URL in %s", entry.key)
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(repository, value)
		}
		out = append(out, LocalURL{Key: entry.key, Value: entry.value, Path: filepath.Clean(value)})
	}
	return out, nil
}

// URLChange is one verified local path change. No external URL or credential
// is accepted as a new value by this relocation boundary.
type URLChange struct {
	Key    string `json:"key"`
	Before string `json:"before"`
	After  string `json:"after"`
}
type ConfigRelocation struct {
	Repository   string      `json:"repository"`
	BeforeDigest string      `json:"beforeDigest"`
	AfterDigest  string      `json:"afterDigest"`
	Changes      []URLChange `json:"changes"`
}

func PlanConfigRelocation(ctx context.Context, repository string, changes []URLChange) (ConfigRelocation, error) {
	var edit ConfigRelocation
	path, err := localConfig(repository)
	if err != nil {
		return edit, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return edit, err
	}
	next, err := changedConfig(ctx, raw, changes)
	if err != nil {
		return edit, err
	}
	return ConfigRelocation{Repository: repository, BeforeDigest: configDigest(raw), AfterDigest: configDigest(next), Changes: slices.Clone(changes)}, nil
}

func (e ConfigRelocation) Apply(ctx context.Context) error {
	path, err := localConfig(e.Repository)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if configDigest(raw) == e.AfterDigest {
		return durable.SyncDir(filepath.Dir(path))
	}
	if configDigest(raw) != e.BeforeDigest {
		return fmt.Errorf("Git config changed during relocation: %s", e.Repository)
	}
	next, err := changedConfig(ctx, raw, e.Changes)
	if err != nil {
		return err
	}
	if configDigest(next) != e.AfterDigest {
		return errors.New("Git config relocation does not match its journal")
	}
	_, err = durable.Write(filepath.Dir(path), filepath.Base(path), next, 0o600, false)
	return err
}

func localConfig(repository string) (string, error) {
	for _, path := range []string{repository, filepath.Join(repository, ".git")} {
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("Git relocation path is not a directory: %s", path)
		}
	}
	path := filepath.Join(repository, ".git", "config")
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("Git relocation config is not a regular file")
	}
	return path, nil
}
func changedConfig(ctx context.Context, raw []byte, changes []URLChange) ([]byte, error) {
	f, err := os.CreateTemp("", "apptown-layout-config-")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	entries, err := readConfig(ctx, path)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, change := range changes {
		if seen[change.Key] || !strings.HasPrefix(change.Key, "remote.") || !(strings.HasSuffix(change.Key, ".url") || strings.HasSuffix(change.Key, ".pushurl")) || !filepath.IsAbs(change.After) || filepath.Clean(change.After) != change.After {
			return nil, errors.New("invalid local Git URL relocation")
		}
		if strings.Contains(change.Before, ":") && !strings.HasPrefix(change.Before, "file:") {
			return nil, errors.New("external Git URL cannot be relocated as a local path")
		}
		seen[change.Key] = true
		count := 0
		for _, entry := range entries {
			if entry.key == change.Key {
				count++
				if entry.value != change.Before {
					return nil, errors.New("Git URL no longer matches relocation input")
				}
			}
		}
		if count != 1 {
			return nil, errors.New("Git URL relocation requires one unambiguous value")
		}
		cmd, done, err := homeCommand(ctx, []string{"config", "--file", path, "--replace-all", change.Key, change.After})
		if err != nil {
			done()
			return nil, err
		}
		_, err = cmd.Output()
		done()
		if err != nil {
			return nil, err
		}
	}
	return os.ReadFile(path)
}
func configDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
