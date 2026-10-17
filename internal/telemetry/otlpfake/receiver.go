// Package otlpfake is an in-process OTLP/HTTP receiver for tests. It records
// what arrives and answers what exporters expect; it is not a collector.
package otlpfake

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	collogpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"
)

// Span is one received span, flattened for assertions.
type Span struct {
	Name       string
	TraceID    string
	SpanID     string
	ParentID   string
	Links      []string // trace ids of linked spans
	Attributes map[string]string
	Resource   map[string]string
}

// Metric is one received data point.
type Metric struct {
	Name       string
	Value      float64
	Attributes map[string]string
	Resource   map[string]string
}

// Log is one received log record.
type Log struct {
	Body       string
	Severity   string
	TraceID    string
	Attributes map[string]string
	Resource   map[string]string
}

// Receiver serves /v1/traces, /v1/metrics and /v1/logs over plain HTTP.
type Receiver struct {
	srv     *httptest.Server
	mu      sync.Mutex
	spans   []Span
	metrics []Metric
	logs    []Log
	headers []http.Header
	hang    chan struct{}
	stall   bool
}

// New starts a receiver on a loopback port.
func New() *Receiver {
	r := &Receiver{hang: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", r.traces)
	mux.HandleFunc("/v1/metrics", r.metrics_)
	mux.HandleFunc("/v1/logs", r.logs_)
	r.srv = httptest.NewServer(mux)
	return r
}

// Endpoint is host:port, what an exporter's endpoint option wants.
func (r *Receiver) Endpoint() string { return strings.TrimPrefix(r.srv.URL, "http://") }

// Close stops the server and releases any hung request.
func (r *Receiver) Close() {
	r.mu.Lock()
	select {
	case <-r.hang:
	default:
		close(r.hang)
	}
	r.mu.Unlock()
	r.srv.Close()
}

// NeverAnswer makes every export block until Close, for shutdown-bound tests.
func (r *Receiver) NeverAnswer() {
	r.mu.Lock()
	r.hang = make(chan struct{})
	r.stall = true
	r.mu.Unlock()
}

// Spans, Metrics and Logs return copies of what has arrived.
func (r *Receiver) Spans() []Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Span(nil), r.spans...)
}
func (r *Receiver) Metrics() []Metric {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Metric(nil), r.metrics...)
}
func (r *Receiver) Logs() []Log {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Log(nil), r.logs...)
}

// Headers returns the request headers of every export received.
func (r *Receiver) Headers() []http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]http.Header(nil), r.headers...)
}

// Contains reports whether needle appears anywhere in anything received:
// names, bodies, attribute keys or values, resource attributes. It is the
// canary check: a secret placed anywhere upstream must not make this true.
func (r *Receiver) Contains(needle string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	in := func(m map[string]string) bool {
		for k, v := range m {
			if strings.Contains(k, needle) || strings.Contains(v, needle) {
				return true
			}
		}
		return false
	}
	for _, s := range r.spans {
		if strings.Contains(s.Name, needle) || in(s.Attributes) || in(s.Resource) {
			return true
		}
	}
	for _, m := range r.metrics {
		if strings.Contains(m.Name, needle) || in(m.Attributes) || in(m.Resource) {
			return true
		}
	}
	for _, l := range r.logs {
		if strings.Contains(l.Body, needle) || in(l.Attributes) || in(l.Resource) {
			return true
		}
	}
	return false
}

func (r *Receiver) receive(w http.ResponseWriter, req *http.Request) ([]byte, bool) {
	r.mu.Lock()
	stall, hang := r.stall, r.hang
	r.headers = append(r.headers, req.Header.Clone())
	r.mu.Unlock()
	if stall {
		<-hang
		w.WriteHeader(http.StatusServiceUnavailable)
		return nil, false
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

func (r *Receiver) traces(w http.ResponseWriter, req *http.Request) {
	body, ok := r.receive(w, req)
	if !ok {
		return
	}
	var in coltracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &in); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	for _, rs := range in.ResourceSpans {
		res := attrs(rs.GetResource().GetAttributes())
		for _, ss := range rs.ScopeSpans {
			for _, s := range ss.Spans {
				sp := Span{Name: s.Name, TraceID: hexOf(s.TraceId), SpanID: hexOf(s.SpanId), ParentID: hexOf(s.ParentSpanId), Attributes: attrs(s.Attributes), Resource: res}
				for _, l := range s.Links {
					sp.Links = append(sp.Links, hexOf(l.TraceId))
				}
				r.spans = append(r.spans, sp)
			}
		}
	}
	r.mu.Unlock()
	respond(w, &coltracepb.ExportTraceServiceResponse{})
}

func (r *Receiver) metrics_(w http.ResponseWriter, req *http.Request) {
	body, ok := r.receive(w, req)
	if !ok {
		return
	}
	var in colmetricpb.ExportMetricsServiceRequest
	if err := proto.Unmarshal(body, &in); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	for _, rm := range in.ResourceMetrics {
		res := attrs(rm.GetResource().GetAttributes())
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				switch d := m.Data.(type) {
				case *metricpbSum:
					for _, p := range d.Sum.DataPoints {
						r.metrics = append(r.metrics, Metric{Name: m.Name, Value: number(p.Value), Attributes: attrs(p.Attributes), Resource: res})
					}
				case *metricpbGauge:
					for _, p := range d.Gauge.DataPoints {
						r.metrics = append(r.metrics, Metric{Name: m.Name, Value: number(p.Value), Attributes: attrs(p.Attributes), Resource: res})
					}
				case *metricpbHistogram:
					for _, p := range d.Histogram.DataPoints {
						r.metrics = append(r.metrics, Metric{Name: m.Name, Value: p.GetSum(), Attributes: attrs(p.Attributes), Resource: res})
					}
				}
			}
		}
	}
	r.mu.Unlock()
	respond(w, &colmetricpb.ExportMetricsServiceResponse{})
}

func (r *Receiver) logs_(w http.ResponseWriter, req *http.Request) {
	body, ok := r.receive(w, req)
	if !ok {
		return
	}
	var in collogpb.ExportLogsServiceRequest
	if err := proto.Unmarshal(body, &in); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	for _, rl := range in.ResourceLogs {
		res := attrs(rl.GetResource().GetAttributes())
		for _, sl := range rl.ScopeLogs {
			for _, l := range sl.LogRecords {
				r.logs = append(r.logs, Log{Body: value(l.Body), Severity: l.SeverityText, TraceID: hexOf(l.TraceId), Attributes: attrs(l.Attributes), Resource: res})
			}
		}
	}
	r.mu.Unlock()
	respond(w, &collogpb.ExportLogsServiceResponse{})
}

func respond(w http.ResponseWriter, msg proto.Message) {
	out, _ := proto.Marshal(msg)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

func attrs(kvs []*commonpb.KeyValue) map[string]string {
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		out[kv.Key] = value(kv.Value)
	}
	return out
}
