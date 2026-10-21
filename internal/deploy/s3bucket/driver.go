// Package s3bucket is the s3-bucket target: a static-only app published
// to a bucket of an S3-compatible store (DigitalOcean Spaces first) and
// served from the bucket's website address
// (design/subsystems/deploy-targets.md, "Bucket websites"). Each standing deployment
// owns one bucket AppTown created; take-down empties and deletes it.
package s3bucket

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/staticsite"
	"flokkit.io/forge/internal/oci"
)

// Config is a target's store: where its API and website addresses are and
// which secrets hold its key pair, never the keys themselves.
type Config struct {
	Endpoint        string // https://syd1.digitaloceanspaces.com
	Region          string // syd1
	WebsiteTemplate string // https://{bucket}.{region}-static.digitaloceanspaces.com
	AccessKeyFrom   string
	SecretKeyFrom   string
}

// Address is a bucket's website address.
func (c Config) Address(bucket string) string {
	return strings.NewReplacer("{bucket}", bucket, "{region}", c.Region).Replace(c.WebsiteTemplate)
}

// Dependencies select one deployment's store, project and records.
type Dependencies struct {
	// Client resolves the key pair for one operation.
	Client func(context.Context) (*Client, Config, error)
	Slug   string // the project, for naming the bucket
	// Own records a bucket AppTown just created on the deployment, before
	// any other call touches it. Until it is recorded the bucket is not
	// the deployment's, and nothing is written to it.
	Own func(bucket string) error
	// ReleaseLogin reads the release's images from their registry.
	ReleaseLogin func() oci.Credential
	// BrowserValues are the project's browser-safe values, for env.js.
	BrowserValues func(context.Context) (map[string]string, error)
}

// Options are timings and naming; zero values are production's.
type Options struct {
	WitnessTries    int
	WitnessInterval time.Duration
	// Suffix makes a bucket name unique; random by default.
	Suffix func() string
}

// Driver is the s3-bucket target's driver.
type Driver struct {
	Dependencies
	options Options
}

func NewDriver(deps Dependencies, options Options) *Driver {
	if options.WitnessTries <= 0 {
		options.WitnessTries = 12
	}
	if options.WitnessInterval <= 0 {
		options.WitnessInterval = 5 * time.Second
	}
	if options.Suffix == nil {
		options.Suffix = func() string {
			b := make([]byte, 4)
			_, _ = rand.Read(b)
			return hex.EncodeToString(b)
		}
	}
	return &Driver{Dependencies: deps, options: options}
}

var (
	_ deploy.Driver   = (*Driver)(nil)
	_ deploy.Renderer = (*Driver)(nil)
)

// ArtifactKind is what the renderer produces: the published set's
// manifest.
const ArtifactKind = "static-site"

// manifest is the artifact's body: what the release publishes and from
// what. Browser values are browser-safe by contract; no secret is here.
type manifest struct {
	Image    string            `json:"image"`
	Release  string            `json:"release"`
	Identity string            `json:"identity"`
	Browser  map[string]string `json:"browser"`
	Keys     []string          `json:"keys"`
}

// Render reads the site's files out of the release image and makes the
// published set; its digest is the artifact's hash.
func (d *Driver) Render(ctx context.Context, p deploy.Placement, src deploy.Source, _ deploy.NeedBindings, _ deploy.ConsumerSecrets, logf deploy.Logf) (deploy.Artifact, error) {
	ref, release, err := releaseOf(src)
	if err != nil {
		return deploy.Artifact{}, err
	}
	browser := map[string]string{}
	if d.BrowserValues != nil {
		if browser, err = d.BrowserValues(ctx); err != nil {
			return deploy.Artifact{}, fmt.Errorf("reading the browser values: %w", err)
		}
	}
	set, err := d.publish(ctx, ref, release, browser)
	if err != nil {
		return deploy.Artifact{}, err
	}
	logf("the site is %d files from %s; this publication is %s", len(set.Files), ref, set.Identity)
	body, err := json.Marshal(manifest{Image: ref, Release: release, Identity: set.Identity, Browser: browser, Keys: set.Keys()})
	if err != nil {
		return deploy.Artifact{}, err
	}
	return deploy.Artifact{Kind: ArtifactKind, Body: body, Hash: set.Digest}, nil
}

