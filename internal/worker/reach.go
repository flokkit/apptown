package worker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Before a chat opens on the worker, AppTown asks whether the worker can
// reach the registries the chat's project is bound to: a chat that cannot
// is refused when it opens, naming the registry, rather than failing in the
// middle of an install.

// ReachRequest names the addresses to try.
type ReachRequest struct {
	URLs []string `json:"urls"`
}

// Reach is one address's answer: any HTTP response counts as reachable
// (a registry may answer 401 or 404 at its root); no response does not.
type Reach struct {
	URL       string `json:"url"`
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
}

const maxReach = 32

func (s *Server) reach(w http.ResponseWriter, r *http.Request) {
	var req ReachRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || len(req.URLs) > maxReach {
		http.Error(w, "a list of at most 32 addresses", http.StatusBadRequest)
		return
	}
	out := make([]Reach, len(req.URLs))
	var wg sync.WaitGroup
	for i, raw := range req.URLs {
		out[i].URL = raw
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			out[i].Error = "not an http or https address"
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i].Reachable, out[i].Error = try(r.Context(), raw)
		}()
	}
	wg.Wait()
	writeJSON(w, out)
}

func try(ctx context.Context, raw string) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return false, err.Error()
	}
	// Redirects are not followed: an answer from the address is enough.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return false, err.Error()
	}
	resp.Body.Close()
	return true, ""
}
