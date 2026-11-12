package notification

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"github.com/flokkit/apptown/internal/apptown/attention"
	"log"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/jobs"
)

type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *lockedLog) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

type leakReporter struct{}

func (leakReporter) Error(err error)                 { log.Printf("jobs: %v", err) }
func (leakReporter) Panic(_ string, _ any, _ []byte) { log.Print("job panicked") }

// Serial: the production queue captures the global logger at construction.
func TestSecretsAbsentFromRiverErrorsLogsAndDeliveryViews(t *testing.T) {
	for _, kind := range []string{"webhook", "lookup", "missing", "smtp"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			const canary = "NEVER-EXPOSE-THIS-SECRET"
			ctx := t.Context()
			if kind == "smtp" {
				cert, cfg := smtpTLS(t)
				f.smtpPeer(t, ctx, cert, cfg)
				f.secrets.values["PASSWORD"] = canary
				_, err := integration.NewWithClock(f.db, func() time.Time { return f.now }).Save(ctx, "phone", integration.CapNotifier, "smtp", `{"host":"smtp.example","port":587,"security":"starttls","username":"admin","passwordFrom":["PASSWORD"],"from":"notices@example.com","to":"admin@example.com"}`, "admin")
				if err != nil {
					t.Fatal(err)
				}
			} else {
				f.channel(t, "phone", 8)
				f.secrets.values["URL"] = "http://127.0.0.1:0/" + canary
				if kind == "missing" {
					f.secrets.values = nil
				}
				if kind == "lookup" {
					f.secrets.err = &net.DNSError{Err: canary, Name: canary}
				}
			}
			f.evaluate(t)
			f.view.Items = []attention.Item{warning("new")}
			f.evaluate(t)
			var logs lockedLog
			prior := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(prior)
			worker, err := jobs.New(jobs.Options{}, f.db.Pool(), map[string]jobs.Kind{DeliverJob: {Run: f.s.Deliver, MaxAttempts: Attempts}}, leakReporter{})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			stopped := make(chan error, 1)
			go func() { stopped <- worker.Run(ctx) }()
			var stopOnce sync.Once
			stop := func() {
				stopOnce.Do(func() {
					cancel()
					deadline, release := context.WithTimeout(t.Context(), 10*time.Second)
					defer release()
					select {
					case err := <-stopped:
						if err != nil {
							t.Error(err)
						}
					case <-deadline.Done():
						t.Error("delivery consumer did not stop")
					}
				})
			}
			defer stop()

			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			var jobErrors string
			for jobErrors == "" || jobErrors == "[]" || jobErrors == "null" {
				row, err := worker.Client.JobGet(ctx, f.queue.id)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				raw, err := json.Marshal(row.Errors)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				jobErrors = string(raw)

				if jobErrors != "" && jobErrors != "[]" && jobErrors != "null" {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("delivery did not record a River error")
				case <-ticker.C:
				}
			}
			stop()
			v, err := f.s.Read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			for label, value := range map[string]string{"River errors": jobErrors, "logs": logs.String(), "API": string(raw)} {
				if strings.Contains(value, canary) || strings.Contains(value, "secret-password") {
					t.Fatalf("secret leaked into %s", label)
				}
			}
			if kind == "smtp" && !strings.Contains(jobErrors, "SMTP 535") {
				t.Fatalf("SMTP error was not safely classified: %s", jobErrors)
			}
		})
	}
}

func (f *fixture) smtpPeer(t *testing.T, ctx context.Context, cert tls.Certificate, cfg *tls.Config) {
	t.Helper()
	f.s.smtp = smtpTransport{tls: cfg, dial: func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() { _ = fakeSMTP(ctx, server, cert, false) }()
		return client, nil
	}}
}

func TestSMTPRejectionEndsFailedWithSafeReason(t *testing.T) {
	t.Parallel()
	f := setup(t)
	ctx := t.Context()
	cert, cfg := smtpTLS(t)
	f.smtpPeer(t, ctx, cert, cfg)
	f.secrets.values["PASSWORD"] = "secret-password"
	if _, err := integration.NewWithClock(f.db, func() time.Time { return f.now }).Save(ctx, "mail", integration.CapNotifier, "smtp", `{"host":"smtp.example","port":587,"security":"starttls","username":"admin","passwordFrom":["PASSWORD"],"from":"notices@example.com","to":"admin@example.com"}`, "admin"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	d, err := f.s.Test(ctx, "mail")
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n < Attempts; n++ {
		_, _ = f.s.Deliver(ctx, d.ID.String(), nil)
	}
	r := f.deliveries(t)[0]
	if r.State != "failed" || r.Reason != "SMTP 535" || r.Attempts != Attempts {
		t.Fatalf("SMTP exhaustion: %+v", r)
	}
}
