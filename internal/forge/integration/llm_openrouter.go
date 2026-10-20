package integration

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/openrouter"
)

// AI models (the llm need), through OpenRouter
// (design/subsystems/integration-runtime.md). The instance binds an
// OpenRouter provisioning key once (a connector); a project opts in by
// attaching that connector; then each deployment and each preview of an app
// that declares the need gets its own ordinary OpenRouter key with a
// spending limit, which it owns: take-down and the end of a preview revoke
// it. The provisioning key is resolved here, inside the AppTown process, for
// the call that needs it, and goes nowhere else.

const (
	CapLLM = "llm"
	// AdapterOpenRouterKey is the register adapter the llm kind names.
	AdapterOpenRouterKey = "openrouter-key"
	// LLMKeyName is the name the key value is stored under, in its owner's scope.
	LLMKeyName = "OPENROUTER_API_KEY"
	// defaultLLMLimit is the default and preview limit, in US dollars, when
	// the connector sets none.
	defaultLLMLimit = 1.0
)

var (
	// ErrLLMNotAttached: the app declares the need, but nobody allowed to
	// attach integrations to the project has attached AI models.
	ErrLLMNotAttached = fault.New(fault.Unprocessable, "this app needs AI models (llm), but they are not attached to the project: attach AI models under Settings › Services first")
	// ErrLLMLimit: the declared limit is not one the connector allows.
	ErrLLMLimit = fault.New(fault.Unprocessable, "AI models limit refused")
	// ErrLLMConfig: the connector's configuration cannot be used.
	ErrLLMConfig = fault.New(fault.Unprocessable, "the AI models connector cannot be used")
)

// llmConfig is an llm connector's configuration, validated.
type llmConfig struct {
	Base         string // the API root apps and the key API use
	KeyFrom      []string
	DefaultLimit float64
	MaxLimit     float64
	PreviewLimit float64
}

