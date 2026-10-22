package secret

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/database"
)

// Secrets resolution. Layering:
//
//  1. Infisical (optional): a Machine Identity (Universal Auth) is the single
//     bootstrap credential; all other secrets live server-side and are fetched
//     with a short cache. Configured via the bootstrap file:
//     INFISICAL_URL, INFISICAL_CLIENT_ID, INFISICAL_CLIENT_SECRET,
//     INFISICAL_PROJECT_ID, INFISICAL_ENV (default "prod"),
//     INFISICAL_PATH (default "/").
//  2. secrets.env (the bootstrap file): holds the machine identity, plus any
//     local overrides — file values win over Infisical.
//
// Engine/step split: engine secrets (GITLAB_TOKEN, the
// Infisical identity itself) NEVER enter step processes. Only keys prefixed
// STEP_ are injected into step environments, with the prefix stripped
// (STEP_FOO=bar → FOO=bar in the container).

func infisicalConfig(file map[string]string) InfisicalConfig {
	return InfisicalConfig{URL: file["INFISICAL_URL"], ClientID: file["INFISICAL_CLIENT_ID"], ClientSecret: file["INFISICAL_CLIENT_SECRET"], ProjectID: file["INFISICAL_PROJECT_ID"], Environment: file["INFISICAL_ENV"], Path: file["INFISICAL_PATH"]}
}

func (a *Service) fileSecrets() map[string]string {
	out, _ := a.readFileSecrets()
	return out
}

// readFileSecrets reads the bootstrap file: no file is an empty map, any
// other read failure is an error (runtime lookup falls back to empty; the
// administrative instance-secret door does not).
func (a *Service) readFileSecrets() (map[string]string, error) {
	out := map[string]string{}
	raw, err := os.ReadFile(filepath.Join(a.dataDir, "secrets.env"))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("reading AppTown's secrets file: %w", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out, nil
}

// All returns the merged secret map: Infisical base, file overrides.
func (a *Service) All(ctx context.Context) map[string]string {
	// The answer cannot say "unknown": a stopped read would be "no secrets",
	// so it runs to its end though the caller stops.
	values, _ := a.WithContext(context.WithoutCancel(ctx))
	return values
}

// WithContext preserves file overrides and ordinary provider-failure
// fallback, but never satisfies a cancelled caller from stale values.
func (a *Service) WithContext(ctx context.Context) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file := a.fileSecrets()
	out := map[string]string{}
	if cfg := infisicalConfig(file); cfg.Configured() {
		remote, err := a.infisicalFetch(ctx, cfg)
		if err == context.Canceled || err == context.DeadlineExceeded {
			return nil, err
		}
		for k, v := range remote {
			out[k] = v
		}
	}
	for k, v := range file {
		out[k] = v
	}
	return out, ctx.Err()
}

func (a *Service) Value(ctx context.Context, key string) string {
	return a.All(ctx)[key]
}

// StepEnv selects STEP_ entries from runtime resolution and strips the prefix.
// Separately authorized integration, registry and SCM delivery belongs to callers.
func (a *Service) StepEnv(ctx context.Context) []string {
	var out []string
	for k, v := range a.All(ctx) {
		if name, ok := strings.CutPrefix(k, "STEP_"); ok && name != "" {
			out = append(out, name+"="+v)
		}
	}
	return out
}

// infisicalFetch adapts the resolver's health transitions to existing
// best-effort receipts. No receipt or network I/O holds the resolver mutex.
func (a *Service) infisicalFetch(ctx context.Context, cfg InfisicalConfig) (map[string]string, error) {
	result, err := a.infisical.Resolve(ctx, cfg, a.client)
	kind, subject := "", ""
	var payload any
	switch result.Transition {
	case RefreshFailed:
		kind, subject = "infisical.error", "secrets fetch failing"
		payload = map[string]string{"error": err.Error()}
	case RefreshRecovered:
		kind, subject = "infisical.recovered", "secrets fetch recovered"
	}
	if kind != "" && a.Vault.db != nil {
		// The transition has happened: its receipt is written though the
		// caller has gone.
		detached := context.WithoutCancel(ctx)
		_, receiptErr := database.Transact(detached, a.Vault.db, func(tx *database.Tx) (struct{}, error) {
			return struct{}{}, audit.At(a.now()).RecordIn(detached, tx, "apptown-system", ids.Nil, kind, subject, payload)
		})
		if receiptErr != nil {
			log.Printf("Infisical transition receipt failed: %v", receiptErr)
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return result.Values, err
}

// record preserves the broker's existing best-effort metadata receipts.
func (a *Service) record(ctx context.Context, who string, projectID ids.ID, kind, subject string, payload any) {
	raw, err := json.Marshal(payload)
	if err == nil {
		err = audit.Append(ctx, a.Vault.db, audit.At(a.now()).String(), who, projectID, kind, subject, string(raw))
	}
	if err != nil {
		log.Printf("best-effort secret receipt failed: %v", err)
	}
}
