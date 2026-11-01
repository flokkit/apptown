package s3bucket

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/staticsite"
)

// MarkInstallation writes the private object naming the installation that
// owns bucket (staticsite.InstanceKey). Spaces implements object but not
// bucket tagging, so the mark is an object; the website address does not
// serve a private one, and the publisher's clean-up keeps it. With no
// installation id there is nothing to write.
func MarkInstallation(ctx context.Context, c *Client, bucket, installation string) error {
	if installation == "" {
		return nil
	}
	return c.PutPrivate(ctx, bucket, staticsite.InstanceKey, "text/plain; charset=utf-8", []byte(installation))
}

// Marked reads which installation bucket's mark names: "" when the bucket
// carries none. A failed read is an error, never an absent mark.
func Marked(ctx context.Context, c *Client, bucket string) (string, error) {
	body, err := c.GetObject(ctx, bucket, staticsite.InstanceKey)
	if IsCode(err, "NoSuchKey") {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading %s's installation mark: %w", bucket, err)
	}
	return strings.TrimSpace(string(body)), nil
}

// Inventory answers what a bucket-website target's store holds: its
// buckets, as the store lists them. Only a bucket carrying this
// installation's mark can be not accounted for; any other (another
// installation's, one a deployment on another kind of target made, or one
// made before the mark) is reported only to confirm a record. Built for a
// target, it can look, never publish.
type Inventory struct {
	Client       func(context.Context) (*Client, Config, error)
	Installation string
}

var _ deploy.Inventorier = Inventory{}

// markReads bounds the marks asked for at once.
const markReads = 8

func (i Inventory) Inventory(ctx context.Context) ([]deploy.Resource, error) {
	c, cfg, err := i.Client(ctx)
	if err != nil {
		return nil, err
	}
	buckets, err := c.ListBuckets(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing buckets: %w", err)
	}
	out := make([]deploy.Resource, len(buckets))
	errs := make([]error, len(buckets))
	slots := make(chan struct{}, markReads)
	var wg sync.WaitGroup
	for n, bucket := range buckets {
		out[n] = deploy.Resource{Kind: "bucket", Name: bucket, Status: "present", Region: cfg.Region, Tags: []string{deploy.TagConfirmsOnly}}
		// Every bucket AppTown names begins apptown- (BucketName); no other
		// can carry the mark, so none other is asked.
		if i.Installation == "" || !strings.HasPrefix(bucket, "apptown-") {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			marked, err := Marked(ctx, c, bucket)
			if err != nil {
				errs[n] = err
				return
			}
			if marked == i.Installation {
				out[n].Tags, out[n].URL = nil, cfg.address(bucket)
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
