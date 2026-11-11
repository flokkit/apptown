package awsecs_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/awsecs"
	"github.com/flokkit/apptown/internal/deploy/awsecs/awsfake"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func imageFixture(t *testing.T) (*awsecs.Driver, *awsfake.Account, deploy.Placement) {
	t.Helper()
	p := deploy.Placement{ID: idstest.N(91)}
	a := &awsfake.Account{}
	a.ECRListTagsForResourceFunc = func(_ context.Context, in *ecr.ListTagsForResourceInput) (*ecr.ListTagsForResourceOutput, error) {
		if aws.ToString(in.ResourceArn) != "arn:repository" {
			t.Fatalf("ownership check: %+v", in)
		}
		return &ecr.ListTagsForResourceOutput{Tags: []ecrtypes.Tag{{Key: aws.String("apptown:instance"), Value: aws.String("installation")}, {Key: aws.String("apptown:deployment"), Value: aws.String(p.ID.String())}}}, nil
	}
	d := awsecs.NewDriver(awsecs.Dependencies{Installation: "installation", Client: func(context.Context) (*awsecs.API, awsecs.Config, awsecs.Estate, error) {
		return a.API(), awsecs.Config{Account: "123456789012", Region: "ap-southeast-2"}, awsecs.Estate{}, nil
	}, Owned: func(context.Context) ([]deploy.Owned, error) {
		return []deploy.Owned{{Kind: "ecr-repository", ID: "arn:repository", Name: "apptown/instance/deployment"}}, nil
	}})
	return d, a, p
}

func imageRef(char string) string { return "hub.invalid/project@sha256:" + strings.Repeat(char, 64) }
func digestOf(ref string) string  { _, digest, _ := strings.Cut(ref, "@"); return digest }
func held(in *ecr.BatchGetImageInput) *ecr.BatchGetImageOutput {
	return &ecr.BatchGetImageOutput{Images: []ecrtypes.Image{{ImageId: &in.ImageIds[0]}}}
}

func TestECRRetainedCopiesAreCheckedWithoutTheHub(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"held", "missing", "refused", "unreadable", "wrong digest", "wrong owner", "no repository"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			d, a, p := imageFixture(t)
			if mode == "no repository" {
				d.Owned = func(context.Context) ([]deploy.Owned, error) { return nil, nil }
			}
			if mode == "wrong owner" {
				a.ECRListTagsForResourceFunc = func(context.Context, *ecr.ListTagsForResourceInput) (*ecr.ListTagsForResourceOutput, error) {
					return &ecr.ListTagsForResourceOutput{}, nil
				}
			}
			reads := 0
			a.BatchGetImageFunc = func(_ context.Context, in *ecr.BatchGetImageInput) (*ecr.BatchGetImageOutput, error) {
				reads++
				if aws.ToString(in.RepositoryName) != "apptown/instance/deployment" || len(in.ImageIds) != 1 || aws.ToString(in.ImageIds[0].ImageTag) != "" {
					t.Fatalf("presence: %+v", in)
				}
				switch mode {
				case "missing":
					return &ecr.BatchGetImageOutput{Failures: []ecrtypes.ImageFailure{{ImageId: &in.ImageIds[0], FailureCode: ecrtypes.ImageFailureCodeImageNotFound}}}, nil
				case "refused":
					return nil, errors.New("access denied")
				case "unreadable":
					return &ecr.BatchGetImageOutput{}, nil
				case "wrong digest":
					return &ecr.BatchGetImageOutput{Images: []ecrtypes.Image{{ImageId: &ecrtypes.ImageIdentifier{ImageDigest: aws.String("sha256:wrong")}}}}, nil
				}
				return held(in), nil
			}
			presence, ok := any(d).(deploy.ImagePresence)
			if !ok {
				t.Fatal("ECR has no retained-copy presence check")
			}
			got, err := presence.HasImages(t.Context(), p, []string{imageRef("a"), imageRef("b")})
			switch mode {
			case "held":
				if !got || err != nil || reads != 2 {
					t.Fatalf("held copies: %v %v (%d reads)", got, err, reads)
				}
			case "missing", "no repository":
				if got || err != nil {
					t.Fatalf("missing: %v %v", got, err)
				}
			default:
				if got || err == nil {
					t.Fatalf("unknown became missing: %v %v", got, err)
				}
			}
		})
	}
}