func number(v any) (float64, bool, error) {
	switch t := v.(type) {
	case nil:
		return 0, false, nil
	case float64:
		return t, true, nil
	case int:
		return float64(t), true, nil
	case string:
		if strings.TrimSpace(t) == "" {
			return 0, false, nil
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil, err
	}
	return 0, false, fmt.Errorf("not a number")
}

// parseLLMConfig validates an llm connector's configuration. The
// provisioning key may not come from a STEP_ secret: those are delivered to
// workflow steps and workers, and a key that can make keys must reach
// neither.
func parseLLMConfig(config map[string]any) (llmConfig, error) {
	cfg := llmConfig{Base: openrouter.DefaultBase, KeyFrom: ConfigStrings(config["provisioningKeyFrom"])}
	if u, _ := config["url"].(string); strings.TrimSpace(u) != "" {
		cfg.Base = strings.TrimSuffix(strings.TrimSpace(u), "/")
	}
	if len(cfg.KeyFrom) == 0 {
		return cfg, fmt.Errorf("%w: name the secret that holds the OpenRouter provisioning key", ErrLLMConfig)
	}
	for _, ref := range cfg.KeyFrom {
		if strings.HasPrefix(ref, "STEP_") {
			return cfg, fmt.Errorf("%w: the provisioning key may not come from %s: secrets starting STEP_ are given to workflow steps and workers; use a name such as OPENROUTER_PROVISIONING_KEY", ErrLLMConfig, ref)
		}
	}
	read := func(key string, fallback float64) (float64, error) {
		v, set, err := number(config[key])
		if err != nil || (set && (v <= 0 || math.IsInf(v, 0) || math.IsNaN(v))) {
			return 0, fmt.Errorf("%w: %s must be a positive number of US dollars", ErrLLMConfig, key)
		}
		if !set {
			return fallback, nil
		}
		return v, nil
	}
	var err error
	if cfg.MaxLimit, err = read("maxLimit", 0); err != nil {
		return cfg, err
	}
	if cfg.MaxLimit == 0 {
		return cfg, fmt.Errorf("%w: set maxLimit, the most any one key may be given", ErrLLMConfig)
	}
	if cfg.DefaultLimit, err = read("defaultLimit", math.Min(defaultLLMLimit, cfg.MaxLimit)); err != nil {
		return cfg, err
	}
	if cfg.PreviewLimit, err = read("previewLimit", math.Min(defaultLLMLimit, cfg.MaxLimit)); err != nil {
		return cfg, err
	}
	if cfg.DefaultLimit > cfg.MaxLimit || cfg.PreviewLimit > cfg.MaxLimit {
		return cfg, fmt.Errorf("%w: defaultLimit and previewLimit may not exceed maxLimit (%s)", ErrLLMConfig, dollars(cfg.MaxLimit))
	}
	return cfg, nil
}

func dollars(f float64) string { return "$" + strconv.FormatFloat(f, 'f', -1, 64) }

// llmClient is the key API client for one llm connector, the provisioning
// key resolved now. The configuration is checked again here, so one saved
// before the rules existed cannot slip through.
func (s *Service) llmClient(ctx context.Context, binding string) (*openrouter.Client, llmConfig, error) {
	b, err := Selected(ctx, s.Bindings.db, CapLLM, binding)
	if err != nil {
		return nil, llmConfig{}, fmt.Errorf("reading the AI models connector %s: %w", binding, err)
	}
	cfg, err := parseLLMConfig(parseJSONMap(b.ConfigJSON))
	if err != nil {
		return nil, cfg, err
	}
	key, err := s.provisioningKey(ctx, cfg)
	if err != nil {
		return nil, cfg, err
	}
	return &openrouter.Client{Base: cfg.Base, ProvisioningKey: key}, cfg, nil
}

func (s *Service) provisioningKey(ctx context.Context, cfg llmConfig) (string, error) {
	if s.secrets == nil {
		return "", fmt.Errorf("%w: no secrets store", ErrLLMConfig)
	}
	values, err := s.secrets.WithContext(ctx)
	if err != nil {
		return "", fmt.Errorf("reading AppTown's secrets: %w", err)
	}
	for _, ref := range cfg.KeyFrom {
		if v := values[ref]; v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("%w: the secret %s holds no provisioning key", ErrLLMConfig, strings.Join(cfg.KeyFrom, ", "))
}

// TestLLMBinding checks a draft llm connector: valid configuration, the
// provisioning key resolves, and OpenRouter accepts it (it lists keys and
// changes nothing).
func (s *Service) TestLLMBinding(ctx context.Context, config map[string]any) Health {
	cfg, err := parseLLMConfig(config)
	if err != nil {
		return Health{OK: false, Detail: err.Error()}
	}
	key, err := s.provisioningKey(ctx, cfg)
	if err != nil {
		return Health{OK: false, Detail: err.Error()}
	}
	keys, err := (&openrouter.Client{Base: cfg.Base, ProvisioningKey: key}).List(ctx)
	if err != nil {
		return Health{OK: false, Detail: err.Error()}
	}
	return Health{OK: true, Detail: fmt.Sprintf("provisioning key accepted; it manages %d key(s); keys get %s by default, at most %s, previews %s", len(keys), dollars(cfg.DefaultLimit), dollars(cfg.MaxLimit), dollars(cfg.PreviewLimit))}
}

// HeldKey is one OpenRouter key something owns: the connector it was made
// through and OpenRouter's identity of it (never the key itself).
type HeldKey struct {
	Connector string
	Hash      string
}

// Ident is how an owner's record names it.
func (k HeldKey) Ident() string { return k.Connector + "/" + k.Hash }

// ParseHeldKey reads an Ident.
func ParseHeldKey(ident string) (HeldKey, bool) {
	c, h, ok := strings.Cut(ident, "/")
	return HeldKey{Connector: c, Hash: h}, ok && c != "" && h != ""
}

// KeyRecorder is the owner's record of the keys it holds: Record is called
// as soon as OpenRouter confirms a new key, before its value is stored;
// Forget when a key the owner held is revoked and replaced.
type KeyRecorder interface {
	Record(context.Context, HeldKey) error
	Forget(context.Context, HeldKey) error
}

// KeyRequest asks for an owner's key.
type KeyRequest struct {
	ProjectID ids.ID
	Name      string // the key's name at OpenRouter: says whose it is
	Scope     string // where its value is stored in the built-in store
	Preview   bool   // a preview: the connector's preview limit, whatever the app declares
	Params    map[string]string
	Held      *HeldKey // the key the owner already holds, if any
}

// Budget is the allowance an owner's key gets: the preview limit for a
// preview; else the app's declared limit (the connector's default when it
// declares none), never more than the connector's maximum, renewed every
// limitReset ("" never).
func (cfg llmConfig) budget(req KeyRequest) (openrouter.Budget, error) {
	if req.Preview {
		return openrouter.Budget{Limit: cfg.PreviewLimit}, nil
	}
	b := openrouter.Budget{Limit: cfg.DefaultLimit, Reset: strings.TrimSpace(req.Params["limitReset"])}
	if raw := strings.TrimSpace(req.Params["limit"]); raw != "" {
		f, err := strconv.ParseFloat(strings.TrimPrefix(raw, "$"), 64)
		if err != nil || f <= 0 || math.IsInf(f, 0) || math.IsNaN(f) {
			return b, fmt.Errorf("%w: needs: llm: limit must be a positive number of US dollars, not %q", ErrLLMLimit, raw)
		}
		b.Limit = f
	}
	if b.Limit > cfg.MaxLimit {
		return b, fmt.Errorf("%w: the app asks for %s, and the AI models connector allows at most %s per key", ErrLLMLimit, dollars(b.Limit), dollars(cfg.MaxLimit))
	}
	if !openrouter.ValidReset(b.Reset) {
		return b, fmt.Errorf("%w: needs: llm: limitReset is daily, weekly or monthly, not %q", ErrLLMLimit, b.Reset)
	}
	return b, nil
}

// RegisterLLMKey gives an owner its key and returns the values the need's
// inject templates read: key and base_url. It reads the project's
// attachment first and makes no OpenRouter call without one. An owner that
// holds a key keeps it, its budget set again (limit and reset together);
// one whose stored value is gone, or whose key OpenRouter no longer has,
// has it revoked and replaced. Any failure is returned: the caller boots
// nothing.
func (s *Service) RegisterLLMKey(ctx context.Context, req KeyRequest, rec KeyRecorder) (map[string]string, error) {
	binding, _, err := s.Attachments.Attached(ctx, req.ProjectID, CapLLM)
	if errors.Is(err, ErrNotAttached) {
		return nil, ErrLLMNotAttached
	}
	if err != nil {
		return nil, fmt.Errorf("reading the project's AI models attachment: %w", err)
	}
	client, cfg, err := s.llmClient(ctx, binding)
	if err != nil {
		return nil, err
	}
	budget, err := cfg.budget(req)
	if err != nil {
		return nil, err
	}
	out := func(value string) map[string]string { return map[string]string{"key": value, "base_url": cfg.Base} }
	if held := req.Held; held != nil {
		value, _, err := s.secrets.Vault.Get(ctx, req.Scope, LLMKeyName)
		switch {
		case err != nil && !errors.Is(err, secret.ErrNotStored):
			return nil, fmt.Errorf("reading the stored AI models key: %w", err)
		case err == nil && held.Connector == binding:
			err := client.SetBudget(ctx, held.Hash, budget)
			var oe *openrouter.Error
			if err == nil {
				return out(value), nil
			}
			if !errors.As(err, &oe) || !oe.NotFound() {
				return nil, fmt.Errorf("setting the key's limit to %s: %w", describeBudget(budget), err)
			}
			// OpenRouter no longer has it (revoked by hand): replace it.
		}
		// The owner's key cannot be used as it is: revoke it, then make
		// another. A revoke that fails stops here; no second key is made.
		if err := s.ReleaseLLMKey(ctx, *held, req.Scope); err != nil {
			return nil, fmt.Errorf("replacing the AI models key: %w", err)
		}
		if err := rec.Forget(ctx, *held); err != nil {
			return nil, err
		}
	}
	k, value, err := client.Create(ctx, req.Name, budget)
	if err != nil {
		return nil, fmt.Errorf("making an AI models key (%s): %w", describeBudget(budget), err)
	}
	held := HeldKey{Connector: binding, Hash: k.Hash}
	if err := rec.Record(ctx, held); err != nil {
		// Not recorded, so nothing would ever revoke it: revoke it now.
		if derr := client.Delete(context.WithoutCancel(ctx), k.Hash); derr != nil {
			return nil, fmt.Errorf("recording the new AI models key %s: %w (revoking it failed too, so it is left at OpenRouter: %v)", req.Name, err, derr)
		}
		return nil, fmt.Errorf("recording the new AI models key: %w", err)
	}
	if _, err := s.secrets.Vault.Put(ctx, req.Scope, LLMKeyName, value, "apptown-system"); err != nil {
		// Recorded without a value: the next boot revokes and replaces it.
		return nil, fmt.Errorf("storing the new AI models key: %w", err)
	}
	return out(value), nil
}

func describeBudget(b openrouter.Budget) string {
	if b.Reset == "" {
		return dollars(b.Limit) + ", never renewed"
	}
	return dollars(b.Limit) + " " + b.Reset
}

// ReleaseLLMKey revokes a key at OpenRouter and deletes its stored value.
// A key OpenRouter does not have counts as revoked. The error names what
// failed, never a key.
func (s *Service) ReleaseLLMKey(ctx context.Context, held HeldKey, scope string) error {
	client, _, err := s.llmClient(ctx, held.Connector)
	if err != nil {
		return err
	}
	var oe *openrouter.Error
	if err := client.Delete(ctx, held.Hash); err != nil && !(errors.As(err, &oe) && oe.NotFound()) {
		return err
	}
	if scope == "" {
		return nil
	}
	if err := s.secrets.Vault.Delete(ctx, scope, LLMKeyName); err != nil {
		return fmt.Errorf("deleting the stored AI models key: %w", err)
	}
	return nil
}

// Allowance is what OpenRouter says about a held key now.
type Allowance struct {
	Limit     *float64 `json:"limit,omitempty"`
	Remaining *float64 `json:"remaining,omitempty"`
	Reset     string   `json:"reset,omitempty"` // daily | weekly | monthly; "" never
	Period    float64  `json:"periodSpend"`     // spend in the current reset period
	Lifetime  float64  `json:"lifetimeSpend"`
	Disabled  bool     `json:"disabled,omitempty"`
}

// LLMAllowance reads a held key's allowance and spend from OpenRouter.
func (s *Service) LLMAllowance(ctx context.Context, held HeldKey) (Allowance, error) {
	client, _, err := s.llmClient(ctx, held.Connector)
	if err != nil {
		return Allowance{}, err
	}
	k, err := client.Get(ctx, held.Hash)
	if err != nil {
		return Allowance{}, err
	}
	a := Allowance{Limit: k.Limit, Remaining: k.Remaining, Period: k.UsagePeriod(), Lifetime: k.Usage, Disabled: k.Disabled}
	if k.Reset != nil {
		a.Reset = *k.Reset
	}
	return a, nil
}
