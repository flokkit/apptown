package deploy

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// A Portainer stack gives the app's own containers (its public service, its
// other parts, its init steps) the extra certificates' script, which
// Compose reads back exactly as written; a static site's file server is not
// given it.
func TestAnIntranetStackGivesTheAppsContainersTheExtraCertificates(t *testing.T) {
	t.Parallel()
	set, err := trust.New(t.TempDir(), map[trust.Use]trust.Bundle{trust.Deployments: trusttest.New(t, "Company CA").Bundle(t)}, trust.Bundle{})
	if err != nil {
		t.Fatal(err)
	}
	script := set.Script(false)
	read := func(st IntranetStack) map[string]map[string]string {
		st.Base, st.Hosts, st.Domain, st.Image, st.Port, st.PublicPart = "app", []string{"app.apps.example.com"}, "apps.example.com", "reg/app@sha256:x", 8080, "web"
		st.Secrets, st.Trust = noSecrets{}, script
		var stack struct {
			Services map[string]struct{ Environment map[string]string }
		}
		out := IntranetCompose(st)
		if err := yaml.Unmarshal([]byte(out), &stack); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		envs := map[string]map[string]string{}
		for name, svc := range stack.Services {
			envs[name] = svc.Environment
		}
		return envs
	}
	envs := read(IntranetStack{
		Init:  []appcontract.InitContainer{{Name: "migrate", Image: "reg/app@sha256:x"}},
		Parts: []appcontract.Part{{Name: "worker", Image: "reg/app@sha256:x"}},
	})
	for _, name := range []string{"app", "init-migrate", "worker"} {
		// Compose reads $$ as one $ (ComposeLiteral).
		if got := strings.ReplaceAll(envs[name][trust.EnvVar], "$$", "$"); got != script {
			t.Fatalf("%s is given %q, want the script", name, got)
		}
	}
	if static := read(IntranetStack{BrowserEnv: "{}"}); static["app"][trust.EnvVar] != "" {
		t.Fatal("a static site's file server was given the script")
	}
}
