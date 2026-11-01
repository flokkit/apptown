package dataprotection

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/dataprotection/internal/protectionsql"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/s3bucket"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/stamp"
)

// The backup store (design/subsystems/deployment.md, "Protecting data
// off-host"): one
// private bucket in an S3-compatible store that a platform administrator
// attaches, where AppTown keeps the copies it takes off a deployment's
// host. AppTown does not make the bucket: it is attached existing and
// private. Its key pair is named by two secrets, resolved for each
// operation by AppTown and never given to a copy job. Whoever holds a key
// to the bucket can read the copies in it; encryption at rest is the
// store's claim, not something AppTown witnesses.

// BackupStore is the attached store, as recorded.
type BackupStore struct {
	Endpoint      string `json:"endpoint"`
	Region        string `json:"region"`
	Bucket        string `json:"bucket"`
	AccessKeyFrom string `json:"accessKeyFrom"`
	SecretKeyFrom string `json:"secretKeyFrom"`
	AttachedBy    string `json:"attachedBy"`
	// AttachedByPrincipal is the attacher's principal; zero when not resolved to a person.
	AttachedByPrincipal ids.ID     `json:"attachedByPrincipal,omitzero"`
	AttachedAt          stamp.Time `json:"attachedAt"`
	// CheckedAt is when a write, read back and delete last succeeded.
	CheckedAt stamp.Time `json:"checkedAt"`
}

// AttachStore is what an administrator gives to attach the store.
type AttachStore struct {
	Endpoint      string `json:"endpoint"`
	Region        string `json:"region"`
	Bucket        string `json:"bucket"`
	AccessKeyFrom string `json:"accessKeyFrom"`
	SecretKeyFrom string `json:"secretKeyFrom"`
}

var (
	errStoreInput = fault.New(fault.Invalid, "a backup store needs an endpoint URL, a region, a bucket and the names of the secrets holding its key pair")
	// errNoStore: production data is not protected off its host until a
	// store is attached.
	errNoStore error = &deployment.Unavailable{Reason: "no-store", Why: "Backups and restore tests need a backup store, and none is attached. An administrator attaches one under Deployments → Backups."}
	// errStoreCheck: the store did not take a write, give it back and
	// delete it; nothing was attached.
	errStoreCheck = fault.New(fault.Unprocessable, "the backup store did not pass its check; nothing was attached")
	// errStoreHoldsSets: another bucket is not attached while copies live
	// in the attached one; they are read and pruned where they were written.
	errStoreHoldsSets = fault.New(fault.Conflict, "copies are kept in the attached bucket")
)

func storeRecord(r protectionsql.BackupStore) *BackupStore {
	return &BackupStore{Endpoint: r.Endpoint, Region: r.Region, Bucket: r.Bucket, AccessKeyFrom: r.AccessKeyFrom, SecretKeyFrom: r.SecretKeyFrom,
		AttachedBy: r.AttachedBy, AttachedByPrincipal: r.AttachedByPrincipal, AttachedAt: stamp.Of(r.AttachedAt), CheckedAt: stamp.Of(r.CheckedAt)}
}

// Store is the attached store, nil when none is.
func (s *core) Store(ctx context.Context) (*BackupStore, error) {
	r, err := protectionsql.New(s.db.SQL()).BackupStore(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return storeRecord(r), nil
}

// AttachStore checks a bucket and attaches it as the backup store,
// replacing any before it (copies already taken stay where they are
// recorded). The check writes a private object, reads it back, deletes it
// and confirms it is gone; any step that fails attaches nothing. Only a
// platform administrator attaches the store.
func (s *Service) AttachStore(ctx context.Context, in AttachStore) (*BackupStore, error) {
	if s.authority == nil {
		return nil, errors.New("attaching a backup store is not configured")
	}
	actor, err := s.authority.Require(ctx, access.Permission{Capability: access.CapPlatformTargets, Resource: access.Resource{Type: "instance"}, Context: access.Context{Route: "backup-store.attach", Consequential: true}})
	if err != nil {
		return nil, err
	}
	in.Endpoint, in.Region, in.Bucket = strings.TrimRight(strings.TrimSpace(in.Endpoint), "/"), strings.TrimSpace(in.Region), strings.TrimSpace(in.Bucket)
	u, perr := url.Parse(in.Endpoint)
	if perr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || in.Region == "" || in.Bucket == "" || in.AccessKeyFrom == "" || in.SecretKeyFrom == "" {
		return nil, errStoreInput
	}
	cfg := &BackupStore{Endpoint: in.Endpoint, Region: in.Region, Bucket: in.Bucket, AccessKeyFrom: in.AccessKeyFrom, SecretKeyFrom: in.SecretKeyFrom}
	if err := s.storeHoldsNoOtherSets(ctx, cfg); err != nil {
		return nil, err
	}
	c, err := s.storeClientFor(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errStoreCheck, err)
	}
	if err := checkStore(ctx, c, cfg.Bucket); err != nil {
		return nil, fmt.Errorf("%w: %w", errStoreCheck, err)
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*BackupStore, error) {
		at := s.now()
		attacher, err := identity.ActorIn(ctx, tx, actor.Principal.Name)
		if err != nil {
			return nil, err
		}
		r, err := protectionsql.New(tx.SQL()).PutBackupStore(ctx, protectionsql.PutBackupStoreParams{Endpoint: cfg.Endpoint, Region: cfg.Region, Bucket: cfg.Bucket,
			AccessKeyFrom: cfg.AccessKeyFrom, SecretKeyFrom: cfg.SecretKeyFrom, AttachedBy: actor.Principal.Name,
			AttachedByPrincipal: attacher.Principal, AttachedByVia: attacher.Via, At: at})
		if err != nil {
			return nil, err
		}
		if err := audit.At(at).RecordIn(ctx, tx, actor.Principal.Name, ids.Nil, "backup-store.attached", cfg.Bucket,
			map[string]any{"endpoint": cfg.Endpoint, "region": cfg.Region, "bucket": cfg.Bucket, "accessKeyFrom": cfg.AccessKeyFrom, "secretKeyFrom": cfg.SecretKeyFrom}); err != nil {
			return nil, err
		}
		return storeRecord(r), nil
	})
}

