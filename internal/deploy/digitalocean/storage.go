package digitalocean

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/s3bucket"
	"flokkit.io/forge/internal/poll"
)

// --- object storage (the s3 need): a Spaces bucket per deployment ------------------------
//
// The s3 need's contract (the services-and-data guide, "What each need
// gives the app") on App Platform: the deployment's own bucket, made before
// the app starts, and a key for that bucket alone, which can put, get, list
// and delete objects there and cannot make a bucket. DigitalOcean makes a
// bucket only through the S3 API with a full-access key, and grants a key
// only to a bucket that exists. So a fulfilment mints a short-lived
// full-access maker key with the account's API token, makes the bucket with
// it, mints the deployment's readwrite key for that bucket, and deletes the
// maker key. The bucket and the key are on the deployment's owned list; the
// key's secret is kept in AppTown's secret store under the deployment's
// scope, since DigitalOcean gives it once.

const (
	// KindBucket is how the owned list names a deployment's Spaces bucket.
	KindBucket = "bucket"
	// KindSpacesKey is how the owned list names a deployment's Spaces key.
	KindSpacesKey = "spaces-key"
	// SpacesSecretName is the stored name of the deployment's key secret.
	SpacesSecretName = "S3_SECRET_ACCESS_KEY"
	needS3           = "s3"
	spacesKeysPath   = "/v2/spaces/keys"
)

// Holdings is a deployment's owned list as the driver reads and writes it:
// what AppTown made for it at the provider beyond the app.
type Holdings interface {
	Owned(ctx context.Context) ([]deploy.Owned, error)
	Created(ctx context.Context, o deploy.Owned) error
	Forget(ctx context.Context, kind, id string) error
}

// KeySecrets keeps the deployment's Spaces key secret in AppTown's own
// secret store. Get answers "" when none is stored.
type KeySecrets interface {
	Get(ctx context.Context, name string) (string, error)
	Put(ctx context.Context, name, value string) error
	Delete(ctx context.Context, name string) error
}

type spacesGrant struct {
	Bucket     string `json:"bucket"`
	Permission string `json:"permission"`
}

type spacesKey struct {
	Name      string        `json:"name"`
	AccessKey string        `json:"access_key"`
	SecretKey string        `json:"secret_key,omitempty"`
	Grants    []spacesGrant `json:"grants"`
}

// spacesRegion is the Spaces region of an App Platform region (syd: syd1).
func spacesRegion(region string) string { return doDBRegion(region) }

// SpacesEndpoint is Spaces' S3 endpoint in a region.
func SpacesEndpoint(region string) string {
	return "https://" + region + ".digitaloceanspaces.com"
}

func makerKeyName(slug, deployment string) string { return "forge-maker-" + slug + "-" + deployment }
func appKeyName(slug, deployment string) string   { return "forge-" + slug + "-" + deployment }

func (d *Driver) mintKey(ctx context.Context, c *Client, name string, grant spacesGrant) (spacesKey, error) {
	var made struct {
		Key spacesKey `json:"key"`
	}
	if err := c.Call(ctx, "POST", spacesKeysPath, map[string]any{"name": name, "grants": []spacesGrant{grant}}, &made); err != nil {
		return spacesKey{}, err
	}
	if made.Key.AccessKey == "" || made.Key.SecretKey == "" {
		return spacesKey{}, fmt.Errorf("DigitalOcean made the Spaces key %s but gave no access key or secret", name)
	}
	return made.Key, nil
}

func (d *Driver) deleteKey(ctx context.Context, c *Client, access string) error {
	if err := c.Call(ctx, "DELETE", spacesKeysPath+"/"+url.PathEscape(access), nil, nil); err != nil && !IsStatus(err, 404) {
		return err
	}
	return nil
}

// spacesKeys are the account's Spaces keys, every page.
func spacesKeys(ctx context.Context, c *Client) ([]spacesKey, error) {
	return listAll[spacesKey](ctx, c, spacesKeysPath+"?per_page=200", "keys")
}

// maker is a full-access key for one operation, deleted by its release.
type maker struct {
	key    spacesKey
	client *s3bucket.Client
}

