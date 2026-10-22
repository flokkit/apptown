package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/s3bucket/s3fake"
)

func TestAttachHostTarget(t *testing.T) {
	w := newTargetHTTPWorld(t)
	builtin := w.seedBuiltin(t)
	writeTargetSecret(t, w, "DROPLET_KEY=-----BEGIN OPENSSH PRIVATE KEY-----\\nabc\\n-----END OPENSSH PRIVATE KEY-----\n") // gitleaks:allow (test fixture, not a credential)
	var hosts []string
	w.setDockerClient(targetDockerClientFunc(func(_ context.Context, host string, _ docker.Command) ([]byte, error) {
		hosts = append(hosts, host)
		if strings.Contains(host, "dead") {
			return []byte("ssh: connect to host dead.example port 22: Connection refused"), errors.New("exit status 1")
		}
		return []byte("29.7.2 on Debian, 0/0 containers\n"), nil
	}))
	call := func(method, path, body, name string) (int, map[string]any) {
		return targetCall(t, w.targets.AttachTarget, method, path, body, map[string]string{"name": name})
	}
	if code, _ := call("POST", "/api/v1/targets", `{"name":"Bad Name","host":"ssh://root@h","keyFrom":"DROPLET_KEY"}`, ""); code != 400 {
		t.Fatalf("bad name: %d", code)
	}
	if code, out := call("POST", "/api/v1/targets", `{"name":"box","host":"ssh://root@h","keyFrom":"NOPE"}`, ""); code != 422 || !strings.Contains(out["error"].(string), "no secret named") {
		t.Fatalf("missing secret: %d %v", code, out)
	}
	if code, out := call("POST", "/api/v1/targets", `{"name":"dead","host":"ssh://root@dead.example","keyFrom":"DROPLET_KEY"}`, ""); code != 422 || !strings.Contains(out["error"].(string), "did not answer") {
		t.Fatalf("dead daemon: %d %v", code, out)
	}
	if _, err := w.deployment.Targets.ByName(t.Context(), "dead"); err == nil {
		t.Fatal("failed probe saved target")
	}
	code, out := call("POST", "/api/v1/targets", `{"name":"droplet","host":"ssh://deploy@droplet.example:2222","keyFrom":"DROPLET_KEY","domain":"*.apps.example.com"}`, "")
	if code != 200 {
		t.Fatalf("attach: %d %v", code, out)
	}
	target, err := w.deployment.Targets.ByName(t.Context(), "droplet")
	if err != nil || target.Origin != deployment.OriginAttached || target.Kind != deployment.KindDockerHost || !target.Capabilities.PersistentVolumes || target.Ingress["domain"] != "apps.example.com" {
		t.Fatalf("saved target: %+v %v", target, err)
	}
	if target.Address["keyFrom"] != "DROPLET_KEY" {
		t.Fatalf("row exposed secret: %v", target.Address)
	}
	if hosts[len(hosts)-1] != "ssh://apptown-target-droplet" {
		t.Fatalf("wrong docker host: %v", hosts)
	}
	files, err := filepath.Glob(filepath.Join(w.dir, "ssh", "target-*.key"))
	if err != nil || len(files) == 0 {
		t.Fatal("no target key", err)
	}
	var key []byte
	for _, file := range files {
		key, _ = os.ReadFile(file)
		if strings.Contains(string(key), "PRIVATE KEY") {
			break
		}
	}
	if !strings.Contains(string(key), "BEGIN OPENSSH PRIVATE KEY-----\nabc\n") {
		t.Fatalf("key was not unfolded: %q", key)
	}
	cfg, err := os.ReadFile(filepath.Join(w.dir, "ssh-config"))
	if err != nil || !strings.Contains(string(cfg), "Host apptown-target-droplet\n  HostName droplet.example\n  User deploy\n  Port 2222") {
		t.Fatalf("ssh stanza: %s %v", cfg, err)
	}
	if code, out := targetCall(t, w.targets.CheckTarget, "POST", "/api/v1/targets/droplet/check", "", map[string]string{"name": "droplet"}); code != 200 || out["ok"] != true {
		t.Fatalf("check: %d %v", code, out)
	}
	if code, out := targetCall(t, w.targets.OfferTarget, "POST", "/api/v1/targets/droplet/offers", `{"group":"*"}`, map[string]string{"name": "droplet"}); code != 200 {
		t.Fatalf("offer: %d %v", code, out)
	}
	dk, err := w.hosts.DockerScopeFor(context.Background(), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dk.Host != "ssh://apptown-target-droplet" || dk.Hostname != "droplet.example" || dk.Domain != "apps.example.com" {
		t.Fatalf("attached scope: %+v", dk)
	}
	if dk.FrontDoor("waypoint", "main", 40001) != "https://waypoint.apps.example.com" || dk.ProbeURL(40001) != "http://droplet.example:40001" {
		t.Fatalf("attached URLs: %q %q", dk.FrontDoor("waypoint", "main", 40001), dk.ProbeURL(40001))
	}
	local, err := w.hosts.DockerScopeFor(context.Background(), builtin.ID)
	if err != nil || local.Host != "" || local.ProbeURL(40001) != "http://127.0.0.1:40001" {
		t.Fatalf("local scope changed: %+v %v", local, err)
	}
}

func TestAttachPortainerTargetAndEdit(t *testing.T) {
	t.Parallel()
	w := newTargetHTTPWorld(t)
	w.seedBuiltin(t)
	server := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "pt-key" {
			resp.WriteHeader(401)
			return
		}
		if r.URL.Path == "/api/endpoints" {
			_, _ = resp.Write([]byte(`[{"Id":3,"Name":"home-box","Type":1}]`))
		} else {
			_, _ = resp.Write([]byte(`{"Id":3,"Name":"home-box","Type":1,"Snapshots":[{"DockerVersion":"29.7.2"}]}`))
		}
	}))
	defer server.Close()
	writeTargetSecret(t, w, "PT_URL="+server.URL+"\nPT_KEY=pt-key\n")
	call := func(handler http.HandlerFunc, method, path, body, name string) (int, map[string]any) {
		return targetCall(t, handler, method, path, body, map[string]string{"name": name})
	}
	if code, out := call(w.targets.AttachTarget, "POST", "/api/v1/targets", `{"kind":"portainer","name":"home","urlFrom":"PT_URL","apiKeyFrom":"PT_KEY","environment":"nope"}`, ""); code != 422 || !strings.Contains(out["error"].(string), "no environment named") {
		t.Fatalf("unknown environment: %d %v", code, out)
	}
	code, out := call(w.targets.AttachTarget, "POST", "/api/v1/targets", `{"kind":"portainer","name":"home","urlFrom":"PT_URL","apiKeyFrom":"PT_KEY","environment":"home-box","domain":"apps.home.example"}`, "")
	if code != 200 {
		t.Fatalf("attach: %d %v", code, out)
	}
	target, err := w.deployment.Targets.ByName(t.Context(), "home")
	if err != nil || target.Kind != deployment.KindPortainer || target.Address["environment"] != "home-box" || !target.Capabilities.OwnLiveness || target.Ingress["domain"] != "apps.home.example" {
		t.Fatalf("saved: %+v %v", target, err)
	}
	if target.Address["apiKeyFrom"] != "PT_KEY" || target.Address["apiKey"] != nil {
		t.Fatalf("row exposed key: %v", target.Address)
	}
	cfg, err := w.providers.PortainerConfig(context.Background(), target)
	if err != nil || cfg.PortainerURL != server.URL || cfg.EnvName != "home-box" || cfg.AppsDomain != "apps.home.example" {
		t.Fatalf("config: %+v %v", cfg, err)
	}
	if code, out := call(w.targets.CheckTarget, "POST", "/api/v1/targets/home/check", "", "home"); code != 200 || out["ok"] != true {
		t.Fatalf("check: %d %v", code, out)
	}
	// The status check above did not move the settings revision, so an edit
	// read before it still saves.
	if code, out := call(w.targets.EditTarget, "PATCH", "/api/v1/targets/home", `{"expectedRevision":0,"domain":"*.apps2.home.example","previews":true}`, "home"); code != 200 {
		t.Fatalf("edit: %d %v", code, out)
	}
	if code, _ := call(w.targets.EditTarget, "PATCH", "/api/v1/targets/home", `{"expectedRevision":0,"previews":false}`, "home"); code != 409 {
		t.Fatalf("stale edit: %d", code)
	}
	if code, _ := call(w.targets.EditTarget, "PATCH", "/api/v1/targets/home", `{"previews":false}`, "home"); code != 400 {
		t.Fatalf("edit without a revision: %d", code)
	}
	target, _ = w.deployment.Targets.ByName(t.Context(), "home")
	if target.Ingress["domain"] != "apps2.home.example" || !target.Capabilities.Previews || !target.Capabilities.PersistentVolumes {
		t.Fatalf("edited: %+v", target)
	}
	if code, _ := call(w.targets.EditTarget, "PATCH", "/api/v1/targets/home", `{"expectedRevision":1,"needsMode":"sideways"}`, "home"); code != 400 {
		t.Fatalf("bad mode: %d", code)
	}
	if code, _ := call(w.targets.EditTarget, "PATCH", "/api/v1/targets/"+targetTestBuiltin, `{"expectedRevision":0,"previews":false}`, targetTestBuiltin); code != 409 {
		t.Fatalf("built-in edited: %d", code)
	}
	code, out = call(w.targets.TargetPage, "GET", "/api/v1/targets/home", "", "home")
	var kinds string
	for _, entry := range out["history"].([]any) {
		kinds += entry.(map[string]any)["kind"].(string) + " "
	}
	if code != 200 || !strings.Contains(kinds, "target.edited") || !strings.Contains(kinds, "target.attached") {
		t.Fatalf("history: %d %s", code, kinds)
	}
}

