package deployment

import (
	"context"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/stock"
)

// Every stock need reaches Portainer as its catalogue starts it on a Docker
// host, with a deployment's generated credentials: Compose reads back the
// same entrypoint (a start script's quotes, backticks and shell variables
// among them), command, health check and environment.
func TestEveryStockNeedReachesPortainerAsItsCatalogueStartsIt(t *testing.T) {
	t.Parallel()
	files, err := fs.Glob(stock.FS, "dependencies/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	creds := func(_ context.Context, capability, name string) (string, error) {
		return capability + "-" + name + "-0f3a9c", nil
	}
	for _, f := range files {
		raw, err := fs.ReadFile(stock.FS, f)
		if err != nil {
			t.Fatal(err)
		}
		k, err := dependency.Parse(string(raw))
		if err != nil || k.Local.Image == "" {
			continue // a registered need runs nothing
		}
		generated, err := k.Generated(t.Context(), creds)
		if err != nil {
			t.Fatal(err)
		}
		s := k.ResolveLocal(nil, "", generated)
		out := deploy.IntranetCompose(deploy.IntranetStack{Slug: "p", Hosts: []string{"p.example.test"}, Domain: "example.test", Image: "app:1", Port: 8080, PublicPart: "web", Secrets: consumerEnv{},
			Needs: []deploy.IntranetNeed{{Name: s.Name, Image: s.Image, Command: s.Command, Entrypoint: s.Entrypoint, Env: s.Env, Health: s.Health, Data: s.Data}}})
		var doc struct {
			Services map[string]struct {
				Entrypoint  []string          `yaml:"entrypoint"`
				Command     []string          `yaml:"command"`
				Environment map[string]string `yaml:"environment"`
				Healthcheck struct {
					Test []string `yaml:"test"`
				} `yaml:"healthcheck"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("%s: the stack does not parse: %v\n%s", path.Base(f), err, out)
		}
		got := doc.Services[s.Name]
		// Compose reads $$ as $ and would read a lone $ as one of its own
		// variables.
		read := func(in []string) []string {
			out := make([]string, len(in))
			for i, v := range in {
				if strings.Contains(strings.ReplaceAll(v, "$$", ""), "$") {
					t.Fatalf("%s: Compose would interpolate %q", s.Name, v)
				}
				out[i] = strings.ReplaceAll(v, "$$", "$")
			}
			return out
		}
		env := map[string]string{}
		maps.Copy(env, got.Environment)
		if !slices.Equal(read(got.Entrypoint), s.Entrypoint) || !slices.Equal(read(got.Command), s.Command) || !maps.Equal(env, s.Env) ||
			(s.Health != "" && !slices.Equal(read(got.Healthcheck.Test), []string{"CMD-SHELL", s.Health})) {
			t.Fatalf("%s reaches Portainer changed: entrypoint %q command %q env %v health %q\n%s", s.Name, got.Entrypoint, got.Command, env, got.Healthcheck.Test, out)
		}
	}
}