// releaseOf is the public static part's image by digest and its release
// identity. Anything but a static-only contract with a digest is refused.
func releaseOf(src deploy.Source) (ref, release string, err error) {
	c := src.Contract
	if c == nil || len(c.Parts) != 1 || !c.Parts[0].Public || !c.Parts[0].IsStatic() || len(c.Init) > 0 || len(c.Needs) > 0 {
		return "", "", deploy.Terminal(errors.New("an s3-bucket target serves only a static-only app: one public static part, no init steps, no needs"))
	}
	ref = src.Images[c.Parts[0].Name]
	if !strings.Contains(ref, "@sha256:") {
		return "", "", deploy.Terminal(fmt.Errorf("part %s has no image by digest in this release (%q); release it from an AppTown with a worker", c.Parts[0].Name, ref))
	}
	if release = c.PublicRelease(src.Revision); release == "" {
		return "", "", deploy.Terminal(errors.New("the release names no revision, so its publication has no identity"))
	}
	return ref, release, nil
}

func (d *Driver) publish(ctx context.Context, ref, release string, browser map[string]string) (staticsite.Set, error) {
	from := oci.Source{Reference: ref}
	if d.ReleaseLogin != nil {
		from.Credential = d.ReleaseLogin()
	}
	files, err := oci.ReadTree(ctx, from, staticsite.Root, staticsite.MaxBytes)
	if err != nil {
		if errors.Is(err, oci.ErrUnavailable) {
			return staticsite.Set{}, fmt.Errorf("reading the site's files: %w", err)
		}
		return staticsite.Set{}, deploy.Terminal(fmt.Errorf("reading the site's files: %w", err))
	}
	_, digest, _ := strings.Cut(ref, "@")
	set, err := staticsite.Publish(files, release, digest, browser)
	if err != nil {
		return staticsite.Set{}, deploy.Terminal(err)
	}
	return set, nil
}

// Reconcile publishes the set: the deployment's bucket (made and recorded
// on its first release), its website configuration, assets, then entry
// files, then the marker, then the witness; old files are removed one
// release late, after the witness.
func (d *Driver) Reconcile(ctx context.Context, p deploy.Placement, generation int64, art deploy.Artifact, _ string, logf func(string, ...any), checkpoint deploy.Checkpoint) (deploy.Observation, error) {
	var m manifest
	if err := json.Unmarshal(art.Body, &m); err != nil || art.Kind != ArtifactKind {
		return deploy.Observation{}, deploy.Terminal(fmt.Errorf("the artifact is not a published site (%s): %w", art.Kind, err))
	}
	// The set is read again from the image; it must be the one rendered.
	set, err := d.publish(ctx, m.Image, m.Release, m.Browser)
	if err != nil {
		return deploy.Observation{}, err
	}
	if set.Digest != art.Hash {
		return deploy.Observation{}, deploy.Terminal(fmt.Errorf("the site read from %s is %s, not the rendered %s", m.Image, set.Digest, art.Hash))
	}
	c, cfg, err := d.Client(ctx)
	if err != nil {
		return deploy.Observation{}, err
	}
	bucket, _ := p.Handle["bucket"].(string)
	if bucket == "" {
		if bucket, err = d.create(ctx, c, p.Name, logf); err != nil {
			return deploy.Observation{}, err
		}
	}
	address := cfg.Address(bucket)
	if err := checkpoint(map[string]any{"bucket": bucket, "address": address, "identity": set.Identity, "keys": set.Keys()}); err != nil {
		return deploy.Observation{}, deploy.Terminal(fmt.Errorf("recording the publication before uploading: %w", err))
	}
	if err := c.PutWebsite(ctx, bucket); err != nil {
		return deploy.Observation{}, refused("setting the website configuration of "+bucket, err)
	}
	for _, pass := range []staticsite.Pass{staticsite.Assets, staticsite.Entries, staticsite.Marker} {
		n := 0
		for _, f := range set.Files {
			if f.Pass != pass {
				continue
			}
			if err := c.PutObject(ctx, bucket, f.Key, f.ContentType, f.NoStore(), f.Body); err != nil {
				if pass == staticsite.Marker {
					// The marker may have been written: whether the address
					// answers it is for the witness to find out.
					return deploy.Observation{}, fmt.Errorf("uploading the marker to %s: %w; whether it was written is unknown", bucket, err)
				}
				return deploy.Observation{}, deploy.Terminal(fmt.Errorf("uploading %s to %s: %v — this publication is incomplete: some of its files are uploaded and the marker is not, so %s still answers the previous one; release again", f.Key, bucket, err, address))
			}
			n++
		}
		logf("uploaded %d %s to %s", n, passWords[pass], bucket)
	}
	if err := staticsite.Witness(ctx, address, set.Identity, d.options.WitnessTries, d.options.WitnessInterval, logf); err != nil {
		return deploy.Observation{}, err
	}
	d.clean(ctx, c, bucket, set.Keys(), keysOf(p.Handle["keys"]), logf)
	return observed(generation, bucket, address, set.Identity, set.Keys()), nil
}