func TestAttachDigitalOceanTarget(t *testing.T) {
	t.Parallel()
	w, _ := targetDOWorld(t)
	call := func(body string) (int, map[string]any) {
		return targetCall(t, w.targets.AttachTarget, "POST", "/api/v1/targets", body, nil)
	}
	if code, _ := call(`{"kind":"do-app","name":"do-nyc","tokenFrom":"NO_SUCH_TOKEN","region":"nyc"}`); code != 422 {
		t.Fatalf("missing token: %d", code)
	}
	code, out := call(`{"kind":"do-app","name":"do-nyc","tokenFrom":"DIGITALOCEAN_ACCESS_TOKEN","region":"nyc","registry":"forge"}`)
	if code != 200 {
		t.Fatalf("attach: %d %v", code, out)
	}
	target, err := w.deployment.Targets.ByName(t.Context(), "do-nyc")
	if err != nil || target.Kind != deployment.KindDOApp || target.Provider != "do-nyc" || target.Address["region"] != "nyc" || target.NeedsMode != "managed" {
		t.Fatalf("target: %+v %v", target, err)
	}
	cfg, err := w.providers.DigitalOceanConfig(t.Context(), "do-nyc")
	if err != nil || cfg.TokenFrom != "DIGITALOCEAN_ACCESS_TOKEN" || cfg.Region != "nyc" || cfg.Registry != "forge" {
		t.Fatalf("connector: %+v %v", cfg, err)
	}
}

