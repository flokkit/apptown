package inference

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/trust"
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
	key [32]byte
	at  time.Time
	ids []string
	err error // the provider's answer could not be had; ids is then empty
}

// Cache only public model IDs, indexed by binding. Credentials never become
// runtime metadata; the digest only invalidates discovery after key rotation.
// The provider is asked under its binding's own lock only. A failed request
// is cached as a failure for the same minute: its ids are never the list an
// earlier request saw, so an unknown list is never shown as current.
func (d *inferenceDiscovery) models(p *inferenceProvider, now time.Time) ([]string, error) {
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
		return old.ids, old.err
	}
	entry := inferenceDiscoveryEntry{key: key, at: now}
	entry.ids, entry.err = fetchModels(p)
	if entry.err != nil {
		entry.ids = nil
	}
	slot.entry = entry
	return entry.ids, entry.err
}

// fetchModels lists the model IDs an OpenAI-style provider offers at
// /models, with the binding's own headers and credential, no proxy and no
// redirects.
func fetchModels(p *inferenceProvider) ([]string, error) {
	req, err := http.NewRequest("GET", p.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	p.authorize(req.Header)
	transport := trust.Transport(trust.Inference)
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the %s provider could not be asked for its models", p.name)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("the %s provider answered HTTP %d when asked for its models", p.name, resp.StatusCode)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&list); err != nil {
		return nil, fmt.Errorf("the %s provider's model list could not be read", p.name)
	}
	var ids []string
	for _, m := range list.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// discoverable is a binding whose provider is asked for its models: one
// with its own address and an OpenAI-style dialect. A built-in provider at
// its fixed address lists only what the binding declares.
func discoverable(p *inferenceProvider, cfg integration.ModelConfig) bool {
	return (cfg.BaseURL != "" || cfg.ProviderDef["baseUrl"] != nil) && strings.HasPrefix(p.api, "openai-")
}

// discoverModels adds what the provider lists live to what the binding
// declares. A list that cannot be read adds nothing and is kept on the
// provider (discoveryErr), so a refusal of an undeclared model says why.
func (catalog Catalog) discoverModels(p *inferenceProvider, cfg integration.ModelConfig) {
	if !discoverable(p, cfg) {
		return
	}
	ids, err := catalog.discovery.models(p, clock.Or(catalog.Now)())
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
