package deployment

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/awsecs"
	"github.com/flokkit/apptown/internal/deploy/awsecs/awsfake"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Admission must check ECR before contacting the hub, including init images.
// The hub is reachable but unavailable, so consulting it fails the held case.
func TestAWSRetainedReleaseAdmissionDoesNotReadTheUnavailableHub(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := createTestProject(t, db, "shop", "Shop")
	projects := project.New(db, nil)
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects})
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer hub.Close()
	host := strings.TrimPrefix(hub.URL, "http://")
	if _, err := integrations.Bindings.Upsert(t.Context(), "registry", integration.CapContainerRegistry, "service", `{"url":"`+host+`"}`, false); err != nil {
		t.Fatal(err)
	}
	env := NewEnvironment(EnvironmentDependencies{Projects: projects, Integrations: integrations})
	dep := &Deployment{ID: idstest.N(901), ProjectID: pid}
	a := &awsfake.Account{}
	a.ECRListTagsForResourceFunc = func(context.Context, *ecr.ListTagsForResourceInput) (*ecr.ListTagsForResourceOutput, error) {
		return &ecr.ListTagsForResourceOutput{Tags: []ecrtypes.Tag{{Key: aws.String("apptown:instance"), Value: aws.String("installation")}, {Key: aws.String("apptown:deployment"), Value: aws.String(dep.ID.String())}}}, nil
	}
	d := awsecs.NewDriver(awsecs.Dependencies{Installation: "installation", Client: func(context.Context) (*awsecs.API, awsecs.Config, awsecs.Estate, error) {
		return a.API(), awsecs.Config{}, awsecs.Estate{}, nil
	}, Owned: func(context.Context) ([]deploy.Owned, error) {
		return []deploy.Owned{{Kind: "ecr-repository", ID: "repository", Name: "repository"}}, nil
	}})
	s := &Service{env: env, providers: &Providers{drivers: map[string]DriverFactory{KindAWSECS: func(context.Context, *Target, *Deployment) (deploy.Driver, error) { return d, nil }}}}
	repo := host + "/shop"
	b := &ReleaseBundle{ProjectID: pid, BuiltFrom: BundleOrigin{Registry: repo, Platform: awsecs.Platform}, Parts: map[string]string{"web": repo + "@sha256:" + strings.Repeat("a", 64)}, Init: map[string]string{"seed": repo + "@sha256:" + strings.Repeat("b", 64)}}
	for _, mode := range []string{"held", "missing", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			reads := 0
			a.BatchGetImageFunc = func(_ context.Context, in *ecr.BatchGetImageInput) (*ecr.BatchGetImageOutput, error) {
				reads++
				if mode == "unknown" {
					return nil, errors.New("ECR unavailable")
				}
				if mode == "missing" {
					return &ecr.BatchGetImageOutput{Failures: []ecrtypes.ImageFailure{{ImageId: &in.ImageIds[0], FailureCode: ecrtypes.ImageFailureCodeImageNotFound}}}, nil
				}
				return &ecr.BatchGetImageOutput{Images: []ecrtypes.Image{{ImageId: &in.ImageIds[0]}}}, nil
			}
			rebuild, err := s.releaseNeedsRebuild(t.Context(), b, &Target{Kind: KindAWSECS}, dep)
			if mode == "held" {
				if err != nil || rebuild || reads != 2 {
					t.Fatalf("retained release consulted hub: %v %v (%d ECR reads)", rebuild, err, reads)
				}
			} else if err == nil || rebuild {
				t.Fatalf("unavailable image admitted or rebuilt: %v %v", rebuild, err)
			}
		})
	}
}
