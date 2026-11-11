package deployment

import (
	"os"
	"strings"
	"testing"
)

// Every kind a target can be links to its setup guide, the product opens it
// at /docs/guides/<guide>, and the guide ships: a new kind is not done until
// its guide is written. An attached Docker host is the same kind as the
// built-in one and opens the same guide.
func TestEveryTargetKindHasASetupGuide(t *testing.T) {
	kinds := Kinds()
	if len(kinds) == 0 {
		t.Fatal("the kind catalog is empty")
	}
	for _, k := range kinds {
		if k.Guide == "" {
			t.Errorf("%s names no setup guide", k.Kind)
			continue
		}
		body, err := os.ReadFile("../../../stock/docs/guides/" + k.Guide + ".md")
		if err != nil {
			t.Errorf("%s: %v", k.Kind, err)
		} else if !strings.HasPrefix(string(body), "# ") {
			t.Errorf("%s: %s.md does not start with its title", k.Kind, k.Guide)
		}
	}
	attached := DescribeTarget(&Target{Kind: KindDockerHost, Origin: OriginAttached})
	if attached.Guide != describeKind(KindDockerHost).Guide {
		t.Errorf("an attached Docker host opens %q, not the Docker host guide", attached.Guide)
	}
}

// A kind whose address answers anyone, with no AppTown sign-in in front of
// it, releases only deployments for the public; a bucket website's files
// are public-read, so it is one of them. A Docker host keeps every audience.
func TestTargetsThatAnswerAnyoneRefuseNonPublicAudiences(t *testing.T) {
	for _, kind := range []string{KindS3Bucket, kindFlyMachines, KindAWSECS} {
		target := &Target{Name: "open", Kind: kind}
		for _, audience := range []string{"team", "organisation", "partners"} {
			if why := target.refusesAudience(&Deployment{Audience: audience}); !strings.Contains(why, "to anyone") {
				t.Errorf("%s accepts the %s audience: %q", kind, audience, why)
			}
		}
		if why := target.refusesAudience(&Deployment{Audience: "public"}); why != "" {
			t.Errorf("%s refuses a public deployment: %s", kind, why)
		}
	}
	if why := (&Target{Name: "host", Kind: KindDockerHost}).refusesAudience(&Deployment{Audience: "team"}); why != "" {
		t.Errorf("a Docker host refuses the team audience: %s", why)
	}
}