// checkStore writes a private object, reads it back, deletes it and
// confirms it is gone.
func checkStore(ctx context.Context, c *s3bucket.Client, bucket string) error {
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	key := ".apptown-check/" + hex.EncodeToString(nonce)
	body := []byte("AppTown checks it can keep copies here: " + key)
	if err := c.PutPrivate(ctx, bucket, key, "text/plain", body); err != nil {
		return fmt.Errorf("writing a check object: %w", err)
	}
	got, err := c.GetObject(ctx, bucket, key)
	if err != nil {
		return fmt.Errorf("reading the check object back: %w", err)
	}
	if !bytes.Equal(got, body) {
		return errors.New("the check object read back is not what was written")
	}
	if err := c.DeleteObject(ctx, bucket, key); err != nil {
		return fmt.Errorf("deleting the check object: %w", err)
	}
	if _, err := c.Head(ctx, bucket, key); !s3bucket.IsCode(err, "NoSuchKey") {
		return fmt.Errorf("the check object is still there after it was deleted (%w)", err)
	}
	return nil
}

// storeClient is a client for the attached store, with its keys resolved
// for this operation; ErrNoStore when none is attached.
func (s *core) storeClient(ctx context.Context) (*s3bucket.Client, *BackupStore, error) {
	cfg, err := s.Store(ctx)
	if err != nil {
		return nil, nil, err
	}
	if cfg == nil {
		return nil, nil, errNoStore
	}
	c, err := s.storeClientFor(ctx, cfg)
	return c, cfg, err
}

func (s *core) storeClientFor(ctx context.Context, cfg *BackupStore) (*s3bucket.Client, error) {
	return s.providers.S3BucketClient(ctx, s3bucket.Config{Endpoint: cfg.Endpoint, Region: cfg.Region, AccessKeyFrom: cfg.AccessKeyFrom, SecretKeyFrom: cfg.SecretKeyFrom})
}

// storeHoldsNoOtherSets refuses a store whose endpoint or bucket differs
// from where any unpruned set was written: that set would be read, offered
// and pruned in the wrong place. Copy the objects across by hand and then
// move the records, or wait for the sets to be pruned. The same bucket may
// be attached again (new key names, a new check).
func (s *Service) storeHoldsNoOtherSets(ctx context.Context, cfg *BackupStore) error {
	n, err := protectionsql.New(s.db.SQL()).StoreSetsElsewhere(ctx, protectionsql.StoreSetsElsewhereParams{Store: StoreBackupStore,
		OkState: BackupOK.String(), PartialState: BackupPartial.String(), Endpoint: cfg.Endpoint, Bucket: cfg.Bucket})
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: %d set(s) are kept elsewhere than %s at %s; they are read and pruned where they were written, so another bucket is attached only once none are left", errStoreHoldsSets, n, cfg.Bucket, cfg.Endpoint)
	}
	return nil
}

// storeOf is a client for the store a set was written to: the attached
// store, when the set's recorded place is that store, and never another.
func (s *core) storeOf(ctx context.Context, b *Backup) (*s3bucket.Client, *BackupStore, error) {
	c, cfg, err := s.storeClient(ctx)
	if err != nil {
		return nil, nil, err
	}
	if b.StoreBucket != cfg.Bucket || b.StoreEndpoint != cfg.Endpoint {
		return nil, nil, fmt.Errorf("set %d is in the bucket %s at %s, and the attached store is %s at %s; it is read only where it was written", b.ID, b.StoreBucket, b.StoreEndpoint, cfg.Bucket, cfg.Endpoint)
	}
	return c, cfg, nil
}
