package secret

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// SetBootstrap replaces one named credential in the instance bootstrap file,
// recorded as an instance secret change (instanceReceipt). Values are
// single-line credentials; the caller selects a supported provider.
func (a *Service) SetBootstrap(ctx context.Context, name, value, who string) error {
	if !ValidName(name) || strings.ContainsAny(value, "\n\r") {
		return errors.New("bootstrap credential requires a valid name and single-line value")
	}
	return a.instanceReceipt(ctx, who, "secret.instance-set", name, func() (bool, error) {
		return a.rewriteBootstrap(name, &value)
	})
}

// rewriteBootstrap drops name's line from the bootstrap file and, when value
// is not nil, appends name=value. It reports whether name had a line. Other
// lines, comments included, are kept in order. The file is replaced whole
// (written beside it, then renamed), so a failure leaves the old file; an
// unreadable file is an error, never replaced by a fresh one. Writers in this
// process are serialised so one cannot lose another's line.
func (a *Service) rewriteBootstrap(name string, value *string) (had bool, err error) {
	a.bootstrapMu.Lock()
	defer a.bootstrapMu.Unlock()
	envPath := filepath.Join(a.dataDir, "secrets.env")
	raw, err := os.ReadFile(envPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l == "" {
			continue
		}
		if bootstrapKey(l) == name {
			had = true
			continue
		}
		lines = append(lines, l)
	}
	if value != nil {
		lines = append(lines, name+"="+*value)
	}
	out := ""
	if len(lines) > 0 {
		out = strings.Join(lines, "\n") + "\n"
	}
	tmp, err := os.CreateTemp(a.dataDir, ".secrets.env.*")
	if err != nil {
		return had, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return had, err
	}
	if _, err := tmp.WriteString(out); err != nil {
		_ = tmp.Close()
		return had, err
	}
	if err := tmp.Close(); err != nil {
		return had, err
	}
	return had, os.Rename(tmp.Name(), envPath)
}

// bootstrapKey is the name a bootstrap line sets, read the way fileSecrets
// reads it; "" for a comment or a line without one.
func bootstrapKey(line string) string {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "#") {
		return ""
	}
	k, _, ok := strings.Cut(line, "=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(k)
}