// sweepMakers deletes every key named as the deployment's maker key: one an
// interrupted operation, or a deletion that failed, left behind. Every
// release that fulfils s3 and every take-down of its storage sweeps, so the
// promise a left key's warning makes holds. Keys it could not delete are
// returned, named; a list that could not be read is the error.
func (d *Driver) sweepMakers(ctx context.Context, c *Client, deployment string, logf func(string, ...any)) (left []deploy.Left, err error) {
	name := makerKeyName(d.Slug, deployment)
	keys, err := spacesKeys(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("reading the account's Spaces keys: %w", err)
	}
	for _, k := range keys {
		if k.Name != name {
			continue
		}
		if err := d.deleteKey(ctx, c, k.AccessKey); err != nil {
			left = append(left, makerLeft(k, err))
			continue
		}
		logf("deleted the full-access maker key %s (%s) an earlier operation left", name, k.AccessKey)
	}
	return left, nil
}

// makerLeft is a maker key that could not be deleted: a live credential
// for every bucket on the account, reported so it is retained and named.
func makerLeft(k spacesKey, err error) deploy.Left {
	return deploy.Left{What: "Spaces full-access maker key", Handle: map[string]any{"name": k.AccessKey, "keyName": k.Name, "kind": KindSpacesKey},
		Why: fmt.Sprintf("deleting it failed: %v; it can read and write every bucket on the account until it is deleted under Spaces keys", err)}
}

// withMaker runs f with a full-access maker key named for the deployment,
// after sweeping any of that name left before. The key is deleted when f
// returns, whatever happened; one whose deletion fails is answered as
// left (a live credential, which the next release or take-down sweeps).
func (d *Driver) withMaker(ctx context.Context, c *Client, region, deployment string, logf func(string, ...any), f func(m maker) error) (left []deploy.Left, err error) {
	if left, err = d.sweepMakers(ctx, c, deployment, logf); err != nil || len(left) > 0 {
		if err == nil {
			err = fmt.Errorf("a full-access maker key an earlier operation left could not be deleted: %s", left[0].Why)
		}
		return left, err
	}
	name := makerKeyName(d.Slug, deployment)
	k, err := d.mintKey(ctx, c, name, spacesGrant{Bucket: "", Permission: "fullaccess"})
	if err != nil {
		return nil, fmt.Errorf("making a full-access Spaces key to make the bucket with: %w", err)
	}
	defer func() {
		if derr := d.deleteKey(context.WithoutCancel(ctx), c, k.AccessKey); derr != nil {
			left = append(left, makerLeft(k, derr))
			logf("the full-access maker key %s (%s) was NOT deleted: %v; it is a live credential for every bucket on the account until this deployment's next release or take-down deletes it, or a person deletes it under Spaces keys", name, k.AccessKey, derr)
		}
	}()
	return nil, f(maker{key: k, client: &s3bucket.Client{Endpoint: d.options.SpacesEndpoint(region), Region: region, AccessKey: k.AccessKey, SecretKey: k.SecretKey, Now: d.options.Now}})
}

func ownedOf(owned []deploy.Owned, kind string) (deploy.Owned, bool) {
	for _, o := range owned {
		if o.Kind == kind && o.Need == needS3 {
			return o, true
		}
	}
	return deploy.Owned{}, false
}

