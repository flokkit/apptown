package deployment

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

func TestPortainerOwnerRefusesNeedSecretCollisionsBeforeCompose(t *testing.T) {
	p := newPromotionTargets(t, false)
	dep, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, "collision", p.target(t, IntranetTargetName).ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := appcontract.Parse([]byte("app:\n  web: {image: 'app:1', ports: ['8080'], public: true}\n  worker: {image: 'app:1'}\ninit:\n  - {name: migrate, image: 'app:1'}\nneeds: {postgres: {}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, consumer := range []string{"web", "worker", "init-migrate"} {
		for _, key := range []string{"DATABASE_URL", "PGHOST"} {
			in := portainerStackOf{Base: "owned", Prefix: "apptown-", Contract: c, Project: p.project, Deployment: dep.ID, Images: stackImages{Parts: map[string]string{"web": "reg/app@sha256:abc", "worker": "reg/app@sha256:abc"}, Init: map[string]string{"migrate": "reg/app@sha256:abc"}}, Secrets: consumerEnv{consumer: {key: "manual-secret-sentinel"}}}
			body, err := p.s.env.portainerStack(t.Context(), in, func(string, ...any) {})
			if err == nil || body != "" || !strings.Contains(err.Error(), consumer) || !strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "sentinel") {
				t.Fatalf("ambiguous %s/%s compose: %q %v", consumer, key, body, err)
			}
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.stacks) != 0 {
		t.Fatal("ambiguous configuration submitted to provider")
	}
}
