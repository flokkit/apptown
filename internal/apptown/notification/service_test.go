package notification

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/attention"
	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	q "github.com/flokkit/apptown/internal/apptown/notification/internal/notificationsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/fault"
)

var suite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M) { os.Exit(suite.Run(m)) }

type queue struct {
	fail bool
	id   int64
}

func (j *queue) EnqueueIn(ctx context.Context, tx *database.Tx, r jobs.Request) error {
	if j.fail {
		return errors.New("queue unavailable")
	}
	result, err := jobs.InsertOnTx(ctx, tx.Native(), "", r, Attempts, time.Time{})
	if err == nil {
		j.id = result.Job.ID
	}
	return err
}

type secrets struct {
	values map[string]string
	err    error
}

func (s *secrets) WithContext(context.Context) (map[string]string, error) { return s.values, s.err }

type authority struct{ err error }

func (a *authority) Require(context.Context, access.Permission) (access.Caller, error) {
	return access.Caller{}, a.err
}

type fixture struct {
	s         *Service
	db        *database.DB
	now       time.Time
	view      attention.View
	readErr   error
	queue     *queue
	secrets   *secrets
	authority *authority
}

func setup(t *testing.T) *fixture {
	t.Helper()
	db := suite.New(t)
	if err := jobs.Migrate(t.Context(), db.Pool()); err != nil {
		t.Fatal(err)
	}
	f := &fixture{db: db, now: time.Date(2026, 11, 13, 7, 0, 0, 0, time.UTC), queue: &queue{}, secrets: &secrets{values: map[string]string{}}, authority: &authority{}}
	f.view = attention.View{Sources: []attention.Coverage{{Key: "deployments/p"}}}
	f.s = New(Dependencies{DB: db, Jobs: f.queue, Secrets: f.secrets, Authority: f.authority, Now: func() time.Time { return f.now }, Origin: "https://apps.example", ReadAttention: func(ctx context.Context) (attention.View, error) {
		c, ok := access.CallerFrom(ctx)
		if !ok || !c.Verified || c.Principal.Instrument != access.SystemInstrument || c.Principal.Via != "notifications" || !c.Principal.ProjectID.IsZero() {
			t.Error("attention must be read as the unbound system instrument")
		}
		return f.view, f.readErr
	}})
	return f
}
func (f *fixture) channel(t *testing.T, name string, hour int) {
	t.Helper()
	b := integration.NewWithClock(f.db, func() time.Time { return f.now })
	cfg, _ := json.Marshal(map[string]any{"urlFrom": []string{"URL"}, "format": "json", "digestHour": hour})
	if _, err := b.Save(t.Context(), name, integration.CapNotifier, "webhook", string(cfg), "admin"); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) evaluate(t *testing.T) {
	t.Helper()
	if _, err := f.s.Evaluate(t.Context(), "scheduled", nil); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) deliveries(t *testing.T) []q.NotificationDelivery {
	t.Helper()
	rs, err := q.New(f.db.SQL()).Recent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return rs
}
func warning(key string) attention.Item {
	return attention.Item{Key: "deployments/p/" + key, Title: "App failed", Detail: "Check the app.", Href: "/projects/app", Severity: "warning"}
}
func TestBaselineSurvivesEmptyEvaluationRestartAndRetention(t *testing.T) {
	t.Parallel()
	f := setup(t)
	f.channel(t, "phone", 8)
	f.evaluate(t)
	// Reconstruct the owner; initialization must come from the database.
	f.s = New(f.s.d)
	f.view.Items = []attention.Item{warning("one")}
	f.evaluate(t)
	f.evaluate(t)
	rs := f.deliveries(t)
	if len(rs) != 1 || rs[0].Kind != "opened" {
		t.Fatalf("new warning after empty baseline: %+v", rs)
	}
	f.view.Items = nil
	f.evaluate(t)
	if len(f.deliveries(t)) != 2 {
		t.Fatal("announced warning must resolve")
	}
	f.now = f.now.AddDate(0, 0, 31)
	f.evaluate(t)
	if len(f.deliveries(t)) != 0 {
		t.Fatal("history not retained")
	}
	f.s = New(f.s.d)
	f.view.Items = []attention.Item{warning("two")}
	f.evaluate(t)
	if len(f.deliveries(t)) != 1 {
		t.Fatal("retention reset initialization")
	}
}
func TestInitialBacklogAndNoChannelStayQuiet(t *testing.T) {
	t.Parallel()
	f := setup(t)
	f.view.Items = []attention.Item{warning("old")}
	f.channel(t, "phone", 8)
	f.evaluate(t)
	f.evaluate(t)
	if len(f.deliveries(t)) != 0 {
		t.Fatal("baseline announced backlog")
	}
	f.now = f.now.Add(time.Hour)
	f.evaluate(t)
	if rs := f.deliveries(t); len(rs) != 1 || rs[0].Kind != "digest" {
		t.Fatalf("backlog missing from digest: %+v", rs)
	}
	g := setup(t)
	g.evaluate(t)
	g.view.Items = []attention.Item{warning("new")}
	g.evaluate(t)
	if len(g.deliveries(t)) != 0 {
		t.Fatal("sent without channel")
	}
	snap, err := g.s.Snapshot(t.Context())
	if err != nil || len(snap.Items) != 0 {
		t.Fatalf("no channel attention: %+v %v", snap, err)
	}
}
func TestEvaluationUncertaintyResolutionAndDigest(t *testing.T) {
	t.Parallel()
	f := setup(t)
	f.channel(t, "phone", 8)
	f.channel(t, "chat", 9)
	f.evaluate(t)
	urgent := warning("urgent")
	advisory := warning("advisory")
	advisory.Severity = "advisory"
	unknown := warning("unknown")
	unknown.Unknown = true
	stale := warning("stale")
	stale.Stale = true
	f.view.Items = []attention.Item{urgent, advisory, unknown, stale}
	f.evaluate(t)
	f.evaluate(t)
	if len(f.deliveries(t)) != 2 {
		t.Fatal("only known fresh warning should announce, once per channel")
	}
	// The item disappears during an owner's grace window, with unchanged key.
	f.view.Items = f.view.Items[1:]
	f.view.Sources[0].Withheld = true
	f.evaluate(t)
	if len(f.deliveries(t)) != 2 {
		t.Fatal("withheld absence resolved an item")
	}
	f.view.Sources[0].Withheld = false
	f.view.Sources[0].Unknown = true
	f.evaluate(t)
	if len(f.deliveries(t)) != 2 {
		t.Fatal("unknown absence resolved an item")
	}
	f.view.Sources = nil
	f.evaluate(t)
	if len(f.deliveries(t)) != 2 {
		t.Fatal("missing source resolved an item")
	}
	f.view.Sources = []attention.Coverage{{Key: "deployments/p"}}
	f.evaluate(t)
	if len(f.deliveries(t)) != 4 {
		t.Fatal("fresh healthy source must resolve announced warning")
	}
	f.now = f.now.Add(time.Hour)
	f.evaluate(t)
	f.evaluate(t)
	rs := f.deliveries(t)
	if len(rs) != 5 {
		t.Fatalf("one daily digest at phone hour: %d", len(rs))
	}
	var m Message
	for _, r := range rs {
		if r.Kind == "digest" {
			if err := json.Unmarshal(r.Body, &m); err != nil {
				t.Fatal(err)
			}
		}
	}
	text := strings.Join(m.Lines, "\n")
	for _, want := range []string{"Needs attention", "Not known", "Cleared"} {
		if !strings.Contains(text, want) {
			t.Errorf("digest missing %s: %s", want, text)
		}
	}
	f.readErr = errors.New("attention unavailable")
	f.view.Items = []attention.Item{warning("must-not-open")}
	if _, err := f.s.Evaluate(t.Context(), "", nil); err == nil {
		t.Fatal("read failure lost")
	}
	if len(f.deliveries(t)) != 5 {
		t.Fatal("read failure changed records")
	}
}
func TestUrgencyTransitionAndUnannouncedResolution(t *testing.T) {
	t.Parallel()
	f := setup(t)
	f.channel(t, "phone", 8)
	f.evaluate(t)
	i := warning("item")
	i.Stale = true
	f.view.Items = []attention.Item{i}
	f.evaluate(t)
	if len(f.deliveries(t)) != 0 {
		t.Fatal("stale item paged")
	}
	f.view.Items[0].Stale = false
	f.evaluate(t)
	if len(f.deliveries(t)) != 1 {
		t.Fatal("confirmed warning did not page")
	}
	f.view.Items = nil
	f.evaluate(t)
	a := warning("quiet")
	a.Severity = "advisory"
	f.view.Items = []attention.Item{a}
	f.evaluate(t)
	f.view.Items = nil
	f.evaluate(t)
	if len(f.deliveries(t)) != 2 {
		t.Fatal("unannounced resolution sent immediately")
	}
}
func TestQueueFailureRollsBackEvaluation(t *testing.T) {
	t.Parallel()
	f := setup(t)
	f.channel(t, "phone", 8)
	f.evaluate(t)
	f.view.Items = []attention.Item{warning("new")}
	f.queue.fail = true
	if _, err := f.s.Evaluate(t.Context(), "", nil); err == nil {
		t.Fatal("queue failure ignored")
	}
	rs, err := q.New(f.db.SQL()).Items(t.Context())
	if err != nil || len(rs) != 0 {
		t.Fatalf("partial evaluation committed: %+v %v", rs, err)
	}
	f.queue.fail = false
	f.evaluate(t)
	if len(f.deliveries(t)) != 1 {
		t.Fatal("retry lost event")
	}
}
func TestDeliveryRecordsOutcomeAndSkipsCompletedJobs(t *testing.T) {
	t.Parallel()
	f := setup(t)
	var calls atomic.Int32
	var status atomic.Int32
	status.Store(500)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(int(status.Load())) }))
	defer server.Close()
	f.secrets.values["URL"] = server.URL + "/secret-topic"
	f.channel(t, "phone", 8)
	f.evaluate(t)
	f.view.Items = []attention.Item{warning("new")}
	f.evaluate(t)
	id := f.deliveries(t)[0].ID
	for n := 1; n <= Attempts; n++ {
		_, err := f.s.Deliver(t.Context(), id.String(), nil)
		r := f.deliveries(t)[0]
		if n < Attempts {
			if err == nil || r.State != "retrying" || r.Reason != "HTTP 500" {
				t.Fatalf("retry: %+v %v", r, err)
			}
		} else if err != nil || r.State != "failed" {
			t.Fatalf("exhausted: %+v %v", r, err)
		}
	}
	if _, err := f.s.Deliver(t.Context(), id.String(), nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != Attempts {
		t.Fatal("failed delivery sent again")
	}
	snap, err := f.s.Snapshot(t.Context())
	if err != nil || len(snap.Items) != 1 {
		t.Fatalf("failure attention: %+v %v", snap, err)
	}
	status.Store(204)
	d, err := f.s.Test(t.Context(), "phone")
	if err != nil || d.State != "sent" {
		t.Fatalf("test: %+v %v", d, err)
	}
	if _, err := f.s.Deliver(t.Context(), d.ID.String(), nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != Attempts+1 {
		t.Fatal("sent delivery sent again")
	}
	snap, err = f.s.Snapshot(t.Context())
	if err != nil || len(snap.Items) != 0 {
		t.Fatalf("later sent did not clear: %+v %v", snap, err)
	}
}
func TestSecretFailuresNeverEscapeToRecordsJobErrorsOrResponses(t *testing.T) {
	t.Parallel()
	for _, lookup := range []bool{false, true} {
		t.Run(map[bool]string{false: "URL", true: "lookup"}[lookup], func(t *testing.T) {
			f := setup(t)
			secret := "http://bad host/top-secret-token"
			f.secrets.values["URL"] = secret
			if lookup {
				f.secrets.err = errors.New("provider reply " + secret)
			}
			f.channel(t, "phone", 8)
			f.evaluate(t)
			f.view.Items = []attention.Item{warning("new")}
			f.evaluate(t)
			_, err := f.s.Deliver(t.Context(), f.deliveries(t)[0].ID.String(), nil)
			if err == nil || strings.Contains(err.Error(), secret) {
				t.Fatalf("unsafe job error: %v", err)
			}
			v, err := f.s.Read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(v)
			if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "top-secret-token") {
				t.Fatalf("unsafe API response: %s", raw)
			}
		})
	}
}
func TestAdministrationRequiresConnectorPermission(t *testing.T) {
	t.Parallel()
	f := setup(t)
	f.authority.err = fault.New(fault.Forbidden, "denied")
	if _, err := f.s.Test(t.Context(), "anything"); fault.KindOf(err) != fault.Forbidden {
		t.Fatalf("test allowed: %v", err)
	}
	if _, err := f.s.Read(t.Context()); fault.KindOf(err) != fault.Forbidden {
		t.Fatalf("read allowed: %v", err)
	}
}
func TestEmptyDigestIsNotCreated(t *testing.T) {
	t.Parallel()
	f := setup(t)
	f.channel(t, "phone", 7)
	f.evaluate(t)
	f.evaluate(t)
	if len(f.deliveries(t)) != 0 {
		t.Fatal("empty digest sent")
	}
}
func TestRemovedChannelEndsPendingDelivery(t *testing.T) {
	t.Parallel()
	f := setup(t)
	f.channel(t, "phone", 8)
	f.evaluate(t)
	f.view.Items = []attention.Item{warning("new")}
	f.evaluate(t)
	if err := integration.NewWithClock(f.db, func() time.Time { return f.now }).Delete(t.Context(), "phone", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Deliver(t.Context(), f.deliveries(t)[0].ID.String(), nil); err != nil {
		t.Fatal(err)
	}
	if r := f.deliveries(t)[0]; r.State != "failed" || r.Reason != "channel removed" {
		t.Fatalf("removed: %+v", r)
	}
}

func TestQueuedMessageKeepsContentFromCreation(t *testing.T) {
	t.Parallel()
	f := setup(t)
	f.channel(t, "phone", 8)
	f.evaluate(t)
	f.view.Items = []attention.Item{warning("new")}
	f.evaluate(t)
	before := f.deliveries(t)[0]
	f.view.Items[0].Title = "A different title"
	f.view.Items[0].Detail = "New evidence"
	f.evaluate(t)
	after := f.deliveries(t)[0]
	if before.ID != after.ID || string(before.Body) != string(after.Body) {
		t.Fatal("a later attention read rewrote a queued notice")
	}
}

func TestDigestRunsAtEachChannelsHourOncePerUTCDate(t *testing.T) {
	t.Parallel()
	f := setup(t)
	f.channel(t, "phone", 8)
	f.channel(t, "chat", 9)
	f.evaluate(t)
	f.view.Items = []attention.Item{warning("open")}
	f.evaluate(t)
	f.now = f.now.Add(time.Hour)
	f.evaluate(t)
	f.evaluate(t)
	f.now = f.now.Add(time.Hour)
	f.evaluate(t)
	f.now = f.now.AddDate(0, 0, 1).Add(-time.Hour)
	f.evaluate(t)
	digests := map[string]int{}
	for _, d := range f.deliveries(t) {
		if d.Kind != "digest" {
			continue
		}
		digests[d.Channel]++
		var m Message
		if err := json.Unmarshal(d.Body, &m); err != nil {
			t.Fatal(err)
		}
		if len(m.Items) != 1 || m.Items[0].Section != "Still open" {
			t.Fatalf("known open warning: %+v", m)
		}
	}
	if digests["phone"] != 2 || digests["chat"] != 1 {
		t.Fatalf("digest schedule: %+v", digests)
	}
}
