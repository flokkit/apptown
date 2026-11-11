package deployment

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/awsecs"
)

// Exercise the release door, with a provider callback that must remain unused.
// A driver-only refusal cannot protect need provisioning before render.
func TestAWSReleaseRefusesUnsupportedShapesAndAudienceBeforeProviderEffects(t *testing.T) {
	base := "app:\n  web:\n    public: true\n    build: worker\n    ports: ['8080']\n"
	for _, tc := range []struct{ name, yaml, audience, want string }{
		{"multipart", base + "  worker:\n    build: worker\n", "public", "single-part"},
		{"s3", base + "needs: {s3: {}}\n", "public", "cannot provide as a managed service"},
		{"redis", base + "needs: {redis: {}}\n", "public", "cannot provide as a managed service"},
		{"mariadb", base + "needs: {mariadb: {}}\n", "public", "cannot provide as a managed service"},
		{"directory", base + "    dataDir: /data\n", "public", "no persistent volumes"},
		{"team", base, "team", "team audience"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPromotionTargetsWith(t, false, tc.yaml)
			b := p.land(t)
			p.s.providers.aws = awsecs.Options{API: func(awsecs.Config, string, string) (*awsecs.API, error) {
				t.Fatal("refusal read AWS credentials or called provider")
				return nil, nil
			}}
			target, err := p.s.Targets.Put(t.Context(), &Target{Name: "aws", Kind: KindAWSECS, Provider: "aws", Status: TargetReady, NeedsMode: "managed", Capabilities: stockCapabilities(KindAWSECS, nil)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = p.s.Deployments.EnsureStanding(t.Context(), p.project, "aws-app", target.ID, Attributes{Feed: "manual", Audience: tc.audience, DataClass: "synthetic"}); err != nil {
				t.Fatal(err)
			}
			_, err = p.s.RequestRelease(context.Background(), DeploySelection{ProjectID: p.project, DeploymentName: "aws-app", BundleID: b.ID})
			if err == nil || !strings.Contains(err.Error(), tc.want) || (!errors.Is(err, ErrRefusedContract) && !errors.Is(err, ErrReleaseRefused)) {
				t.Fatalf("%s: %v", tc.name, err)
			}
		})
	}
}
