package deployment

import (
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestTargetRefusesContract(t *testing.T) {
	keeps := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true, DataDir: "/data"}}}
	needs := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true}}, Needs: []string{"postgres", "redis"}}
	host := &Target{Name: "box", Kind: KindDockerHost, Capabilities: TargetCapabilities{PersistentVolumes: true}, NeedsMode: "emulators"}
	cloud := &Target{Name: "cloud", Kind: KindDOApp, Capabilities: TargetCapabilities{Needs: []string{"postgres"}}, NeedsMode: "managed"}
	if why := host.RefusesContract(keeps); why != "" {
		t.Fatalf("a host with volumes takes a data directory: %s", why)
	}
	if why := cloud.RefusesContract(keeps); !strings.Contains(why, "persistent volumes") {
		t.Fatalf("a hostless target refuses a data directory with the ruling: %q", why)
	}
	if why := host.RefusesContract(needs); why != "" {
		t.Fatalf("emulated needs are not judged against the target's managed list: %s", why)
	}
	if why := cloud.RefusesContract(needs); !strings.Contains(why, "needs redis") {
		t.Fatalf("a managed-needs target refuses a need it cannot fulfil: %q", why)
	}
}

func TestDeployableByHand(t *testing.T) {
	if err := (&Deployment{Kind: Preview, Feed: "landing"}).DeployableByHand(); !errors.Is(err, ErrPreviewNotDeployed) {
		t.Fatalf("a preview follows its change: %v", err)
	}
	if err := (&Deployment{Kind: Standing, Name: "main", Feed: "landing"}).DeployableByHand(); !errors.Is(err, ErrFedByLanding) || !strings.HasPrefix(err.Error(), "main is") {
		t.Fatalf("a landing-fed deployment is deployed by landing: %v", err)
	}
	if err := (&Deployment{Kind: Standing, Name: "staging", Feed: "promotion"}).DeployableByHand(); err != nil {
		t.Fatalf("a promotion-fed deployment takes a deploy by hand: %v", err)
	}
}

// A landing-fed deployment takes by hand only the bundle it is known to run:
// another bundle, or software without recorded provenance, remains landing's.
func TestDeployableByHandAtTheBundleItRuns(t *testing.T) {
	main := &Deployment{Kind: Standing, Name: "main", Feed: "landing"}
	if err := main.DeployableByHandAt(idstest.N(5), idstest.N(5)); err != nil {
		t.Fatalf("what runs may be deployed again: %v", err)
	}
	for _, c := range [][2]ids.ID{{idstest.N(6), idstest.N(5)}, {idstest.N(5), ids.Nil}, {ids.Nil, ids.Nil}} {
		if err := main.DeployableByHandAt(c[0], c[1]); !errors.Is(err, ErrFedByLanding) {
			t.Fatalf("bundle %s with %s running is landing's: %v", c[0], c[1], err)
		}
	}
	if err := (&Deployment{Kind: Preview, Feed: "landing"}).DeployableByHandAt(idstest.N(5), idstest.N(5)); !errors.Is(err, ErrPreviewNotDeployed) {
		t.Fatalf("a preview follows its change, whatever it runs: %v", err)
	}
}

func TestRemovable(t *testing.T) {
	if err := (&Deployment{Name: "main"}).Removable(); !errors.Is(err, ErrMainNotRemoved) {
		t.Fatalf("main is never removed by hand: %v", err)
	}
	if err := (&Deployment{Name: "staging"}).Removable(); err != nil {
		t.Fatalf("another deployment may be torn down: %v", err)
	}
}

func TestTargetsRefuseUnexecutedHealthPolicyBeforePreparation(t *testing.T) {
	for _, kind := range []string{KindDockerHost, KindPortainer, KindS3Bucket} {
		c := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true, Static: "site", Intent: &appcontract.Health{Readiness: &appcontract.Probe{HTTP: "/ready", Period: "5s"}}}}}
		target := &Target{Kind: kind, Capabilities: TargetCapabilities{PersistentVolumes: true}}
		if kind == KindS3Bucket {
			if target.RefusesContract(c) == "" {
				t.Fatal("bucket accepted ignored runtime policy")
			}
			continue
		}
		if why := target.RefusesContract(c); why != "" {
			t.Fatalf("%s valid public policy: %s", kind, why)
		}
		c.Parts = append(c.Parts, appcontract.Part{Name: "worker", Intent: &appcontract.Health{Readiness: &appcontract.Probe{HTTP: "/ready", Period: "5s"}}})
		if target.RefusesContract(c) == "" {
			t.Fatalf("%s accepted unexecuted worker readiness", kind)
		}
	}
}