// fulfilStorage makes the deployment's bucket and key, or reuses the ones
// its owned list records, and answers the binding: the contract's five
// variables, the key's secret as a secret. It records no need instance (no
// handle): the bucket is on the owned list, not a managed database.
func (d *Driver) fulfilStorage(ctx context.Context, p deploy.Placement, logf func(string, ...any)) (deploy.NeedBinding, map[string]any, error) {
	if d.Holdings == nil || d.Secrets == nil {
		return deploy.NeedBinding{}, nil, deploy.Terminal(errors.New("App Platform keeps no record of a deployment's bucket here, so it cannot provide s3"))
	}
	c, cfg, err := d.Client(ctx)
	if err != nil {
		return deploy.NeedBinding{}, nil, err
	}
	region := spacesRegion(cfg.Region)
	owned, err := d.Holdings.Owned(ctx)
	if err != nil {
		return deploy.NeedBinding{}, nil, fmt.Errorf("reading what the deployment owns: %w", err)
	}
	bucket, haveBucket := ownedOf(owned, KindBucket)
	key, haveKey := ownedOf(owned, KindSpacesKey)
	secret := ""
	if haveKey {
		if secret, err = d.Secrets.Get(ctx, SpacesSecretName); err != nil {
			return deploy.NeedBinding{}, nil, fmt.Errorf("reading the deployment's Spaces key secret: %w", err)
		}
	}
	if !haveBucket || !haveKey || secret == "" {
		_, err := d.withMaker(ctx, c, region, p.Name, logf, func(m maker) error {
			if !haveBucket {
				name, err := d.makeBucket(ctx, m.client, p.Name, logf)
				if err != nil {
					return err
				}
				bucket = deploy.Owned{Kind: KindBucket, ID: name, Name: name, Data: true, Need: needS3}
				if err := d.Holdings.Created(context.WithoutCancel(ctx), bucket); err != nil {
					return fmt.Errorf("recording the bucket %s AppTown made: %w; it is at the provider, empty, and not on the deployment's records", name, err)
				}
				haveBucket = true
			}
			if haveKey && secret == "" {
				// A key whose secret is gone cannot be given to the app.
				if err := d.deleteKey(ctx, c, key.ID); err != nil {
					return fmt.Errorf("deleting the Spaces key %s, whose secret AppTown no longer holds: %w", key.ID, err)
				}
				if err := d.Holdings.Forget(context.WithoutCancel(ctx), KindSpacesKey, key.ID); err != nil {
					return fmt.Errorf("forgetting the deleted Spaces key %s: %w", key.ID, err)
				}
				logf("deleted the Spaces key %s, whose secret AppTown no longer holds, to make another", key.ID)
				haveKey = false
			}
			if !haveKey {
				k, err := d.mintBucketKey(ctx, c, p.Name, bucket.ID)
				if err != nil {
					return err
				}
				key = deploy.Owned{Kind: KindSpacesKey, ID: k.AccessKey, Name: k.Name, Need: needS3}
				if err := d.Holdings.Created(context.WithoutCancel(ctx), key); err != nil {
					return fmt.Errorf("recording the Spaces key %s AppTown made: %w", k.AccessKey, err)
				}
				if err := d.Secrets.Put(context.WithoutCancel(ctx), SpacesSecretName, k.SecretKey); err != nil {
					return fmt.Errorf("keeping the Spaces key %s's secret: %w", k.AccessKey, err)
				}
				secret = k.SecretKey
				logf("made the Spaces key %s (%s) for the bucket %s alone", k.Name, k.AccessKey, bucket.ID)
			}
			return nil
		})
		if err != nil {
			return deploy.NeedBinding{}, nil, err
		}
	} else {
		// Nothing to make: still sweep a maker key an earlier operation
		// left, as its warning promised.
		left, err := d.sweepMakers(ctx, c, p.Name, logf)
		if err != nil {
			return deploy.NeedBinding{}, nil, err
		}
		for _, l := range left {
			logf("the %s %s was NOT deleted: %s", l.What, l.Handle["name"], l.Why)
		}
		logf("the bucket %s and its key %s exist", bucket.ID, key.ID)
	}
	b := deploy.NeedBinding{
		Env: map[string]string{
			"AWS_ENDPOINT_URL_S3": d.options.SpacesEndpoint(region),
			"AWS_REGION":          region,
			"S3_BUCKET":           bucket.ID,
			"AWS_ACCESS_KEY_ID":   key.ID,
		},
		Secret: map[string]string{"AWS_SECRET_ACCESS_KEY": secret},
	}
	return b, nil, nil
}

// makeBucket makes a new bucket for the deployment. A taken name is
// refused, never adopted; an answer that does not say whether the bucket
// was made is left for a person, as the bucket target does.
func (d *Driver) makeBucket(ctx context.Context, s3 *s3bucket.Client, deployment string, logf func(string, ...any)) (string, error) {
	name := s3bucket.BucketName(d.Slug, deployment, d.options.BucketSuffix())
	err := s3.CreateBucket(ctx, name)
	var refusal *s3bucket.Error
	switch {
	case errors.As(err, &refusal) && refusal.Status == 409:
		return "", deploy.Terminal(fmt.Errorf("the bucket name %s is taken (%s); AppTown never adopts a bucket it did not make — release again for another name", name, refusal.Code))
	case errors.As(err, &refusal) && refusal.Status < 500:
		return "", deploy.Terminal(fmt.Errorf("making the bucket %s: %w", name, err))
	case err != nil:
		// No answer, or a server error, says nothing about whether it was made.
		return "", fmt.Errorf("making the bucket %s: %w — whether it was made is not known; look for it under Spaces and delete it if it is there", name, err)
	}
	logf("made the bucket %s for the deployment's objects", name)
	return name, nil
}

