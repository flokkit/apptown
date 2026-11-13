package notification

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"time"

	"github.com/flokkit/apptown/internal/apptown/jobs"
	q "github.com/flokkit/apptown/internal/apptown/notification/internal/notificationsql"
	"github.com/flokkit/apptown/internal/ids"
)

func (s *Service) Deliver(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	id, err := ids.Parse(key)
	if err != nil {
		return nil, jobs.Permanent(errors.New("invalid delivery id"))
	}
	db := q.New(s.d.DB.SQL())
	r, err := db.Delivery(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.State == "sent" || r.State == "failed" {
		return nil, nil
	}
	cs, err := channels(ctx, s.d.DB)
	if err != nil {
		return nil, err
	}
	reason := "channel removed"
	for _, b := range cs {
		if b.Name != r.Channel {
			continue
		}
		cfg, e := config(b)
		if e != nil {
			reason = "invalid channel settings"
			break
		}
		values, e := s.d.Secrets.WithContext(ctx)
		if e != nil {
			reason = "could not read channel secret"
			break
		}
		var m Message
		if json.Unmarshal(r.Body, &m) != nil {
			reason = "invalid message"
			break
		}
		switch b.Provider {
		case "webhook":
			reason = sendWebhook(ctx, cfg, values, m)
		case "smtp":
			reason = s.sendSMTP(ctx, cfg, values, m)
		default:
			reason = "invalid channel settings"
		}
		break
	}
	now := s.d.Now().UTC()
	state := "sent"
	next := sql.NullTime{}
	finished := sql.NullTime{Time: now, Valid: true}
	if reason != "" {
		state = "retrying"
		finished.Valid = false
		next = sql.NullTime{Time: now.Add(retryDelay(int(r.Attempts) + 1)), Valid: true}
		if r.Attempts+1 >= Attempts || reason == "channel removed" {
			state = "failed"
			finished.Valid = true
			next.Valid = false
		}
	}
	n, err := db.SetDeliveryState(ctx, q.SetDeliveryStateParams{ID: id, State: state, Attempts: r.Attempts, Reason: reason, NextAt: next, FinishedAt: finished})
	if err != nil {
		return nil, err
	}

	if n > 0 && state == "retrying" {
		return nil, retryError(reason)
	}
	return nil, nil
}

// River's default retry delay is attempt^4 seconds plus jitter.
func retryDelay(n int) time.Duration { return time.Duration(n*n*n*n) * time.Second }

// retryError carries only an already classified safe reason across River's log boundary.
type retryError string

func (e retryError) Error() string { return "notification: " + string(e) }
