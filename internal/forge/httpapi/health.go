package httpapi

import (
	"context"
	"io"
	"net/http"
	"time"
)

// Health answers GET /healthz for a load balancer or a service manager: up
// when AppTown serves and its database answers, down otherwise. It says
// nothing else (no version, no counts) and asks no identity.
// ping is the database's answer, the one fact health reports.
type Health struct{ ping func(context.Context) error }

func NewHealth(ping func(context.Context) error) Health { return Health{ping: ping} }

func (h Health) Healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := h.ping(ctx); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "database unreachable\n")
		return
	}
	_, _ = io.WriteString(w, "ok\n")
}
