package apptown

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/strs"
)

// AppTown reports its own errors the way it asks every app to: through
// the error-reporting connector, to the GlitchTip project of the "apptown"
// project on this instance. Attach AppTown project under Settings ›
// Services (or POST /projects/apptown/integrations {"capability":"errors"})
// and apptownd starts sending its panics, errored steps, cut-short reviews
// and failed deployment rebuilds there, tagged by run and step. Nothing
// is sent while AppTown project is unattached; nothing is sent
// synchronously; a burst is capped so a crash loop cannot flood the
// tracker.

const (
	selfErrorsProject = "apptown"
	selfErrorsBurst   = 30 // events per minute, then dropped with one log line
)

// sentryDSN is a parsed DSN: scheme://key@host/project.
type sentryDSN struct {
	Scheme, Host, Key, Project string
}

func parseSentryDSN(dsn string) (sentryDSN, bool) {
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil || u.User == nil || u.Host == "" {
		return sentryDSN{}, false
	}
	project := strings.Trim(u.Path, "/")
	if project == "" || u.User.Username() == "" {
		return sentryDSN{}, false
	}
	return sentryDSN{Scheme: u.Scheme, Host: u.Host, Key: u.User.Username(), Project: project}, true
}

func (d sentryDSN) storeURL() string {
	return d.Scheme + "://" + d.Host + "/api/" + d.Project + "/store/"
}

// selfErrorEvent is the Sentry event shape AppTown sends: enough for
// grouping (exception type + value), the where, and the ids to chase.
type selfErrorEvent struct {
	EventID    string            `json:"event_id"`
	Timestamp  string            `json:"timestamp"`
	Platform   string            `json:"platform"`
	Level      string            `json:"level"`
	Logger     string            `json:"logger"`
	ServerName string            `json:"server_name"`
	Release    string            `json:"release,omitempty"`
	Message    map[string]string `json:"message"`
	Tags       map[string]string `json:"tags,omitempty"`
	Extra      map[string]string `json:"extra,omitempty"`
	Exception  map[string]any    `json:"exception,omitempty"`
}

// selfErrors is the sender: the DSN (re-read when the attachment changes),
// the burst counter, and the HTTP client.
type selfErrors struct {
	mu       sync.Mutex
	dsn      sentryDSN
	haveDSN  bool
	checked  time.Time
	minute   time.Time
	sent     int
	dropped  int
	client   *http.Client
	storeURL func(sentryDSN) string
}

// selfDSN finds AppTown project's own error-reporting attachment, cached
// for a minute so a hot path never queries per event.
func (e selfErrorReporter) dsn(ctx context.Context) (sentryDSN, bool) {
	s := e.state
	s.mu.Lock()
	defer s.mu.Unlock()
	now := clock.Or(e.now)()
	if now.Sub(s.checked) < time.Minute {
		return s.dsn, s.haveDSN
	}
	s.checked = now
	s.haveDSN = false
	p, err := e.projects.BySlug(ctx, selfErrorsProject)
	if !readOK(err) {
		return s.dsn, false
	}
	_, res, err := e.attachments.Attached(ctx, p.ID, integration.CapErrors)
	if !readOK(err) || res == "" {
		return s.dsn, false
	}
	var r struct {
		DSN string `json:"dsn"`
	}
	if json.Unmarshal([]byte(res), &r) != nil {
		return s.dsn, false
	}
	if d, ok := parseSentryDSN(r.DSN); ok {
		s.dsn, s.haveDSN = d, true
	}
	return s.dsn, s.haveDSN
}

// reportError sends one event about AppTown's own failure: where it
// happened (the grouping key, with the kind), what it said, and the ids
// worth chasing. Never blocks the caller; never sends without the
// attachment; never more than a burst a minute.
func (e selfErrorReporter) Report(kind, where, message string, tags map[string]string, stack string) {
	dsn, ok := e.dsn(e.life)
	if !ok {
		return
	}
	s := e.state
	now := clock.Or(e.now)()
	s.mu.Lock()
	if now.Sub(s.minute) > time.Minute {
		if s.dropped > 0 {
			log.Printf("self errors: dropped %d event(s) in the last minute (burst cap %d)", s.dropped, selfErrorsBurst)
		}
		s.minute, s.sent, s.dropped = now, 0, 0
	}
	if s.sent >= selfErrorsBurst {
		s.dropped++
		s.mu.Unlock()
		return
	}
	s.sent++
	s.mu.Unlock()

	id := make([]byte, 16)
	_, _ = rand.Read(id)
	host, _ := os.Hostname()
	ev := selfErrorEvent{
		EventID: hex.EncodeToString(id), Timestamp: now.UTC().Format(time.RFC3339),
		Platform: "go", Level: "error", Logger: "apptownd", ServerName: host, Release: apptownRelease(),
		Message: map[string]string{"formatted": where + ": " + message},
		Tags:    map[string]string{"where": where, "kind": kind},
		Exception: map[string]any{"values": []map[string]any{{
			"type": kind, "value": strs.Truncate(message, 1000), "module": where,
		}}},
	}
	for k, v := range tags {
		ev.Tags[k] = v
	}
	if stack != "" {
		ev.Extra = map[string]string{"stack": strs.Truncate(stack, 8000)}
	}
	go e.send(dsn, ev)
}