var passWords = map[staticsite.Pass]string{staticsite.Assets: "asset files", staticsite.Entries: "entry files", staticsite.Marker: "marker"}

func observed(generation int64, bucket, address, identity string, keys []string) deploy.Observation {
	return deploy.Observation{Generation: generation, Rollout: "active", Health: "healthy", URL: address,
		Handle: map[string]any{"bucket": bucket, "address": address, "identity": identity, "keys": keys}}
}

// create makes the deployment's bucket and records it before anything
// else touches it. A taken name is refused, never adopted; a create whose
// outcome is unknown is left for a person.
func (d *Driver) create(ctx context.Context, c *Client, deployment string, logf func(string, ...any)) (string, error) {
	bucket := BucketName(d.Slug, deployment, d.options.Suffix())
	err := c.CreateBucket(ctx, bucket)
	var refusal *Error
	switch {
	case errors.As(err, &refusal) && refusal.Status == 409:
		return "", deploy.Terminal(fmt.Errorf("the bucket name %s is taken (%s); AppTown never adopts a bucket it did not create — release again for another name", bucket, refusal.Code))
	case errors.As(err, &refusal):
		return "", deploy.Terminal(fmt.Errorf("creating the bucket %s: %w", bucket, err))
	case err != nil:
		return "", fmt.Errorf("creating the bucket %s: %w — whether it was created is unknown; look for it in the store and delete it if it is there", bucket, err)
	}
	if err := d.Own(bucket); err != nil {
		return "", deploy.Terminal(fmt.Errorf("the bucket %s was created but could not be recorded on the deployment (%w); nothing was written to it — delete it in the store", bucket, err))
	}
	logf("created the bucket %s for this deployment; taking the deployment down deletes it", bucket)
	return bucket, nil
}

// clean removes, one release late, every object in neither this set nor
// the previous one. Listing is safe: the bucket is the deployment's, and
// only AppTown writes there. A failure leaves files until the next
// release's clean-up; the release stands.
func (d *Driver) clean(ctx context.Context, c *Client, bucket string, current, previous []string, logf func(string, ...any)) {
	keep := map[string]bool{}
	for _, k := range append(current, previous...) {
		keep[k] = true
	}
	objects, err := c.List(ctx, bucket)
	if err != nil {
		logf("old files were not removed (listing %s: %v); the next release removes them", bucket, err)
		return
	}
	removed := 0
	for _, o := range objects {
		if keep[o.Key] {
			continue
		}
		if err := c.DeleteObject(ctx, bucket, o.Key); err != nil {
			logf("old files were not all removed (%s: %v); the next release removes them", o.Key, err)
			return
		}
		removed++
	}
	if removed > 0 {
		logf("removed %d files no longer in this or the previous publication", removed)
	}
}

