// Package inference owns runtime model admission, private provider access and accounting.
package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	nooptrace "go.opentelemetry.io/otel/trace/noop"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/clock"

	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/trust"
)

// Gateway owns transport and admission. Authentication, bindings and
// receipts are narrow dependencies; the proxy never has an App or a database.
type Gateway struct {
	networks     []netip.Prefix
	authenticate func(context.Context, string) (identity.Workload, error)
	providers    func(context.Context) (map[string]*inferenceProvider, error)
	begin        func(context.Context, identity.Workload, string, string) (ids.ID, error)
	finish       func(context.Context, ids.ID, int, string, inferenceUsage)
	// open and settle are an AppTown call's receipt and spend line (Ask).
	open        func(context.Context, identity.Workload, string, string, string, time.Duration) (ids.ID, error)
	settle      func(context.Context, ids.ID, int, string, inferenceUsage, string) (bool, error)
	instruments sync.Once
	links       func(runID ids.ID, stepID ids.ID, attempt int64) (trace.SpanContext, bool)
	tracer      trace.Tracer
	meter       metric.Meter
	requests    metric.Int64Counter
	tokens      metric.Int64Counter
	duration    metric.Float64Histogram
	client      *http.Client
	mu          sync.Mutex
	active      map[string]int
	slots       chan struct{}
	timeout     time.Duration
	clock       func() time.Time // AppTown's clock, which times a call; nil is the real one
	// models is the one model choice for a workload's project (an eval
	// copy runs only its arm's models); nil admits every enabled model.
	models func(ctx context.Context, projectID ids.ID, pattern string) error
	// judge is the judge's door: the model choice for an eval comparison's
	// rubric calls (Ask with Judge); nil admits none.
	judge func(ctx context.Context, projectID ids.ID, pattern string) error
}

// ParseConfig validates configuration. No implicit
// loopback/private-network trust and no forwarded-header interpretation.
func ParseConfig(runtimeURL string, cidrs []string) ([]netip.Prefix, error) {
	u, err := url.Parse(runtimeURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("inference URL must be an HTTP(S) origin")
	}
	var networks []netip.Prefix
	for _, raw := range cidrs {
		p, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || p.Bits() == 0 || p.Addr().Is4In6() {
			return nil, fmt.Errorf("inference requires explicit, valid worker CIDRs (no wildcard networks)")
		}
		networks = append(networks, p.Masked())
	}
	if len(networks) == 0 {
		return nil, fmt.Errorf("inference requires at least one allowed worker CIDR")
	}
	return networks, nil
}

// New binds current workload authority, private providers and durable accounting.
// The request transport never captures the application composition root.
func New(networks []netip.Prefix, identities Identity, catalog *Catalog, requests *usage.Requests) *Gateway {
	transport := trust.Transport(trust.Inference)
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 2 * time.Minute
	accounting := receipts{requests: requests}
	g := &Gateway{networks: append([]netip.Prefix(nil), networks...),
		authenticate: identities.Authenticate, providers: catalog.providers, begin: accounting.begin, finish: accounting.finish, open: accounting.open, settle: accounting.settle,
		client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		active: map[string]int{}, slots: make(chan struct{}, 64), timeout: 20 * time.Minute}
	g.Instrument(nooptrace.NewTracerProvider().Tracer("forge/inference"), noopmetric.NewMeterProvider().Meter("forge/inference"))
	return g
}

