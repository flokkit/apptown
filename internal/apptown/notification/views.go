package notification

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/attention"
	q "github.com/flokkit/apptown/internal/apptown/notification/internal/notificationsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"time"
)

func (s *Service) require(ctx context.Context) error {
	_, err := s.d.Authority.Require(ctx, access.Permission{Capability: access.CapIntegrationConnector, Resource: access.Resource{Type: "instance"}})
	return err
}

type Delivery struct {
	ID        ids.ID    `json:"id"`
	Channel   string    `json:"channel"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	Attempts  int64     `json:"attempts"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"createdAt"`
}

func delivery(r q.NotificationDelivery) (Delivery, error) {
	var m Message
	if err := json.Unmarshal(r.Body, &m); err != nil {
		return Delivery{}, errors.New("could not read notification message")
	}
	return Delivery{r.ID, r.Channel, r.Kind, m.Title, r.State, r.Attempts, r.Reason, r.CreatedAt}, nil
}

type Channel struct {
	Name       string `json:"name"`
	Label      string `json:"label"`
	Kind       string `json:"kind"`
	Format     string `json:"format"`
	DigestHour int    `json:"digestHour"`
	State      string `json:"state"`
}
type View struct {
	Channels   []Channel  `json:"channels"`
	Deliveries []Delivery `json:"deliveries"`
}

func (s *Service) Read(ctx context.Context) (View, error) {
	out := View{Channels: []Channel{}, Deliveries: []Delivery{}}
	if err := s.require(ctx); err != nil {
		return out, err
	}
	cs, err := channels(ctx, s.d.DB)
	if err != nil {
		return out, err
	}
	db := q.New(s.d.DB.SQL())
	states, err := db.LatestStates(ctx)
	if err != nil {
		return out, err
	}
	for _, c := range cs {
		cfg, err := config(c)
		if err != nil {
			return out, err
		}
		state := ""
		for _, v := range states {
			if v.Channel == c.Name {
				state = v.State
			}
		}
		label := c.DisplayLabel
		if label == "" {
			label = c.Name
		}
		out.Channels = append(out.Channels, Channel{c.Name, label, c.Provider, cfg.Format, cfg.hour(), state})
	}
	rs, err := db.Recent(ctx)
	if err != nil {
		return out, err
	}
	for _, r := range rs {
		d, err := delivery(r)
		if err != nil {
			return out, err
		}
		out.Deliveries = append(out.Deliveries, d)
	}
	return out, nil
}
func (s *Service) Test(ctx context.Context, name string) (Delivery, error) {
	if err := s.require(ctx); err != nil {
		return Delivery{}, err
	}
	cs, err := channels(ctx, s.d.DB)
	if err != nil {
		return Delivery{}, err
	}
	found := false
	for _, c := range cs {
		found = found || c.Name == name
	}
	if !found {
		return Delivery{}, fault.New(fault.NotFound, "Notification channel not found")
	}
	var id ids.ID
	err = s.d.DB.InTx(ctx, func(tx *database.Tx) error {
		var err error
		id, err = s.create(ctx, tx, name, "test", q.NotificationItem{}, "", Message{Title: "AppTown test notification", Lines: []string{"Notifications can reach this channel."}, Instance: "AppTown"}, s.d.Now().UTC())
		return err
	})
	if err != nil {
		return Delivery{}, err
	}
	_, err = s.Deliver(ctx, id.String(), nil)
	// Delivery failures are recorded and safe to show as state in the response.
	var retry retryError
	if err != nil && !errors.As(err, &retry) {
		return Delivery{}, err
	}
	r, err := q.New(s.d.DB.SQL()).Delivery(ctx, id)
	if err != nil {
		return Delivery{}, err
	}
	return delivery(r)
}
func (s *Service) Snapshot(ctx context.Context) (attention.Snapshot, error) {
	out := attention.Snapshot{Known: true, ObservedAt: s.d.Now(), Href: "/integrations/notifications", NextStep: "Check the channel's settings and send a test."}
	cs, err := channels(ctx, s.d.DB)
	if err != nil {
		return out, err
	}
	rs, err := q.New(s.d.DB.SQL()).LatestFinished(ctx)
	if err != nil {
		return out, err
	}
	for _, c := range cs {
		for _, r := range rs {
			if r.Channel == c.Name && r.State == "failed" {
				label := c.DisplayLabel
				if label == "" {
					label = c.Name
				}
				out.Items = append(out.Items, attention.Item{Key: c.Name, Title: "Notifications to " + label + " are failing", Detail: r.Reason + ". Check the channel's settings and send a test.", Href: out.Href, Severity: attention.SeverityWarning, ObservedAt: r.FinishedAt.Time})
			}
		}
	}
	return out, nil
}