func keysOf(v any) []string {
	var out []string
	switch ks := v.(type) {
	case []string:
		out = ks
	case []any:
		for _, k := range ks {
			if s, ok := k.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func refused(what string, err error) error {
	var refusal *Error
	if errors.As(err, &refusal) {
		return deploy.Terminal(fmt.Errorf("%s: %w", what, err))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// Resume only observes: it witnesses the recorded publication at its
// address and never uploads, changes or deletes anything. A publication
// interrupted before its marker stays unwitnessed; the ordinary release
// path publishes again.
func (d *Driver) Resume(ctx context.Context, _ deploy.Placement, generation int64, noted map[string]any, logf func(string, ...any)) (deploy.Observation, error) {
	bucket, _ := noted["bucket"].(string)
	address, _ := noted["address"].(string)
	identity, _ := noted["identity"].(string)
	if bucket == "" || address == "" || identity == "" {
		return deploy.Observation{}, errors.New("AppTown restarted before it recorded which publication it was making; release again")
	}
	if err := staticsite.Witness(ctx, address, identity, d.options.WitnessTries, d.options.WitnessInterval, logf); err != nil {
		return deploy.Observation{}, err
	}
	return observed(generation, bucket, address, identity, keysOf(noted["keys"])), nil
}

// Observe asks the address for its marker: this publication is healthy,
// any other answer unhealthy, no answer an error (unknown).
func (d *Driver) Observe(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
	address, _ := p.Handle["address"].(string)
	if address == "" {
		address = p.URL
	}
	if address == "" {
		return deploy.Observation{Generation: p.Generation, Rollout: "absent", Health: "unknown"}, nil
	}
	identity, _ := p.Handle["identity"].(string)
	status, body, err := staticsite.Answer(ctx, address)
	if err != nil {
		return deploy.Observation{}, err
	}
	o := deploy.Observation{Generation: p.Generation, Rollout: "active", Health: "unhealthy", URL: address, Handle: p.Handle}
	if status == 200 && identity != "" && body == identity {
		o.Health = "healthy"
	}
	return o, nil
}

// Teardown deletes the deployment's bucket: every object in it, then the
// bucket, then witnesses that the address answers 404. Whatever is left,
// or a withdrawal it could not see, is named, with the site maybe public.
// The bucket is the deployment's because its handle records it, which only
// AppTown's own create does (Dependencies.Own): that record is the
// deployment's list for this target, so td.Owned is not read. A static
// site holds no data, so there is nothing to back up.
func (d *Driver) Teardown(ctx context.Context, p deploy.Placement, generation int64, _ deploy.TakeDown) (deploy.TakenDown, error) {
	var out deploy.TakenDown
	bucket, _ := p.Handle["bucket"].(string)
	if bucket == "" {
		return out, nil // no release made a bucket
	}
	if err := d.deleteBucket(ctx, bucket); err != nil {
		return out, err
	}
	out.Deleted = append(out.Deleted, "bucket "+bucket)
	return out, nil
}

func (d *Driver) deleteBucket(ctx context.Context, bucket string) error {
	c, cfg, err := d.Client(ctx)
	if err != nil {
		return err
	}
	address := cfg.Address(bucket)
	stillPublic := func(what string, err error) error {
		return fmt.Errorf("the site is still public at %s: %s (%w)", address, what, err)
	}
	objects, err := c.List(ctx, bucket)
	switch {
	case IsCode(err, "NoSuchBucket"):
	case err != nil:
		return stillPublic("the bucket "+bucket+" could not be listed", err)
	default:
		for i, o := range objects {
			if err := c.DeleteObject(ctx, bucket, o.Key); err != nil {
				return stillPublic(fmt.Sprintf("%d of %d files remain in the bucket %s", len(objects)-i, len(objects), bucket), err)
			}
		}
		if err := c.DeleteBucket(ctx, bucket); err != nil && !IsCode(err, "NoSuchBucket") {
			return fmt.Errorf("the bucket %s is empty but was not deleted (%w); delete it in the store", bucket, err)
		}
	}
	if err := staticsite.Gone(ctx, address, d.options.WitnessTries, d.options.WitnessInterval); err != nil {
		return fmt.Errorf("the bucket %s is deleted, but the site may still be public: %w", bucket, err)
	}
	return nil
}

func (d *Driver) Controls() deploy.Controls { return deploy.Controls{Liveness: true, Rollout: true} }

var nameChars = regexp.MustCompile(`[^a-z0-9-]+`)

// BucketName is a deployment's bucket: forge-<project>-<deployment>-<suffix>,
// lowercase, DNS-valid and at most 63 characters.
func BucketName(slug, deployment, suffix string) string {
	clean := func(s string, n int) string {
		s = strings.Trim(nameChars.ReplaceAllString(strings.ToLower(s), "-"), "-")
		if len(s) > n {
			s = strings.TrimRight(s[:n], "-")
		}
		return s
	}
	return "apptown-" + clean(slug, 28) + "-" + clean(deployment, 16) + "-" + clean(suffix, 8)
}