func (g *Gateway) allowedPeer(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	for _, p := range g.networks {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func inferenceToken(r *http.Request) string {
	// Pi's native SDKs use these three authentication headers. Ambiguous or
	// duplicate credentials are rejected. Query-string keys are never accepted.
	var token string
	for _, name := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key"} {
		vals := r.Header.Values(name)
		if len(vals) > 1 {
			return ""
		}
		if len(vals) == 0 {
			continue
		}
		v := vals[0]
		if name == "Authorization" {
			if !strings.HasPrefix(v, "Bearer ") {
				return ""
			}
			v = strings.TrimPrefix(v, "Bearer ")
		}
		if v == "" || (token != "" && token != v) {
			return ""
		}
		token = v
	}
	return token
}

func (g *Gateway) acquire(token string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active[token] >= 8 {
		return false
	}
	select {
	case g.slots <- struct{}{}:
		g.active[token]++
		return true
	default:
		return false
	}
}
func (g *Gateway) release(token string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active[token]--
	if g.active[token] == 0 {
		delete(g.active, token)
	}
	<-g.slots
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !g.allowedPeer(r.RemoteAddr) {
		http.Error(w, "inference is restricted to configured worker networks", http.StatusForbidden)
		return
	}
	g.admitted(w, r)
}

// admitted is a request from an allowed peer, or from AppTown itself
// (Complete): everything after the network check.
func (g *Gateway) admitted(w http.ResponseWriter, r *http.Request) {
	g.instruments.Do(func() {
		if g.tracer == nil {
			g.Instrument(nooptrace.NewTracerProvider().Tracer("forge/inference"), noopmetric.NewMeterProvider().Meter("forge/inference"))
		}
	})
	fail := func(status int, msg string) { http.Error(w, msg, status) }
	if r.Method != "POST" {
		fail(405, "inference requires POST")
		return
	}
	token := inferenceToken(r)
	_, err := g.authenticate(r.Context(), token)
	if err != nil {
		fail(401, "a live AppTown runtime credential is required")
		return
	}
	if !g.acquire(token) {
		fail(429, "too many inference requests; retry after an active request finishes")
		return
	}
	defer g.release(token)
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/providers/"), "/", 2)
	if !strings.HasPrefix(r.URL.Path, "/providers/") || len(parts) != 2 || !inferenceProviderName.MatchString(parts[0]) {
		fail(404, "unknown inference route")
		return
	}
	ps, err := g.providers(r.Context())
	if err != nil {
		fail(503, "model provider configuration is unavailable; ask AppTown administrator")
		return
	}
	p := ps[parts[0]]
	if p == nil {
		fail(404, "unknown inference provider")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
	if err != nil {
		fail(413, "inference request is too large or unreadable")
		return
	}
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil || request == nil {
		fail(400, "inference body must be a JSON object")
		return
	}
	if _, ok := request["models"]; ok {
		fail(400, "fallback model lists are not supported; select one enabled model")
		return
	}
	var background bool
	if json.Unmarshal(request["background"], &background) == nil && background {
		fail(400, "background inference is not supported")
		return
	}
	// Canonicalize top-level keys so differing duplicate-key parsers cannot
	// dispatch a different model from the one admitted here.
	body, err = json.Marshal(request)
	if err != nil {
		fail(400, "invalid inference body")
		return
	}
	model, _ := jsonString(request["model"])
	path, model, err := p.inferencePath(parts[1], r.URL.Query(), model)
	if err != nil {
		fail(400, "unsupported inference operation")
		return
	}
	if !p.allows(model) {
		// Not allowed for want of a readable list is unknown, not refused.
		if p.discoveryErr != nil {
			fail(502, "the provider's model list could not be read, and this model is not declared in its AppTown binding")
			return
		}
		fail(403, "model is not enabled in this AppTown provider binding")
		return
	}
	// The route, model and charge attribution cannot be chosen by an agent's
	// claimed actor/run headers. Only the authenticated lifecycle grants them.
	// Body upload/discovery can take time. A credential revoked while those
	// were in progress must not get a fresh upstream admission afterwards.
	who, err := g.authenticate(r.Context(), token)
	if err != nil {
		fail(401, "runtime credential was revoked before inference admission")
		return
	}
	if g.models != nil {
		if err := g.models(r.Context(), who.ProjectID, p.name+"/"+model); err != nil {
			fail(403, err.Error())
			return
		}
	}
	id, err := g.begin(r.Context(), who, p.name, model)
	if err != nil {
		fail(503, "cannot record inference admission")
		return
	}
	status, outcome := 0, "upstream_error"
	usage := newInferenceUsageReader()
	started := g.now()
	spanOpts := []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindServer)}
	if g.links != nil && !who.RunID.IsZero() {
		if sc, ok := g.links(who.RunID, who.StepID, who.Attempt); ok {
			spanOpts = append(spanOpts, trace.WithLinks(trace.Link{SpanContext: sc}))
		}
	}
	spanCtx, span := g.tracer.Start(r.Context(), "inference.request", append(spanOpts, trace.WithAttributes(
		attribute.String("apptown.inference.provider", p.name), attribute.String("apptown.inference.model", model), attribute.String("apptown.inference.api", p.api),
		attribute.String("apptown.workload.kind", who.Kind), attribute.String("apptown.project", who.ProjectID.String()), attribute.String("apptown.run", who.RunID.String()), attribute.String("apptown.step", who.StepID.String())))...)
	r = r.WithContext(spanCtx)
	defer func() {
		u := usage.result()
		// The receipt records a call already made: written though the request ended.
		g.finish(context.WithoutCancel(spanCtx), id, status, outcome, u)
		g.observe(spanCtx, span, p.name, model, status, outcome, u, g.now().Sub(started))
	}()
	ctx, cancel := context.WithTimeout(r.Context(), g.timeout)
	defer cancel()
	// A terminated session/account also stops an outstanding stream. Each
	// request owns one bounded watcher, stopped by the request context.
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := g.authenticate(r.Context(), token); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	upstream, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		fail(503, "invalid inference provider configuration")
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Accept", r.Header.Get("Accept"))
	// SDK protocol negotiation only. No cookies, caller identity, supplied
	// upstream keys, routing headers, or forwarded headers cross this boundary.
	for _, h := range []string{"Anthropic-Version", "Anthropic-Beta"} {
		if v := r.Header.Get(h); v != "" {
			upstream.Header.Set(h, v)
		}
	}
	p.authorize(upstream.Header)
	resp, err := g.client.Do(upstream)
	if err != nil {
		if ctx.Err() != nil {
			outcome = "cancelled"
		}
		fail(502, "inference provider request failed")
		return
	}
	defer resp.Body.Close()
	status = resp.StatusCode
	if status < 200 || status >= 300 {
		// Upstream errors may echo headers/URLs including provider credentials.
		// Keep the useful status, but never reflect their body or Location.
		outcome = "rejected"
		code := status
		if status < 400 {
			code = 502
		}
		fail(code, fmt.Sprintf("inference provider rejected the request (HTTP %d)", status))
		return
	}
	usage.sse = strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(status)
	reader := io.TeeReader(resp.Body, usage)
	buf := make([]byte, 32<<10)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			if _, err = w.Write(buf[:n]); err != nil {
				outcome = "cancelled"
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				outcome = "complete"
			} else if ctx.Err() != nil {
				outcome = "cancelled"
			} else {
				outcome = "interrupted"
			}
			return
		}
	}
}

