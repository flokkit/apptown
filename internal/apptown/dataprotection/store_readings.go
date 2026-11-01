package dataprotection

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dataprotection/internal/protectionsql"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/ids"
)

const StoreObservationJob = "backup-store-observation"

func storeIdentity(cfg *BackupStore) (string, error) {
	raw, err := json.Marshal(cfg)
	return fmt.Sprintf("%x", sha256.Sum256(raw)), err
}
func (s *core) storeReading(ctx context.Context, identity string) (deployment.DetailReading, error) {
	var out deployment.DetailReading
	row, err := protectionsql.New(s.db.SQL()).Reading(ctx, protectionsql.ReadingParams{Subject: "instance", Kind: "backup-store"})
	if errors.Is(err, sql.ErrNoRows) || err == nil && row.Identity != identity {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(row.Payload, &out)
	return out, err
}

// Store reads only the local configuration and the bucket check's stored result.
func (s *core) Store(ctx context.Context) (*BackupStore, error) {
	cfg, err := s.store(ctx)
	if err != nil || cfg == nil {
		return cfg, err
	}
	identity, err := storeIdentity(cfg)
	if err != nil {
		return nil, err
	}
	r, err := s.storeReading(ctx, identity)
	if err != nil {
		return nil, err
	}
	r.Stale = r.ObservedAt.IsZero() || s.now().Sub(r.ObservedAt) > 15*time.Minute
	r.Unknown = r.ObservedAt.IsZero() || s.now().Sub(r.ObservedAt) > 6*time.Hour
	if r.AttemptedAt.IsZero() {
		r.Failure = "Waiting for the first background backup-store check."
	}
	cfg.Reading = r
	return cfg, nil
}

func (s *Service) ObserveStore(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
	cfg, err := s.store(ctx)
	if err != nil || cfg == nil {
		return nil, err
	}
	identity, err := storeIdentity(cfg)
	if err != nil {
		return nil, err
	}
	prior, err := s.storeReading(ctx, identity)
	if err != nil {
		return nil, err
	}
	client, checkErr := s.storeClientFor(ctx, cfg)
	if checkErr == nil {
		checkErr = checkStore(ctx, client, cfg.Bucket)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil, ctx.Err()
	}
	prior.AttemptedAt = s.now()
	prior.Failure = ""
	if checkErr == nil {
		prior.ObservedAt = s.now()
	} else {
		prior.Failure = "The backup-store check failed: " + checkErr.Error()
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	current, err := s.store(finish)
	if err != nil {
		return nil, errors.Join(checkErr, err)
	}
	currentIdentity, err := storeIdentity(current)
	if err != nil || currentIdentity != identity {
		return nil, err
	}
	raw, err := json.Marshal(prior)
	if err != nil {
		return nil, err
	}
	err = protectionsql.New(s.db.SQL()).StoreReading(finish, protectionsql.StoreReadingParams{ID: ids.New(s.now()), Subject: "instance", Kind: "backup-store", Identity: identity, Payload: raw})
	return nil, errors.Join(checkErr, err)
}
