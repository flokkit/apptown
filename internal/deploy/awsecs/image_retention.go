package awsecs

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/google/go-containerregistry/pkg/name"
)

var _ deploy.ImageStore = (*Driver)(nil)
var _ deploy.ImagePresence = (*Driver)(nil)

func (d *Driver) ImageStore(ctx context.Context, p deploy.Placement) (string, error) {
	_, c, _, err := d.Client(ctx)
	if err != nil {
		return "", err
	}
	return c.Account + ".dkr.ecr." + c.Region + ".amazonaws.com/apptown/" + installationPart(d.Installation) + "/" + deploy.HostKey(p.ID), nil
}

// Check recorded ownership before reading or deleting the deployment's copies.
func (d *Driver) imageRepository(ctx context.Context, a *API, c Config, p deploy.Placement) (string, error) {
	o, ok, err := d.recorded(ctx, "ecr-repository")
	if err != nil || !ok {
		return "", err
	}
	live, err := d.checkOwned(ctx, a, c, p, o)
	if err != nil || !live {
		return "", err
	}
	if o.Name == "" {
		return "", fmt.Errorf("AWS recorded image repository has no name")
	}
	return o.Name, nil
}

func recordedDigest(ref string) (string, error) {
	r, err := name.NewDigest(ref, name.StrictValidation)
	if err != nil {
		return "", fmt.Errorf("retention requires an image digest: %w", err)
	}
	return r.DigestStr(), nil
}

// A digest-specific read avoids listing or deleting images absent from bundles.
// Every answer must identify the requested digest; unknown is never missing.
func heldImage(ctx context.Context, a *API, repo, digest string) (bool, error) {
	out, err := a.ECR.BatchGetImage(ctx, &ecr.BatchGetImageInput{RepositoryName: aws.String(repo), ImageIds: []ecrtypes.ImageIdentifier{{ImageDigest: aws.String(digest)}}})
	if missing(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if out != nil {
		if len(out.Images) == 1 && len(out.Failures) == 0 && out.Images[0].ImageId != nil && aws.ToString(out.Images[0].ImageId.ImageDigest) == digest {
			return true, nil
		}
		if len(out.Images) == 0 && len(out.Failures) == 1 && out.Failures[0].ImageId != nil && aws.ToString(out.Failures[0].ImageId.ImageDigest) == digest {
			f := out.Failures[0]
			if f.FailureCode == ecrtypes.ImageFailureCodeImageNotFound {
				return false, nil
			}
			return false, fmt.Errorf("ECR image %s: %s: %s", digest, f.FailureCode, aws.ToString(f.FailureReason))
		}
	}
	return false, fmt.Errorf("ECR returned an unreadable answer for image %s", digest)
}

func (d *Driver) HasImages(ctx context.Context, p deploy.Placement, refs []string) (bool, error) {
	a, c, _, err := d.Client(ctx)
	if err != nil {
		return false, err
	}
	repo, err := d.imageRepository(ctx, a, c, p)
	if err != nil || repo == "" {
		return false, err
	}
	for _, ref := range refs {
		digest, err := recordedDigest(ref)
		if err != nil {
			return false, err
		}
		held, err := heldImage(ctx, a, repo, digest)
		if err != nil || !held {
			return false, err
		}
	}
	return true, nil
}

func (d *Driver) TrimImages(ctx context.Context, p deploy.Placement, expired, keep []string) ([]string, []deploy.Left, error) {
	a, c, _, err := d.Client(ctx)
	if err != nil {
		return nil, nil, err
	}
	repo, err := d.imageRepository(ctx, a, c, p)
	if err != nil || repo == "" {
		return nil, nil, err
	}
	protected := map[string]bool{}
	for _, ref := range keep {
		digest, err := recordedDigest(ref)
		if err != nil {
			return nil, nil, err
		}
		protected[digest] = true
	}
	var candidates []string
	for _, ref := range expired {
		digest, err := recordedDigest(ref)
		if err != nil {
			return nil, nil, err
		}
		if protected[digest] || slices.Contains(candidates, digest) {
			continue
		}
		held, err := heldImage(ctx, a, repo, digest)
		if err != nil {
			return nil, nil, err
		}
		if held {
			candidates = append(candidates, digest)
		}
	}
	var deleted []string
	var left []deploy.Left
	for _, digest := range candidates {
		ref := c.Account + ".dkr.ecr." + c.Region + ".amazonaws.com/" + repo + "@" + digest
		out, err := a.ECR.BatchDeleteImage(ctx, &ecr.BatchDeleteImageInput{RepositoryName: aws.String(repo), ImageIds: []ecrtypes.ImageIdentifier{{ImageDigest: aws.String(digest)}}})
		why := ""
		switch {
		case err != nil:
			why = err.Error()
		case out == nil:
			why = "ECR returned no deletion result"
		case len(out.Failures) > 0:
			f := out.Failures[0]
			if len(out.Failures) == 1 && len(out.ImageIds) == 0 && f.ImageId != nil && aws.ToString(f.ImageId.ImageDigest) == digest && f.FailureCode == ecrtypes.ImageFailureCodeImageNotFound {
				break
			}
			why = fmt.Sprintf("ECR refused image deletion: %s: %s", f.FailureCode, aws.ToString(f.FailureReason))
		case len(out.ImageIds) != 1 || aws.ToString(out.ImageIds[0].ImageDigest) != digest || aws.ToString(out.ImageIds[0].ImageTag) != "":
			why = "ECR returned an unreadable deletion result"
		}
		if why != "" {
			left = append(left, deploy.Left{What: ref, Why: strings.TrimSpace(why)})
		} else {
			deleted = append(deleted, ref)
		}
	}
	return deleted, left, nil
}
