package docker

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A deployment made before 1.0 is booted under the names it was made with:
// the driver hands the boot its recorded prefix, never the current one, so
// its app, stack and data are looked for where they are. One with no
// recorded prefix is refused before anything is touched.
func TestADeploymentIsBootedUnderTheNamesItWasMadeWith(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("app:\n  game:\n    static: .\n    public: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"version":1,"revision":"abc"}`)
	art := deploy.Artifact{Kind: "docker-source", Body: body, Hash: deploy.ConfigHash(string(body)),
		Source: &deploy.Source{Workspace: t.TempDir(), Revision: "abc", Slug: "game", Contract: c}}
	for prefix, want := range map[string]string{"forge-": "forge-env-game-main", "apptown-": "apptown-env-game-main", "": ""} {
		boot := &fakeBoot{daemon: &fakeDaemon{}}
		d := &Driver{Scope: func(context.Context) (Scope, error) { return Scope{Client: boot.daemon}, nil }, Boot: boot,
			URL: func(_, _ string, port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) }, AfterRelease: func(deploy.Placement) {}}
		place := deploy.Placement{HostPrefix: prefix, ID: idstest.N(7), Label: "7", Name: "main"}
		_, err := d.Reconcile(context.Background(), place, 1, art, "key", func(string, ...any) {}, func(map[string]any) error { return nil })
		if want == "" {
			if err == nil || !strings.Contains(err.Error(), "not a prefix") || boot.req.Name != "" {
				t.Fatalf("a placement with no recorded prefix was booted: %v %+v", err, boot.req)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if boot.req.Name != want || string(boot.req.Names) != prefix {
			t.Fatalf("a deployment recorded %q was booted as %q under %q", prefix, boot.req.Name, boot.req.Names)
		}
	}
}

// The incident of 21 Oct 2026, under a rename: a standing need made before
// 1.0 is found under its forge- name, kept on its forge- volume, and nothing
// is made under another name beside it.
func TestAnOldDeploymentsNeedKeepsItsNamesAndItsData(t *testing.T) {
	t.Parallel()
	vol := Before1.NeedVolume("env-shop-main", "postgres")
	client := carryDaemon("volume\t"+vol+"\t/var/lib/docker/volumes/"+vol+"/_data\t"+carryPath+"\n", oldEnv, "268435456", nil)
	own := Ownership{Needs: map[string]string{"postgres": carryPath}, Names: Before1}
	if _, err := EnsureStandingServices(context.Background(), Scope{Client: client}, "env-shop-main", []deploy.ServiceSpec{carryService(true)}, "production", own); err != nil {
		t.Fatal(err)
	}
	if got := runMount(client.args); got != vol {
		t.Fatalf("made again on %q, not the volume its data is in (%s): %v", got, vol, client.args)
	}
	for _, args := range client.args {
		if line := strings.Join(args, " "); strings.Contains(line, "apptown-") {
			t.Fatalf("something was looked for or made under another name: %s", line)
		}
	}
	if !hasStandingCall(client.args, "inspect "+Before1.Service("env-shop-main", "postgres")) {
		t.Fatalf("the old service was not looked for under its own name: %v", client.args)
	}
}

// A standing boot that does not say which names its deployment carries is
// refused: it would look for its data under the wrong ones.
func TestAStandingBootNamingNoPrefixIsRefused(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("app:\n  game:\n    static: .\n    public: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{}
	_, err = NewHost(Dependencies{}).Boot(t.Context(), BootRequest{Docker: Scope{Client: client}, Name: "forge-env-game-main", StackID: "env-game-main", Workspace: t.TempDir(), Persistent: true}, c)
	if err == nil || !strings.Contains(err.Error(), "names no host prefix") || len(client.args) != 0 {
		t.Fatalf("a standing boot with no prefix ran: %v %v", err, client.args)
	}
}
