// Package notification announces attention items through integration bindings.
// Its durable baseline and delivery ledger survive restarts. Remote acceptance
// followed by a crash before recording sent can still cause one duplicate.
package notification

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/attention"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	q "github.com/flokkit/apptown/internal/apptown/notification/internal/notificationsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

const EvaluateJob = "notify-evaluate"
const DeliverJob = "notify-deliver"
const Attempts = 6

type Secrets interface {
	WithContext(context.Context) (map[string]string, error)
}
type Dependencies struct {
	DB            *database.DB
	Jobs          jobs.Enqueuer
	Secrets       Secrets
	Authority     access.Authorizer
	ReadAttention func(context.Context) (attention.View, error)
	Now           func() time.Time
	Origin        string
}
type Service struct {
	d    Dependencies
	smtp smtpTransport
}

func New(d Dependencies) *Service { return &Service{d: d} }

type channelConfig struct {
	URLFrom      []string `json:"urlFrom"`
	PasswordFrom []string `json:"passwordFrom"`
	Format       string   `json:"format"`
	DigestHour   *int     `json:"digestHour"`
	Host         string   `json:"host"`
	Port         int      `json:"port"`
	Security     string   `json:"security"`
	Username     string   `json:"username"`
	From         string   `json:"from"`
	To           string   `json:"to"`
}

func config(b integration.Binding) (channelConfig, error) {
	c := channelConfig{Format: "json", Port: 587, Security: "starttls"}
	if err := json.Unmarshal([]byte(b.ConfigJSON), &c); err != nil {
		return c, errors.New("invalid channel settings")
	}
	if c.DigestHour != nil && (*c.DigestHour < 0 || *c.DigestHour > 23) {
		return c, errors.New("invalid channel settings")
	}
	return c, nil
}
func (c channelConfig) hour() int {
	if c.DigestHour == nil {
		return 8
	}
	return *c.DigestHour
}
func channels(ctx context.Context, db database.Queryer) ([]integration.Binding, error) {
	return integration.ForCapability(ctx, db, integration.CapNotifier)
}
func currentEnough(key string, v attention.View, allowWithheld bool) bool {
	for _, c := range v.Sources {
		if key == c.Key || strings.HasPrefix(key, c.Key+"/") {
			return !c.Unknown && !c.Stale && (allowWithheld || !c.Withheld)
		}
	}
	return false
}
func urgent(i attention.Item) bool {
	return i.Severity == attention.SeverityWarning && !i.Unknown && !i.Stale
}
func (s *Service) Evaluate(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
	ctx = access.WithCaller(ctx, access.Caller{Verified: true, Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "notifications"}})
	v, err := s.d.ReadAttention(ctx)
	if err != nil {
		return nil, err
	}
	now := s.d.Now().UTC()
	err = s.d.DB.InTx(ctx, func(tx *database.Tx) error {
		db := q.New(tx.SQL())
		if err := db.LockBaseline(ctx); err != nil {
			return err
		}
		baseline, err := db.HasBaseline(ctx)
		if err != nil {
			return err
		}
		cs, err := channels(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := db.Items(ctx)
		if err != nil {
			return err
		}
		open := map[string]q.NotificationItem{}
		for _, r := range rows {
			if !r.ResolvedAt.Valid {
				open[r.Key] = r
			}
		}
		present := map[string]bool{}
		for _, i := range v.Items {
			present[i.Key] = true
			r, found := open[i.Key]
			wasUrgent := r.Urgent
			if !found {
				r = q.NotificationItem{ID: ids.New(now), Key: i.Key, OpenedAt: now}
				if err := db.OpenItem(ctx, q.OpenItemParams{ID: r.ID, Key: i.Key, Title: i.Title, Detail: i.Detail, Href: i.Href, Severity: i.Severity, Unknown: i.Unknown, Stale: i.Stale, Urgent: urgent(i), OpenedAt: now}); err != nil {
					return err
				}
			}
			r.Title = i.Title
			r.Detail = i.Detail
			r.Href = i.Href
			r.Severity = i.Severity
			r.Unknown = i.Unknown
			r.Stale = i.Stale
			r.Urgent = urgent(i)
			if err := db.RefreshItem(ctx, q.RefreshItemParams{ID: r.ID, Title: r.Title, Detail: r.Detail, Href: r.Href, Severity: r.Severity, Unknown: r.Unknown, Stale: r.Stale, Urgent: r.Urgent}); err != nil {
				return err
			}
			if baseline && r.Urgent && (!found || !wasUrgent) && !r.Announced && len(cs) > 0 {
				for _, c := range cs {
					if _, err := s.create(ctx, tx, c.Name, "opened", r, "", messageFor("opened", []q.NotificationItem{r}, s.d.Origin), now); err != nil {
						return err
					}
				}
				if err := db.Announce(ctx, r.ID); err != nil {
					return err
				}
			}
		}
		for key, r := range open {
			if present[key] || !currentEnough(key, v, false) {
				continue
			}
			if err := db.Resolve(ctx, q.ResolveParams{ID: r.ID, ResolvedAt: sql.NullTime{Time: now, Valid: true}}); err != nil {
				return err
			}
			if baseline && r.Announced {
				for _, c := range cs {
					if _, err := s.create(ctx, tx, c.Name, "resolved", r, "", messageFor("resolved", []q.NotificationItem{r}, s.d.Origin), now); err != nil {
						return err
					}
				}
			}
		}
		if err := db.Baseline(ctx, now); err != nil {
			return err
		}
		rows, err = db.Items(ctx)
		if err != nil {
			return err
		}
		if baseline {
			for _, c := range cs {
				cfg, err := config(c)
				if err != nil {
					return err
				}
				if cfg.hour() != now.Hour() {
					continue
				}
				last, err := db.LastDigest(ctx, c.Name)
				if err != nil {
					return err
				}
				selected := []q.NotificationItem{}
				for _, r := range rows {
					if r.ResolvedAt.Valid {
						if !r.ResolvedAt.Time.After(last) {
							continue
						}
					} else if !currentEnough(r.Key, v, true) {
						r.Stale = true
					}
					selected = append(selected, r)
				}
				if len(selected) == 0 {
					continue
				}
				if _, err := s.create(ctx, tx, c.Name, "digest", q.NotificationItem{}, now.Format("2006-01-02"), messageFor("digest", selected, s.d.Origin), now); err != nil {
					return err
				}
			}
		}
		if err := db.RetainDeliveries(ctx, now.AddDate(0, 0, -30)); err != nil {
			return err
		}
		return db.RetainItems(ctx, sql.NullTime{Time: now.AddDate(0, 0, -30), Valid: true})
	})
	return nil, err
}
func (s *Service) create(ctx context.Context, tx *database.Tx, channel, kind string, item q.NotificationItem, date string, m Message, now time.Time) (ids.ID, error) {
	id := ids.New(now)
	m.ID = id.String()
	m.Kind = kind
	body, err := json.Marshal(m)
	if err != nil {
		return id, err
	}
	n, err := q.New(tx.SQL()).CreateDelivery(ctx, q.CreateDeliveryParams{ID: id, Channel: channel, Kind: kind, ItemKey: item.Key, OpenedAt: sql.NullTime{Time: item.OpenedAt, Valid: !item.OpenedAt.IsZero()}, DigestDate: date, Body: body, CreatedAt: now})
	if err != nil || n == 0 {
		return id, err
	}
	return id, s.d.Jobs.EnqueueIn(ctx, tx, jobs.Request{Operation: DeliverJob, Key: id.String()})
}