// authorize sets the binding's own upstream headers and its credential, in
// the header its dialect expects. Only the gateway's upstream requests carry
// them.
func (p *inferenceProvider) authorize(h http.Header) {
	for k, v := range p.headers {
		h.Set(k, v)
	}
	if p.key != "" {
		switch p.api {
		case "anthropic-messages":
			h.Set("X-Api-Key", p.key)
		case "google-generative-ai":
			h.Set("X-Goog-Api-Key", p.key)
		default:
			h.Set("Authorization", "Bearer "+p.key)
		}
	}
}

func jsonString(b []byte) (string, error) { var s string; err := json.Unmarshal(b, &s); return s, err }

func (p *inferenceProvider) inferencePath(path string, q url.Values, model string) (string, string, error) {
	bad := func() (string, string, error) { return "", "", fmt.Errorf("unsupported operation") }
	// Query parameters never carry credentials or alternate upstream URLs.
	for k, v := range q {
		if k != "alt" || len(v) != 1 || v[0] != "sse" || p.api != "google-generative-ai" {
			return bad()
		}
	}
	switch p.api {
	case "openai-completions":
		if path == "chat/completions" {
			return "/chat/completions", model, nil
		}
	case "openai-responses":
		if path == "responses" {
			return "/responses", model, nil
		}
	case "anthropic-messages":
		if path == "v1/messages" || path == "messages" {
			if strings.HasSuffix(p.baseURL, "/v1") {
				return "/messages", model, nil
			}
			return "/v1/messages", model, nil
		}
	case "google-generative-ai":
		path = strings.TrimPrefix(path, "v1beta/")
		if !strings.HasPrefix(path, "models/") || strings.ContainsAny(path, "?#%\\") || strings.Contains(path, "..") {
			return bad()
		}
		for _, op := range []string{":generateContent", ":streamGenerateContent"} {
			if strings.HasSuffix(path, op) {
				m := strings.TrimSuffix(strings.TrimPrefix(path, "models/"), op)
				if m == "" || strings.Contains(m, "/") {
					return bad()
				}
				prefix := "/v1beta/"
				if strings.HasSuffix(p.baseURL, "/v1beta") {
					prefix = "/"
				}
				suffix := ""
				if len(q) > 0 {
					suffix = "?alt=sse"
				}
				return prefix + path + suffix, m, nil
			}
		}
	}
	return bad()
}