// mintBucketKey mints the deployment's readwrite key for bucket. A bucket
// made a moment ago can be refused a grant for a while ("invalid grant"),
// so a refusal is asked again until GrantWait passes.
func (d *Driver) mintBucketKey(ctx context.Context, c *Client, deployment, bucket string) (spacesKey, error) {
	deadline := d.options.Now().Add(d.options.GrantWait)
	for {
		k, err := d.mintKey(ctx, c, appKeyName(d.Slug, deployment), spacesGrant{Bucket: bucket, Permission: "readwrite"})
		// Only "invalid grant" (403) is asked again: the new bucket is not
		// yet seen by the keys API.
		if err == nil || !IsStatus(err, 403) || d.options.Now().After(deadline) {
			if err != nil {
				return spacesKey{}, fmt.Errorf("making the Spaces key for the bucket %s: %w", bucket, err)
			}
			return k, nil
		}
		if err := poll.Wait(ctx, d.options.PollInterval); err != nil {
			return spacesKey{}, err
		}
	}
}

// BucketConnection is the connection the deployment's own Spaces key gives
// to its bucket, for a copy of its objects: the Spaces endpoint, the
// bucket, the key on the owned list and its secret from AppTown's secret
// store, read now and never recorded.
func (d *Driver) BucketConnection(ctx context.Context, bucket string) (deploy.Connection, error) {
	if d.Holdings == nil || d.Secrets == nil {
		return deploy.Connection{}, errors.New("App Platform keeps no record of the deployment's bucket here")
	}
	owned, err := d.Holdings.Owned(ctx)
	if err != nil {
		return deploy.Connection{}, fmt.Errorf("reading what the deployment owns: %w", err)
	}
	if b, ok := ownedOf(owned, KindBucket); !ok || b.ID != bucket {
		return deploy.Connection{}, fmt.Errorf("the bucket %s is not this deployment's", bucket)
	}
	key, ok := ownedOf(owned, KindSpacesKey)
	if !ok {
		return deploy.Connection{}, fmt.Errorf("the deployment has no Spaces key for its bucket %s", bucket)
	}
	secret, err := d.Secrets.Get(ctx, SpacesSecretName)
	if err != nil {
		return deploy.Connection{}, fmt.Errorf("reading the deployment's Spaces key secret: %w", err)
	}
	if secret == "" {
		return deploy.Connection{}, fmt.Errorf("AppTown no longer holds the secret of the Spaces key %s", key.ID)
	}
	_, cfg, err := d.Client(ctx)
	if err != nil {
		return deploy.Connection{}, err
	}
	endpoint, err := url.Parse(d.options.SpacesEndpoint(spacesRegion(cfg.Region)))
	if err != nil || endpoint.Hostname() == "" {
		return deploy.Connection{}, fmt.Errorf("the Spaces endpoint %q cannot be read: %v", d.options.SpacesEndpoint(spacesRegion(cfg.Region)), err)
	}
	conn := deploy.Connection{Host: endpoint.Hostname(), Port: 443, Database: bucket, User: key.ID, Password: secret, SSLMode: "require"}
	if endpoint.Scheme == "http" {
		conn.Port, conn.SSLMode = 80, "disable"
	}
	if p := endpoint.Port(); p != "" {
		if conn.Port, err = strconv.Atoi(p); err != nil {
			return deploy.Connection{}, fmt.Errorf("the Spaces endpoint's port %q: %w", p, err)
		}
	}
	return conn, nil
}

// TearDownStorage deletes the deployment's Spaces key and, when its data
// may go, its bucket, as tearDownStorage: the storage part of a teardown,
// as a step of its own (deploy.BucketData).
func (d *Driver) TearDownStorage(ctx context.Context, p deploy.Placement, td deploy.TakeDown) deploy.TakenDown {
	deleted, left := d.tearDownStorage(ctx, p, td)
	return deploy.TakenDown{Deleted: deleted, Left: left}
}