// The account check reports the registry's room, read from DigitalOcean:
// how many of the tier's repositories are used.
//
// Kills: a check that names the registry but not its room (complaint #14:
// headroom invisible until a release fails).
func TestAttachTargetDryRun(t *testing.T) {
	t.Parallel()
	w, api := targetDOWorld(t)
	api.SeedRegistry("flokkit", "basic", 5, "example-app")
	code, out := targetCall(t, w.targets.AttachTarget, "POST", "/api/v1/targets", `{"kind":"do-app","name":"do-dry","tokenFrom":"DIGITALOCEAN_ACCESS_TOKEN","region":"nyc","dryRun":true}`, nil)
	if code != 200 || out["ok"] != true || out["dryRun"] != true {
		t.Fatalf("dry run: %d %v", code, out)
	}
	if out["registry"] != "flokkit" || !strings.Contains(out["detail"].(string), "container registry flokkit: 1 of 5 repositories used, basic tier") {
		t.Fatalf("registry detail: %v", out)
	}
	if _, err := w.deployment.Targets.ByName(t.Context(), "do-dry"); err == nil {
		t.Fatal("dry run saved target")
	}
	if _, err := w.providers.DigitalOceanConfig(t.Context(), "do-dry"); err == nil {
		t.Fatal("dry run made connector")
	}
}

