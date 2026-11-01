package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/openrouter/openrouterfake"
)

const provisioningSentinel = "sk-or-v1-PROVISIONING-SENTINEL-7f3a"

// heldKeys is an owner's record of its keys, as the deployment keeps one.
type heldKeys struct{ keys []HeldKey }

func (h *heldKeys) Record(ctx context.Context, k HeldKey) error {
	h.keys = append(h.keys, k)
	return nil
}
func (h *heldKeys) Forget(ctx context.Context, k HeldKey) error {
	for i, x := range h.keys {
		if x == k {
			h.keys = append(h.keys[:i], h.keys[i+1:]...)
		}
	}
	return nil
}
func (h *heldKeys) held() *HeldKey {
	if len(h.keys) == 0 {
		return nil
	}
	k := h.keys[0]
	return &k
}

// llmFixture: a project, AppTown's secrets holding the provisioning key
// (and a STEP_ secret, as steps get), a fake OpenRouter, and an llm
// connector pointing at it with the given extra config.
func llmFixture(t *testing.T, config string) (*Service, ids.ID, *openrouterfake.Server) {
	t.Helper()
	s, id, dir := runtimeFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("OPENROUTER_PROVISIONING_KEY="+provisioningSentinel+"\nSTEP_OPENROUTER_PROVISIONING_KEY="+provisioningSentinel+"\nSTEP_OTHER=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := openrouterfake.New(provisioningSentinel)
	t.Cleanup(f.Close)
	cfg := `{"url":"` + f.Base + `","provisioningKeyFrom":["OPENROUTER_PROVISIONING_KEY"],"maxLimit":10,"defaultLimit":2,"previewLimit":1` + config + `}`
	if _, err := s.Bindings.Upsert(t.Context(), "openrouter", CapLLM, "openrouter", cfg, false); err != nil {
		t.Fatal(err)
	}
	return s, id, f
}

func attachLLM(t *testing.T, s *Service, id ids.ID) {
	t.Helper()
	if err := s.Attachments.Attach(t.Context(), id, CapLLM, "openrouter", "{}", "admin", nil); err != nil {
		t.Fatal(err)
	}
}

// Declaring the need is not enough: without the project's attachment no
// key is made and OpenRouter is not called at all.
func TestAIModelsNeedTheProjectAttachmentBeforeAnyProviderCall(t *testing.T) {
	t.Parallel()
	s, id, f := llmFixture(t, "")
	rec := &heldKeys{}
	_, err := s.RegisterLLMKey(context.Background(), KeyRequest{ProjectID: id, Name: "forge-demo-main-1", Scope: "llm:deployment:1"}, rec)
	if !errors.Is(err, ErrLLMNotAttached) {
		t.Fatalf("want not attached, got %v", err)
	}
	if n := len(f.Calls()); n != 0 || len(rec.keys) != 0 {
		t.Fatalf("an unattached project made %d OpenRouter calls and holds %d keys", n, len(rec.keys))
	}
	attachLLM(t, s, id)
	if _, err := s.RegisterLLMKey(context.Background(), KeyRequest{ProjectID: id, Name: "forge-demo-main-1", Scope: "llm:deployment:1"}, rec); err != nil {
		t.Fatalf("an attached project gets its key: %v", err)
	}
}

// A deployment's key: made once with the declared budget, recorded before
// its value is stored, reused on every later boot with its budget (limit
// and reset together) set again, and never more than the connector allows,
// on the first boot or any later one.
func TestAIModelsKeyIsMadeOnceAndItsBudgetFollowsTheDeclaration(t *testing.T) {
	t.Parallel()
	s, id, f := llmFixture(t, "")
	attachLLM(t, s, id)
	ctx := context.Background()
	rec := &heldKeys{}
	req := func(params map[string]string) KeyRequest {
		return KeyRequest{ProjectID: id, Name: "forge-demo-main-1", Scope: "llm:deployment:1", Params: params, Held: rec.held()}
	}

	// Over the ceiling on the first boot: refused before any call.
	if _, err := s.RegisterLLMKey(ctx, req(map[string]string{"limit": "11"}), rec); !errors.Is(err, errLLMLimit) || len(f.Calls()) != 0 {
		t.Fatalf("over the maximum: %v, %d calls", err, len(f.Calls()))
	}
	// No limit declared: the connector's default.
	env, err := s.RegisterLLMKey(ctx, req(map[string]string{"limitReset": "daily"}), rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.keys) != 1 || f.CallsOf("POST") != 1 {
		t.Fatalf("one key made and recorded: %+v, %d POSTs", rec.keys, f.CallsOf("POST"))
	}
	hash := rec.keys[0].Hash
	k := f.Keys()[hash]
	if *k.Limit != 2 || *k.Reset != "daily" || env["key"] != k.Value || env["base_url"] != f.Base {
		t.Fatalf("budget and values: %+v %v", k, env)
	}
	if stored, _, err := s.secrets.Vault.Get(ctx, "llm:deployment:1", LLMKeyName); err != nil || stored != k.Value {
		t.Fatalf("the value is stored with its owner: %v", err)
	}

	// The same amount, another reset; then no reset: the same key, changed.
	for _, change := range []struct {
		params map[string]string
		reset  *string
	}{
		{map[string]string{"limit": "2", "limitReset": "monthly"}, strPtr("monthly")},
		{map[string]string{"limit": "2"}, nil},
	} {
		env, err := s.RegisterLLMKey(ctx, req(change.params), rec)
		if err != nil {
			t.Fatal(err)
		}
		got := f.Keys()[hash]
		if env["key"] != k.Value || f.CallsOf("POST") != 1 || (got.Reset == nil) != (change.reset == nil) || (got.Reset != nil && *got.Reset != *change.reset) {
			t.Fatalf("%v: same key, reset now %v; %d POSTs", change.params, got.Reset, f.CallsOf("POST"))
		}
	}
	// Over the ceiling on a later boot: refused before the change.
	calls := len(f.Calls())
	if _, err := s.RegisterLLMKey(ctx, req(map[string]string{"limit": "50"}), rec); !errors.Is(err, errLLMLimit) || len(f.Calls()) != calls {
		t.Fatalf("over the maximum on a redeploy: %v, %d new calls", err, len(f.Calls())-calls)
	}
	// OpenRouter refuses the change: the boot fails, nothing claims it applied.
	f.FailNext("PATCH", 500)
	if _, err := s.RegisterLLMKey(ctx, req(map[string]string{"limit": "3"}), rec); err == nil {
		t.Fatal("a refused budget change must fail the boot")
	}
	if *f.Keys()[hash].Limit != 2 {
		t.Fatal("the refused change was applied")
	}
}

// A key whose stored value is gone, or which OpenRouter no longer has, is
// revoked and replaced; a stored value that cannot be read stops the boot.
func TestAIModelsKeyIsReplacedWhenItCannotBeUsed(t *testing.T) {
	t.Parallel()
	s, id, f := llmFixture(t, "")
	attachLLM(t, s, id)
	ctx := context.Background()
	rec := &heldKeys{}
	req := func() KeyRequest {
		return KeyRequest{ProjectID: id, Name: "forge-demo-main-1", Scope: "llm:deployment:1", Held: rec.held()}
	}
	if _, err := s.RegisterLLMKey(ctx, req(), rec); err != nil {
		t.Fatal(err)
	}
	first := rec.keys[0]
	if err := s.secrets.Vault.Delete(ctx, "llm:deployment:1", LLMKeyName); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterLLMKey(ctx, req(), rec); err != nil {
		t.Fatal(err)
	}
	if _, still := f.Keys()[first.Hash]; still || len(rec.keys) != 1 || rec.keys[0] == first {
		t.Fatalf("the unusable key is revoked and replaced: %+v", rec.keys)
	}
	second := rec.keys[0]
	f.Forget(second.Hash)
	if _, err := s.RegisterLLMKey(ctx, req(), rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.keys) != 1 || rec.keys[0] == second || len(f.Keys()) != 1 {
		t.Fatalf("a key OpenRouter lost is replaced: %+v", rec.keys)
	}
	// A revoke that fails stops the replacement: no second key.
	if err := s.secrets.Vault.Delete(ctx, "llm:deployment:1", LLMKeyName); err != nil {
		t.Fatal(err)
	}
	f.FailNext("DELETE", 503)
	posts := f.CallsOf("POST")
	if _, err := s.RegisterLLMKey(ctx, req(), rec); err == nil || f.CallsOf("POST") != posts {
		t.Fatalf("a failed revoke must stop before another key: %v", err)
	}
}

// Releasing revokes the key and deletes its value; a key OpenRouter does
// not have counts as revoked; a failed revoke is an error, value kept.
func TestAIModelsKeyReleaseRevokesAndDeletes(t *testing.T) {
	t.Parallel()
	s, id, f := llmFixture(t, "")
	attachLLM(t, s, id)
	ctx := context.Background()
	rec := &heldKeys{}
	if _, err := s.RegisterLLMKey(ctx, KeyRequest{ProjectID: id, Name: "k", Scope: "llm:deployment:1"}, rec); err != nil {
		t.Fatal(err)
	}
	held := rec.keys[0]
	f.FailNext("DELETE", 500)
	if err := s.ReleaseLLMKey(ctx, held, "llm:deployment:1"); err == nil {
		t.Fatal("a failed revoke is reported")
	}
	if _, _, err := s.secrets.Vault.Get(ctx, "llm:deployment:1", LLMKeyName); err != nil {
		t.Fatalf("the value stays while the key stands: %v", err)
	}
	if err := s.ReleaseLLMKey(ctx, held, "llm:deployment:1"); err != nil {
		t.Fatal(err)
	}
	if len(f.Keys()) != 0 {
		t.Fatal("the key is revoked")
	}
	if _, _, err := s.secrets.Vault.Get(ctx, "llm:deployment:1", LLMKeyName); !errors.Is(err, secret.ErrNotStored) {
		t.Fatalf("the value goes with the key: %v", err)
	}
	if err := s.ReleaseLLMKey(ctx, held, "llm:deployment:1"); err != nil {
		t.Fatalf("a key already gone counts as revoked: %v", err)
	}
}

// A preview's key always gets the connector's preview limit.
func TestAIModelsPreviewKeyGetsThePreviewLimit(t *testing.T) {
	t.Parallel()
	s, id, f := llmFixture(t, "")
	attachLLM(t, s, id)
	rec := &heldKeys{}
	if _, err := s.RegisterLLMKey(context.Background(), KeyRequest{ProjectID: id, Name: "apptown-preview-x", Scope: "llm:preview:x", Preview: true, Params: map[string]string{"limit": "9", "limitReset": "daily"}}, rec); err != nil {
		t.Fatal(err)
	}
	k := f.Keys()[rec.keys[0].Hash]
	if *k.Limit != 1 || k.Reset != nil {
		t.Fatalf("preview key budget: %v %v", *k.Limit, k.Reset)
	}
}

// The provisioning key stays on the server: a STEP_
// reference is refused when saved and when used, and the key is in no
// step environment, app environment, returned value or error.
func TestProvisioningKeyStaysOnTheServer(t *testing.T) {
	t.Parallel()
	s, id, f := llmFixture(t, "")
	attachLLM(t, s, id)
	ctx := context.Background()

	if _, err := s.Bindings.Save(ctx, "other", CapLLM, "openrouter", `{"provisioningKeyFrom":["STEP_OPENROUTER_PROVISIONING_KEY"],"maxLimit":5}`, "admin"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("a STEP_ reference is refused on save: %v", err)
	}
	if _, err := s.Bindings.ByName(ctx, "other"); err == nil {
		t.Fatal("the refused connector was recorded")
	}
	if _, err := s.Bindings.Save(ctx, "other", CapLLM, "openrouter", `{"provisioningKeyFrom":["OPENROUTER_PROVISIONING_KEY"],"maxLimit":5}`, "admin"); err != nil {
		t.Fatalf("a server-only reference is fine: %v", err)
	}
	for _, bad := range []string{`{"provisioningKeyFrom":["K"]}`, `{"provisioningKeyFrom":["K"],"maxLimit":5,"defaultLimit":6}`, `{"provisioningKeyFrom":["K"],"maxLimit":-1}`, `{"maxLimit":5}`} {
		if _, err := s.Bindings.Save(ctx, "bad", CapLLM, "openrouter", bad, "admin"); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	if h := s.TestLLMBinding(ctx, map[string]any{"url": f.Base, "provisioningKeyFrom": []any{"OPENROUTER_PROVISIONING_KEY"}, "maxLimit": 5.0}); !h.OK || strings.Contains(h.Detail, provisioningSentinel) {
		t.Fatalf("health: %+v", h)
	}

	// A configuration saved before the rule (written past the door) is
	// refused at use, before any call.
	if _, err := s.Bindings.Upsert(ctx, "openrouter", CapLLM, "openrouter", `{"url":"`+f.Base+`","provisioningKeyFrom":["STEP_OPENROUTER_PROVISIONING_KEY"],"maxLimit":10}`, false); err != nil {
		t.Fatal(err)
	}
	before := len(f.Calls())
	if _, err := s.RegisterLLMKey(ctx, KeyRequest{ProjectID: id, Name: "k", Scope: "llm:deployment:1"}, &heldKeys{}); !errors.Is(err, errLLMConfig) || len(f.Calls()) != before {
		t.Fatalf("a stored STEP_ reference is refused at use: %v, %d calls", err, len(f.Calls())-before)
	}
	if _, err := s.Bindings.Upsert(ctx, "openrouter", CapLLM, "openrouter", `{"url":"`+f.Base+`","provisioningKeyFrom":["OPENROUTER_PROVISIONING_KEY"],"maxLimit":10}`, false); err != nil {
		t.Fatal(err)
	}

	var seen []string
	f.FailNext("POST", 500)
	_, err := s.RegisterLLMKey(ctx, KeyRequest{ProjectID: id, Name: "k", Scope: "llm:deployment:1"}, &heldKeys{})
	if err == nil {
		t.Fatal("a refused create is an error")
	}
	seen = append(seen, err.Error())
	env, err := s.RegisterLLMKey(ctx, KeyRequest{ProjectID: id, Name: "k", Scope: "llm:deployment:1"}, &heldKeys{})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range env {
		seen = append(seen, v)
	}
	if h := s.TestLLMBinding(ctx, map[string]any{"url": f.Base, "provisioningKeyFrom": []any{"NOT_THERE"}, "maxLimit": 5.0}); !h.OK {
		seen = append(seen, h.Detail)
	}
	seen = append(seen, mustEnvironment(t, s, id)...)
	stepEnv, err := s.secrets.StepEnv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range stepEnv {
		if strings.HasPrefix(e, "OPENROUTER_PROVISIONING_KEY=") {
			continue // the deliberately misnamed STEP_ copy this fixture holds, which no connector may name
		}
		seen = append(seen, e)
	}
	for _, text := range seen {
		if strings.Contains(text, provisioningSentinel) {
			t.Fatalf("the provisioning key escaped: %q", text)
		}
	}
	for _, c := range f.Calls() {
		for k, v := range c.Body {
			if s, ok := v.(string); ok && strings.Contains(s, provisioningSentinel) {
				t.Fatalf("the provisioning key was sent in a body field %s", k)
			}
		}
	}
}

func strPtr(s string) *string { return &s }

// refusingRecorder cannot record a key, and its caller stops as it fails.
type refusingRecorder struct{ stop context.CancelFunc }

func (r refusingRecorder) Record(context.Context, HeldKey) error {
	r.stop()
	return errors.New("no record")
}
func (r refusingRecorder) Forget(context.Context, HeldKey) error { return nil }

// A key OpenRouter has made but the owner cannot record is revoked though
// the caller stops at that moment: nothing would ever revoke it later.
func TestAnUnrecordedAIModelsKeyIsRevokedThoughTheCallerStops(t *testing.T) {
	t.Parallel()
	s, id, f := llmFixture(t, "")
	attachLLM(t, s, id)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := s.RegisterLLMKey(ctx, KeyRequest{ProjectID: id, Name: "forge-demo-main-1", Scope: "llm:deployment:1"}, refusingRecorder{cancel}); err == nil {
		t.Fatal("an unrecorded key was handed out")
	}
	if f.CallsOf("POST") != 1 || len(f.Keys()) != 0 {
		t.Fatalf("the unrecorded key is left at OpenRouter: %d made, %d left", f.CallsOf("POST"), len(f.Keys()))
	}
}