var _ deploy.BucketData = (*Driver)(nil)

// tearDownStorage deletes the deployment's Spaces key and, when its data
// may go (synthetic, or a final backup covers it), its bucket; a bucket
// that holds data no copy covers is kept and reported Left. Only what the
// owned list names is touched.
func (d *Driver) tearDownStorage(ctx context.Context, p deploy.Placement, td deploy.TakeDown) (deleted []string, left []deploy.Left) {
	var keys, buckets []deploy.Owned
	for _, o := range td.Owned {
		switch {
		case o.Kind == KindSpacesKey && o.Need == needS3:
			keys = append(keys, o)
		case o.Kind == KindBucket && o.Need == needS3:
			buckets = append(buckets, o)
		}
	}
	if len(keys)+len(buckets) == 0 {
		return nil, nil
	}
	c, cfg, err := d.Client(ctx)
	if err != nil {
		for _, o := range append(keys, buckets...) {
			left = append(left, storageLeft(o, "the account could not be reached: "+err.Error()))
		}
		return nil, left
	}
	swept, err := d.sweepMakers(ctx, c, p.Name, func(string, ...any) {})
	if err != nil {
		for _, o := range append(keys, buckets...) {
			left = append(left, storageLeft(o, "the account's keys could not be read: "+err.Error()))
		}
		return nil, left
	}
	left = append(left, swept...)
	for _, k := range keys {
		if err := d.deleteKey(ctx, c, k.ID); err != nil {
			left = append(left, storageLeft(k, "deleting it failed: "+err.Error()))
			continue
		}
		deleted = append(deleted, "Spaces key "+k.Name+" ("+k.ID+")")
		if d.Secrets != nil {
			if err := d.Secrets.Delete(context.WithoutCancel(ctx), SpacesSecretName); err != nil {
				deleted[len(deleted)-1] += "; its stored secret was not deleted: " + err.Error()
			}
		}
	}
	if len(buckets) == 0 {
		return deleted, left
	}
	if !td.Synthetic && !td.BackedUp {
		for _, b := range buckets {
			left = append(left, storageLeft(b, "kept: no final copy of its objects was taken"))
		}
		return deleted, left
	}
	region := spacesRegion(cfg.Region)
	makers, err := d.withMaker(ctx, c, region, p.Name, func(string, ...any) {}, func(m maker) error {
		for _, b := range buckets {
			if err := emptyAndDelete(ctx, m.client, b.ID); err != nil {
				left = append(left, storageLeft(b, "deleting it failed: "+err.Error()))
				continue
			}
			deleted = append(deleted, "bucket "+b.ID+" and its objects")
		}
		return nil
	})
	left = append(left, makers...)
	if err != nil {
		for _, b := range buckets {
			if !slices.ContainsFunc(deleted, func(d string) bool { return strings.HasPrefix(d, "bucket "+b.ID+" ") }) {
				left = append(left, storageLeft(b, "it could not be deleted: "+err.Error()))
			}
		}
	}
	return deleted, left
}

func emptyAndDelete(ctx context.Context, s3 *s3bucket.Client, bucket string) error {
	objects, err := s3.List(ctx, bucket)
	if s3bucket.IsCode(err, "NoSuchBucket") {
		return nil
	}
	if err != nil {
		return err
	}
	for _, o := range objects {
		if err := s3.DeleteObject(ctx, bucket, o.Key); err != nil {
			return err
		}
	}
	if err := s3.DeleteBucket(ctx, bucket); err != nil && !s3bucket.IsCode(err, "NoSuchBucket") {
		return err
	}
	return nil
}

func storageLeft(o deploy.Owned, why string) deploy.Left {
	what, kind := "Spaces access key", "spaces-key"
	if o.Kind == KindBucket {
		what, kind = "Spaces bucket", "bucket"
	}
	return deploy.Left{What: what, Handle: map[string]any{"name": o.ID, "kind": kind, "need": needS3}, Data: o.Data, Why: why}
}

// randomSuffix makes a bucket name unique across Spaces.
func randomSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
