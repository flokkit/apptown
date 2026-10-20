// Package openrouterfake is an in-memory OpenRouter key management API for
// tests: create, list, read, change and delete keys under /api/v1/keys,
// authenticated by one provisioning key. It records every call it was
// asked, so a test can say "no call at all", and can be told to refuse
// the next call of an operation.
package openrouterfake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Call is one request the fake answered (or refused).
type Call struct {
	Method string
	Path   string
	Body   map[string]any
}

// Key is the fake's record of one key.
type Key struct {
	Hash, Name, Value string
	Limit             *float64
	Reset             *string
	Usage, Period     float64
}

// Server is the fake. Base is the API root a client is given.
type Server struct {
	*httptest.Server
	Base            string
	ProvisioningKey string

	mu    sync.Mutex
	calls []Call
	keys  map[string]*Key
	n     int
	fail  map[string]int // "POST" | "PATCH" | "DELETE" | "GET" → status for the next such call
	// Before, when set, runs as each request arrives, before it is
	// answered: a test stops its caller there.
	Before func(method string)
}

// New starts a fake accepting provisioningKey.
func New(provisioningKey string) *Server {
	s := &Server{ProvisioningKey: provisioningKey, keys: map[string]*Key{}, fail: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	s.Base = s.Server.URL + "/api/v1"
	return s
}

// FailNext makes the next request with method answer status.
func (s *Server) FailNext(method string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[method] = status
}

// Calls is every call so far.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// CallsOf counts the calls with method.
func (s *Server) CallsOf(method string) int {
	n := 0
	for _, c := range s.Calls() {
		if c.Method == method {
			n++
		}
	}
	return n
}

// Keys is the keys that exist now, by hash.
func (s *Server) Keys() map[string]Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]Key{}
	for h, k := range s.keys {
		out[h] = *k
	}
	return out
}

// SetUsage sets a key's lifetime and current-period spend.
func (s *Server) SetUsage(hash string, lifetime, period float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k := s.keys[hash]; k != nil {
		k.Usage, k.Period = lifetime, period
	}
}

// Forget removes a key behind AppTown's back (someone revoked it by hand).
func (s *Server) Forget(hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.keys, hash)
}

func refuse(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": status, "message": msg}})
}

func (k *Key) view() map[string]any {
	out := map[string]any{"hash": k.Hash, "name": k.Name, "label": "sk-or-v1-…" + k.Value[len(k.Value)-3:], "disabled": false,
		"limit": k.Limit, "limit_reset": k.Reset, "usage": k.Usage, "usage_daily": 0.0, "usage_weekly": 0.0, "usage_monthly": 0.0}
	if k.Limit != nil {
		out["limit_remaining"] = *k.Limit - k.Period
	} else {
		out["limit_remaining"] = nil
	}
	if k.Reset != nil {
		out["usage_"+*k.Reset] = k.Period
	}
	return out
}

func (s *Server) budget(k *Key, body map[string]any) {
	if v, ok := body["limit"]; ok {
		if f, ok := v.(float64); ok {
			k.Limit = &f
		} else {
			k.Limit = nil
		}
	}
	if v, ok := body["limit_reset"]; ok {
		if r, ok := v.(string); ok {
			k.Reset = &r
		} else {
			k.Reset = nil
		}
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if s.Before != nil {
		s.Before(r.Method)
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, Call{Method: r.Method, Path: r.URL.Path, Body: body})
	if r.Header.Get("Authorization") != "Bearer "+s.ProvisioningKey {
		refuse(w, 401, "No auth credentials found")
		return
	}
	if status := s.fail[r.Method]; status != 0 {
		delete(s.fail, r.Method)
		refuse(w, status, "refused by the fake")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	w.Header().Set("Content-Type", "application/json")
	switch {
	case path == "/keys" && r.Method == http.MethodGet:
		var data []map[string]any
		for _, k := range s.keys {
			data = append(data, k.view())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	case path == "/keys" && r.Method == http.MethodPost:
		name, _ := body["name"].(string)
		if name == "" {
			refuse(w, 400, "name is required")
			return
		}
		s.n++
		k := &Key{Hash: fmt.Sprintf("hash%04d", s.n), Name: name, Value: fmt.Sprintf("sk-or-v1-fake%04d", s.n)}
		s.budget(k, body)
		s.keys[k.Hash] = k
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": k.view(), "key": k.Value})
	case strings.HasPrefix(path, "/keys/"):
		k := s.keys[strings.TrimPrefix(path, "/keys/")]
		if k == nil {
			refuse(w, 404, "Key not found")
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": k.view()})
		case http.MethodPatch:
			s.budget(k, body)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": k.view()})
		case http.MethodDelete:
			delete(s.keys, k.Hash)
			_ = json.NewEncoder(w).Encode(map[string]any{"deleted": true})
		default:
			refuse(w, 405, "method not allowed")
		}
	default:
		refuse(w, 404, "not found")
	}
}
