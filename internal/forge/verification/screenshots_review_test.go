//go:build integration

package verification

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestIntegrationScreenshotsReviewRedirectRoundTrip(t *testing.T) {
	var base string
	other := listen(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, base+"/returned", http.StatusFound)
	}), nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/", page("<h1>Home</h1>"))
	mux.HandleFunc("/leave", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, fmt.Sprintf("http://host.docker.internal:%d/", other), http.StatusFound)
	})
	base = fmt.Sprintf("http://host.docker.internal:%d", listen(t, mux, nil))
	c := localShots(nil).Take(context.Background(), base, []string{"/leave"})
	for _, s := range c.Shots {
		if s.Path == "/leave" {
			t.Fatalf("captured a main-frame redirect through another origin: %+v", c.Missing)
		}
	}
}

func TestIntegrationScreenshotsReviewOnePageDeadline(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", page("<h1>Home</h1>"))
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		if !wait(r, 8*time.Second) {
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		if !wait(r, 9*time.Second) {
			return
		}
		page("<h1>Late</h1>")(w, r)
	})
	base := fmt.Sprintf("http://host.docker.internal:%d", listen(t, mux, nil))
	c := localShots(nil).Take(context.Background(), base, []string{"/slow"})
	for _, s := range c.Shots {
		if s.Path == "/slow" {
			t.Fatal("captured a page that took more than its 10 seconds")
		}
	}
}

// wait holds a slow page's answer for d, or until its request ends.
func wait(r *http.Request, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-r.Context().Done():
		return false
	}
}