// The App Platform target's page shows its registry's plan and how many
// repositories are used, as the newest check read them from DigitalOcean,
// with when; a check that cannot read the registry shows it unknown, with
// why, rather than the older numbers.
//
// Kills: no reading on the page; a reading without its time; an unreadable
// registry shown with stale numbers as current.
func TestTheTargetPageShowsTheRegistrysPlan(t *testing.T) {
	t.Parallel()
	w, api := targetDOWorld(t)
	api.SeedRegistry("flokkit", "basic", 5, "a", "b", "c", "d", "e")
	if code, out := targetCall(t, w.targets.AttachTarget, "POST", "/api/v1/targets", `{"kind":"do-app","name":"do-page","tokenFrom":"DIGITALOCEAN_ACCESS_TOKEN","region":"nyc"}`, nil); code != 200 {
		t.Fatalf("attach: %d %v", code, out)
	}
	page := func() map[string]any {
		code, out := targetCall(t, w.targets.TargetPage, "GET", "/api/v1/targets/do-page", "", map[string]string{"name": "do-page"})
		reg, _ := out["registry"].(map[string]any)
		if code != 200 || reg == nil || reg["readAt"] == "" || reg["readAt"] == nil {
			t.Fatalf("page: %d %v", code, out)
		}
		return reg
	}
	if reg := page(); reg["registry"] != "flokkit" || reg["tier"] != "basic" || reg["used"] != 5.0 || reg["included"] != 5.0 || reg["full"] != true || reg["unknown"] != nil {
		t.Fatalf("full registry: %v", reg)
	}
	api.Fail("GET", "/v2/registry/subscription", 500)
	if code, out := targetCall(t, w.targets.CheckTarget, "POST", "/api/v1/targets/do-page/check", "", map[string]string{"name": "do-page"}); code != 200 || out["ok"] != true {
		t.Fatalf("check: %d %v", code, out)
	}
	if reg := page(); reg["unknown"] == nil || reg["used"] != 0.0 || reg["full"] != false {
		t.Fatalf("unreadable registry: %v", reg)
	}
}

func TestRetireDOTargetRemovesConnector(t *testing.T) {
	t.Parallel()
	w, _ := targetDOWorld(t)
	if code, out := targetCall(t, w.targets.AttachTarget, "POST", "/api/v1/targets", `{"kind":"do-app","name":"do-gone","tokenFrom":"DIGITALOCEAN_ACCESS_TOKEN","region":"nyc"}`, nil); code != 200 {
		t.Fatalf("attach: %d %v", code, out)
	}
	binding, err := w.integrations.Bindings.ByName(t.Context(), "do-gone")
	if err != nil {
		t.Fatal(err)
	}
	if binding.Managed {
		t.Fatal("attached account became config-managed")
	}
	// A retire without its confirmation, with no body at all or with an
	// empty object, is refused by one sentence naming the body to send, and
	// leaves the target in service.
	for _, body := range []string{"", "{}"} {
		code, out := targetCall(t, w.targets.RetireTarget, "POST", "/api/v1/targets/do-gone/retire", body, map[string]string{"name": "do-gone"})
		if msg, _ := out["error"].(string); code != 400 || !strings.Contains(msg, `send {"confirm":"do-gone"}`) {
			t.Fatalf("retire with body %q: %d %v", body, code, out)
		}
		if target, err := w.deployment.Targets.ByName(t.Context(), "do-gone"); err != nil || target.Status.Available() != nil {
			t.Fatalf("refused retire changed the target: %+v %v", target, err)
		}
	}
	if code, out := targetCall(t, w.targets.RetireTarget, "POST", "/api/v1/targets/do-gone/retire", `{"confirm":"do-gone"}`, map[string]string{"name": "do-gone"}); code != 200 {
		t.Fatalf("retire: %d %v", code, out)
	}
	if _, err := w.providers.DigitalOceanConfig(t.Context(), "do-gone"); err == nil {
		t.Fatal("last target kept connector")
	}
	// Its name is not free again, and the refusal says why and what to do
	// (complaint #20: "already exists" alone, for a target nobody can see
	// in service).
	code, out := targetCall(t, w.targets.AttachTarget, "POST", "/api/v1/targets", `{"kind":"do-app","name":"do-gone","tokenFrom":"DIGITALOCEAN_ACCESS_TOKEN","region":"nyc"}`, nil)
	if msg, _ := out["error"].(string); code != 409 || !strings.Contains(msg, "was retired") || !strings.Contains(msg, "a new name") {
		t.Fatalf("attach under a retired name: %d %v", code, out)
	}
	if target, err := w.deployment.Targets.ByName(t.Context(), "do-gone"); err != nil || !errors.Is(target.Status.Available(), deployment.ErrTargetRetired) {
		t.Fatalf("the refused attach revived the target: %+v %v", target, err)
	}
	// A target in service under the name is still "already exists".
	if code, out := targetCall(t, w.targets.AttachTarget, "POST", "/api/v1/targets", `{"kind":"do-app","name":"do-here","tokenFrom":"DIGITALOCEAN_ACCESS_TOKEN","region":"nyc"}`, nil); code != 200 {
		t.Fatalf("attach: %d %v", code, out)
	}
	code, out = targetCall(t, w.targets.AttachTarget, "POST", "/api/v1/targets", `{"kind":"do-app","name":"do-here","tokenFrom":"DIGITALOCEAN_ACCESS_TOKEN","region":"nyc"}`, nil)
	if msg, _ := out["error"].(string); code != 409 || !strings.Contains(msg, "already exists") {
		t.Fatalf("attach under a name in service: %d %v", code, out)
	}
}

