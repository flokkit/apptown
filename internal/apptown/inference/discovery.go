package inference

import (
	"context"
	"crypto/sha256"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/clock"
)

type inferenceDiscovery struct {
	mu      sync.Mutex
	entries map[string]*inferenceDiscoverySlot
}

// inferenceDiscoverySlot is one binding's cached answer. Its own lock is
// held while that binding's provider is asked, so concurrent readers of one
// binding make one request and other bindings never wait on it.
type inferenceDiscoverySlot struct {
	mu    sync.Mutex
	entry inferenceDiscoveryEntry
}
type inferenceDiscoveryEntry struct {
	key  [32]byte
	at   time.Time
	list []listedModel
	err  error // the provider's answer could not be had; list is then empty
}

// Cache only public model IDs and capability fields, indexed by binding. Credentials never become
// runtime metadata; the digest only invalidates discovery after key rotation.
// The provider is asked under its binding's own lock only. A failed request
// is cached as a failure for the same minute: its ids are never the list an
// earlier request saw, so an unknown list is never shown as current.
func (d *inferenceDiscovery) listed(ctx context.Context, p *inferenceProvider, now time.Time) ([]listedModel, error) {
	key := discoveryKey(p)
	d.mu.Lock()
	if d.entries == nil {
		d.entries = map[string]*inferenceDiscoverySlot{}
	}
	slot := d.entries[p.name]
	if slot == nil {
		slot = &inferenceDiscoverySlot{}
		d.entries[p.name] = slot
	}
	d.mu.Unlock()
	slot.mu.Lock()
	defer slot.mu.Unlock()
	old := slot.entry
	if old.key == key && now.Sub(old.at) < time.Minute {
		return old.list, old.err
	}
	entry := inferenceDiscoveryEntry{key: key, at: now}
	// The answer is cached for every caller this minute: one caller
	// stopping must not leave a failure the others read.
	entry.list, entry.err = listModels(context.WithoutCancel(ctx), "the "+p.name+" provider", p.baseURL+"/models", p.api, p.authorize)
	if entry.err != nil {
		entry.list = nil
	}
	slot.entry = entry
	return entry.list, entry.err
}

// discoverable is a binding whose provider is asked for its models: one
// with an OpenAI-style dialect and its own address. Built-in providers
// offer and admit only their enabled models.
func discoverable(p *inferenceProvider, cfg integration.ModelConfig) bool {
	return !cfg.BuiltIn(p.name) && (cfg.BaseURL != "" || cfg.ProviderDef["baseUrl"] != nil) && strings.HasPrefix(p.api, "openai-")
}

// discoverModels adds what the provider lists live to what the binding
// declares. A list that cannot be read adds nothing and is kept on the
// provider (discoveryErr), so a refusal of an undeclared model says why.
func (catalog Catalog) discoverModels(ctx context.Context, p *inferenceProvider, cfg integration.ModelConfig) {
	if !discoverable(p, cfg) {
		return
	}
	ids, err := catalog.discovery.models(ctx, p, clock.Or(catalog.Now)())
	p.discoveryErr = err
	for _, id := range ids {
		if len(cfg.Models) == 0 {
			p.patterns = append(p.patterns, id)
		}
		if p.allows(id) && p.models[id] == nil {
			p.models[id] = map[string]any{"id": id, "contextWindow": 131072, "maxTokens": 16384, "cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}
		}
	}
}

// discoveryKey digests what decides which models a binding sees: its
// address, its key and its headers in a fixed order.
func discoveryKey(p *inferenceProvider) [32]byte {
	h := sha256.New()
	h.Write([]byte(p.baseURL + "\x00" + p.key + "\x00"))
	for _, k := range slices.Sorted(maps.Keys(p.headers)) {
		h.Write([]byte(k + "\x00" + p.headers[k] + "\x00"))
	}
	var key [32]byte
	h.Sum(key[:0])
	return key
}

func (d *inferenceDiscovery) models(ctx context.Context, p *inferenceProvider, now time.Time) ([]string, error) {
	list, err := d.listed(ctx, p, now)
	var ids []string
	for _, m := range list {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, err
}