// Instrument gives the gateway a tracer and a meter. New installs no-op ones,
// so an uninstrumented gateway costs nothing and branches nowhere.
// Clocked times the gateway's calls by now, AppTown's clock.
func (g *Gateway) Clocked(now func() time.Time) *Gateway {
	g.clock = now
	return g
}

func (g *Gateway) now() time.Time { return clock.Or(g.clock)() }

func (g *Gateway) Instrument(tracer trace.Tracer, meter metric.Meter) *Gateway {
	g.tracer, g.meter = tracer, meter
	g.requests, _ = meter.Int64Counter("apptown_inference_requests_total", metric.WithDescription("inference requests admitted by the gateway, by provider, model and outcome"))
	g.tokens, _ = meter.Int64Counter("apptown_inference_tokens_total", metric.WithDescription("tokens reported by upstream providers, by provider, model and direction"))
	g.duration, _ = meter.Float64Histogram("apptown_inference_request_duration", metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(250, 500, 1000, 2000, 5000, 10000, 20000, 30000, 60000, 120000, 300000),
		metric.WithDescription("gateway request duration from admission to completion"))
	return g
}

// observe closes the request's span and records its metrics. Never the
// prompt, the completion or a credential: provider, model, outcome and counts.
func (g *Gateway) observe(ctx context.Context, span trace.Span, provider, model string, status int, outcome string, u inferenceUsage, took time.Duration) {
	span.SetAttributes(attribute.Int("http.response.status_code", status), attribute.String("apptown.inference.outcome", outcome),
		attribute.Int64("apptown.inference.tokens.input", u.Input), attribute.Int64("apptown.inference.tokens.output", u.Output),
		attribute.Int64("apptown.inference.tokens.cache_read", u.CacheRead), attribute.Int64("apptown.inference.tokens.cache_write", u.CacheWrite))
	if status >= 500 || (status == 0 && outcome != "ok") {
		span.SetStatus(codes.Error, outcome)
	}
	span.End()
	labels := []attribute.KeyValue{attribute.String("provider", provider), attribute.String("model", model)}
	g.requests.Add(ctx, 1, metric.WithAttributes(append(labels, attribute.String("outcome", outcome))...))
	g.duration.Record(ctx, float64(took.Milliseconds()), metric.WithAttributes(labels...))
	for dir, n := range map[string]int64{"input": u.Input, "output": u.Output, "cache_read": u.CacheRead, "cache_write": u.CacheWrite} {
		if n > 0 {
			g.tokens.Add(ctx, n, metric.WithAttributes(append(labels, attribute.String("direction", dir))...))
		}
	}
}

// ChooseModels gives the gateway the one model choice for a workload's
// project, asked before anything is sent: an eval copy's runs may use only
// the models their arm runs.
func (g *Gateway) ChooseModels(choose func(ctx context.Context, projectID ids.ID, pattern string) error) *Gateway {
	g.models = choose
	return g
}

// ChooseJudge gives the gateway the judge's door: the one model choice for an
// eval comparison's rubric calls (modelcall.Call.Judge), asked instead of the
// arm's own choice.
func (g *Gateway) ChooseJudge(choose func(ctx context.Context, projectID ids.ID, pattern string) error) *Gateway {
	g.judge = choose
	return g
}

// LinkAttempts lets each inference span link to the step attempt that made
// the request, when the lookup knows it.
func (g *Gateway) LinkAttempts(lookup func(runID ids.ID, stepID ids.ID, attempt int64) (trace.SpanContext, bool)) *Gateway {
	g.links = lookup
	return g
}