func (e selfErrorReporter) send(dsn sentryDSN, ev selfErrorEvent) {
	defer func() { _ = recover() }() // the reporter must never take AppTown down
	body, _ := json.Marshal(ev)
	target := dsn.storeURL()
	if e.state.storeURL != nil {
		target = e.state.storeURL(dsn)
	}
	req, err := http.NewRequest("POST", target, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Sentry-Auth", fmt.Sprintf("Sentry sentry_version=7, sentry_client=apptownd/1, sentry_key=%s", dsn.Key))
	client := e.state.client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// apptownRelease is what AppTown reports as its release: the build's
// commit when the binary carries one, else "dev".
func apptownRelease() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 8 {
				return s.Value[:8]
			}
		}
	}
	return "dev"
}

// InstallPanicReporter binds process-wide panic reporting explicitly at daemon
// startup, before any background work. New never changes this global hook.
func (a *App) InstallPanicReporter() {
	reporter := selfErrorsFor(a)
	selfPanicReporter = func(where string, r any, stack []byte) {
		reporter.Report("panic", where, fmt.Sprint(r), nil, string(stack))
	}
}

// selfPanicReporter lets logPanic report without holding an App; nil until the
// daemon explicitly installs its reporting destination.
var selfPanicReporter func(where string, r any, stack []byte)

type selfErrorReporter struct {
	state       *selfErrors
	projects    *project.Projects
	attachments *integration.Attachments
	now         func() time.Time // AppTown's clock: the DSN's re-read, the burst's minute, an event's time
	// life is the App's lifetime: a report comes from any goroutine with no
	// caller to stop it, so its attachment read runs until shutdown.
	life context.Context
}

func selfErrorsFor(a *App) selfErrorReporter {
	return selfErrorReporter{state: &a.selfErr, projects: a.Projects, attachments: a.Integrations.Attachments, now: a.cfg.clock.Now, life: a.life}
}
func (e selfErrorReporter) BrowserConfig(ctx context.Context) (string, string, bool) {
	dsn, ok := e.dsn(ctx)
	if !ok {
		return "", "", false
	}
	value, _, valid := dsn.browserDestination()
	return value, apptownRelease(), valid
}

// Workflow reports a step verdict through this instance's diagnostic attachment.
func (e selfErrorReporter) Workflow(err error) {
	var verdict *workflow.StepError
	if errors.As(err, &verdict) {
		e.Report("step-error", verdict.Kind+"/"+verdict.Name, verdict.Reason, map[string]string{"run": fmt.Sprint(verdict.RunID), "step": fmt.Sprint(verdict.StepID)}, "")
	} else {
		reportWorkflowDispatch(err)
	}
}

// browserDestination validates the browser-facing DSN separately from the server
// sender. Only a concrete HTTP(S) origin may enter a CSP source list.
func (d sentryDSN) browserDestination() (string, string, bool) {
	if d.Scheme != "http" && d.Scheme != "https" {
		return "", "", false
	}
	u, err := url.Parse(d.Scheme + "://" + d.Host)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", "", false
	}
	host := u.Hostname()
	if net.ParseIP(host) == nil {
		if host == "" || strings.IndexFunc(host, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-')
		}) >= 0 {
			return "", "", false
		}
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", "", false
		}
	}
	if d.Key == "" || d.Project == "" {
		return "", "", false
	}
	// The final path component is Sentry's numeric project ID; preceding
	// components support trackers installed below a path prefix.
	parts := strings.Split(d.Project, "/")
	if _, err := strconv.ParseUint(parts[len(parts)-1], 10, 64); err != nil {
		return "", "", false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", "", false
		}
	}
	u.Host = strings.ToLower(u.Host)
	origin := u.String()
	u.User, u.Path = url.User(d.Key), "/"+d.Project
	return u.String(), origin, true
}