// A bucket target is attached only when the store accepts its key pair,
// named by secret, never stored in the row; attaching says what the key can
// reach and that take-down deletes a deployment's bucket.
func TestAttachBucketTarget(t *testing.T) {
	w := newTargetHTTPWorld(t)
	store := s3fake.New(t, "AKFORGE")
	writeTargetSecret(t, w, "SPACES_ACCESS_KEY=AKFORGE\nSPACES_SECRET_KEY=never-shown\nWRONG_KEY=AKOTHER\n")
	call := func(body string) (int, map[string]any) {
		return targetCall(t, w.targets.AttachTarget, "POST", "/api/v1/targets", body, nil)
	}
	body := func(name, access string) string {
		return `{"kind":"s3-bucket","name":"` + name + `","endpoint":"` + store.API.URL + `","region":"syd1","websiteEndpoint":"` + store.WebsiteTemplate() + `","accessKeyFrom":"` + access + `","secretKeyFrom":"SPACES_SECRET_KEY"}`
	}
	if code, out := call(body("refused", "WRONG_KEY")); code != 422 || !strings.Contains(out["error"].(string), "did not accept") {
		t.Fatalf("a refused key: %d %v", code, out)
	}
	if code, _ := call(`{"kind":"s3-bucket","name":"half","endpoint":"` + store.API.URL + `"}`); code != 400 {
		t.Fatalf("a half-described target: %d", code)
	}
	code, out := call(body("spaces", "SPACES_ACCESS_KEY"))
	if code != 200 || !strings.Contains(out["detail"].(string), "full-access") || !strings.Contains(out["detail"].(string), "deletes it") {
		t.Fatalf("attach: %d %v", code, out)
	}
	target, err := w.deployment.Targets.ByName(t.Context(), "spaces")
	if err != nil || target.Kind != deployment.KindS3Bucket || target.Address["accessKeyFrom"] != "SPACES_ACCESS_KEY" {
		t.Fatalf("saved target: %+v %v", target, err)
	}
	for _, v := range target.Address {
		if s, _ := v.(string); s == "AKFORGE" || s == "never-shown" {
			t.Fatalf("the row holds a key: %v", target.Address)
		}
	}
	if code, out := targetCall(t, w.targets.CheckTarget, "POST", "/api/v1/targets/spaces/check", "", map[string]string{"name": "spaces"}); code != 200 || out["ok"] != true {
		t.Fatalf("check: %d %v", code, out)
	}
}