func TestECRRetentionDeletesOnlyRecordedExpiredDigestsAfterAllReads(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"deleted", "read fails", "malformed read", "delete refused", "delete failure", "malformed delete", "already gone", "wrong owner"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			d, a, p := imageFixture(t)
			if mode == "wrong owner" {
				a.ECRListTagsForResourceFunc = func(context.Context, *ecr.ListTagsForResourceInput) (*ecr.ListTagsForResourceOutput, error) {
					return &ecr.ListTagsForResourceOutput{}, nil
				}
			}
			var reads, deletes []string
			a.BatchGetImageFunc = func(_ context.Context, in *ecr.BatchGetImageInput) (*ecr.BatchGetImageOutput, error) {
				digest := aws.ToString(in.ImageIds[0].ImageDigest)
				reads = append(reads, digest)
				if digest == digestOf(imageRef("b")) {
					if mode == "read fails" {
						return nil, errors.New("unreachable")
					}
					if mode == "malformed read" {
						return &ecr.BatchGetImageOutput{}, nil
					}
				}
				return held(in), nil
			}
			a.BatchDeleteImageFunc = func(_ context.Context, in *ecr.BatchDeleteImageInput) (*ecr.BatchDeleteImageOutput, error) {
				if len(reads) != 2 {
					t.Fatalf("deleted before all reads: %v", reads)
				}
				if aws.ToString(in.RepositoryName) != "apptown/instance/deployment" || len(in.ImageIds) != 1 || aws.ToString(in.ImageIds[0].ImageTag) != "" {
					t.Fatalf("delete: %+v", in)
				}
				digest := aws.ToString(in.ImageIds[0].ImageDigest)
				deletes = append(deletes, digest)
				switch mode {
				case "delete refused":
					return nil, errors.New("access denied")
				case "delete failure":
					return &ecr.BatchDeleteImageOutput{Failures: []ecrtypes.ImageFailure{{ImageId: &in.ImageIds[0], FailureCode: ecrtypes.ImageFailureCodeKmsError, FailureReason: aws.String("key unavailable")}}}, nil
				case "malformed delete":
					return &ecr.BatchDeleteImageOutput{}, nil
				case "already gone":
					return &ecr.BatchDeleteImageOutput{Failures: []ecrtypes.ImageFailure{{ImageId: &in.ImageIds[0], FailureCode: ecrtypes.ImageFailureCodeImageNotFound}}}, nil
				}
				return &ecr.BatchDeleteImageOutput{ImageIds: in.ImageIds}, nil
			}
			store, ok := any(d).(deploy.ImageStore)
			if !ok {
				t.Fatal("ECR has no image retention adapter")
			}
			// c is shared with a kept bundle; d is unrecorded and must never be read or deleted.
			deleted, left, err := store.TrimImages(t.Context(), p, []string{imageRef("a"), imageRef("b"), imageRef("c"), imageRef("a")}, []string{imageRef("c")})
			switch mode {
			case "read fails", "malformed read", "wrong owner":
				if err == nil || len(deletes) != 0 || len(deleted) != 0 {
					t.Fatalf("unknown inventory deleted: %v %v %v", deletes, deleted, err)
				}
			default:
				if err != nil || !slices.Equal(deletes, []string{digestOf(imageRef("a")), digestOf(imageRef("b"))}) {
					t.Fatalf("wrong candidates: %v %v", deletes, err)
				}
				if mode == "deleted" || mode == "already gone" {
					if len(deleted) != 2 || len(left) != 0 || !strings.HasPrefix(deleted[0], "123456789012.dkr.ecr.ap-southeast-2.amazonaws.com/apptown/instance/deployment@sha256:") {
						t.Fatalf("deleted: %v left: %v", deleted, left)
					}
				} else if len(deleted) != 0 || len(left) != 2 || left[0].Why == "" {
					t.Fatalf("refusal reported success: %v %v", deleted, left)
				}
			}
		})
	}
}

func TestECRStoreIdentitySeparatesDeployments(t *testing.T) {
	t.Parallel()
	d, _, p := imageFixture(t)
	store, ok := any(d).(deploy.ImageStore)
	if !ok {
		t.Fatal("ECR has no image retention adapter")
	}
	first, err := store.ImageStore(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	p.ID = idstest.Parse(t, "00000000-0000-7000-8000-000000000092")
	second, err := store.ImageStore(t.Context(), p)
	if err != nil || first == second || !strings.HasPrefix(first, "123456789012.dkr.ecr.ap-southeast-2.amazonaws.com/apptown/") {
		t.Fatalf("store identity: %q %q %v", first, second, err)
	}
}

func TestECRStoreIdentitySeparatesAccountsAndRegions(t *testing.T) {
	t.Parallel()
	var stores []string
	for _, c := range []awsecs.Config{{Account: "123456789012", Region: "ap-southeast-2"}, {Account: "999999999999", Region: "ap-southeast-2"}, {Account: "123456789012", Region: "us-east-1"}} {
		d, a, p := imageFixture(t)
		d.Client = func(context.Context) (*awsecs.API, awsecs.Config, awsecs.Estate, error) {
			return a.API(), c, awsecs.Estate{}, nil
		}
		store, ok := any(d).(deploy.ImageStore)
		if !ok {
			t.Fatal("ECR has no image retention adapter")
		}
		key, err := store.ImageStore(t.Context(), p)
		if err != nil || slices.Contains(stores, key) {
			t.Fatalf("shared foreign ECR store: %q %v", key, err)
		}
		stores = append(stores, key)
	}
}
