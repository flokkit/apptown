//go:build integration

package dependency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockertest"
	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/stock"
)

// The shipped image, with real MariaDB and the s3 need's own store, each
// started from its catalogue entry as AppTown starts it: install twice
// without resetting users/content, create media and a thumbnail, read them
// through the site's front door (the store has no public address), replace
// only the web container, and prove the same objects and content remain.
// The app gets only what the needs inject. No WP_HOME is bound, as on a
// stock deployment: the site takes the address it is reached at when
// APPTOWN_ORIGINS lists it, and refuses any other host.
func TestIntegrationWordPressSeedKeepsDataAndMediaOutsideTheWebContainer(t *testing.T) {
	for _, size := range []string{"", "small"} {
		t.Run(map[bool]string{true: "default", false: size}[size == ""], func(t *testing.T) { wordpressSeedKeepsDataAndMedia(t, size) })
	}
}
func wordpressSeedKeepsDataAndMedia(t *testing.T, size string) {
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	name := fmt.Sprintf("forge-wp-it-%d-%d", os.Getpid(), time.Now().UnixNano())
	image, network, db, s3, web := name+":test", name+"-net", name+"-db", name+"-s3", name+"-web"
	dir := t.TempDir()
	if err := fs.WalkDir(stock.FS, "seeds/wordpress-bedrock", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(stock.FS, path)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(path, "seeds/wordpress-bedrock/")
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			return err
		}
		mode := os.FileMode(0644)
		if strings.HasPrefix(rel, "bin/") {
			mode = 0755
		}
		return os.WriteFile(p, raw, mode)
	}); err != nil {
		t.Fatal(err)
	}
	run := func(stdin []byte, args ...string) (string, error) {
		var out, errs bytes.Buffer
		_, err := (docker.CLI{}).Run(ctx, "", docker.Command{Args: args, Stdin: bytes.NewReader(stdin), Stdout: &out, Stderr: &errs})
		if err != nil {
			return out.String(), fmt.Errorf("%w: %s", err, errs.String())
		}
		return out.String() + errs.String(), nil
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := run(nil, args...)
		if err != nil {
			t.Fatalf("docker %s: %v %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(out)
	}
	t.Cleanup(func() {
		_ = dockertest.Run("rm", "-f", "-v", web, db, s3)
		_ = dockertest.Run("network", "rm", network)
		_ = dockertest.Run("image", "rm", "-f", image)
	})
	must("build", "-t", image, dir)
	must("network", "create", "--internal", network)
	catalogued := func(name string) *LocalService {
		t.Helper()
		raw, err := fs.ReadFile(stock.FS, "dependencies/"+name+".yaml")
		if err != nil {
			t.Fatal(err)
		}
		kind, err := Parse(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		return kind.ResolveLocal(nil, name, nil)
	}
	local := catalogued("mariadb")
	memory := int64(256 << 20)
	if size != "" {
		catalog := NewCatalog(testDB(t), time.Now)
		if _, _, err := catalog.SyncStock(ctx, stock.FS, "dependencies"); err != nil {
			t.Fatal(err)
		}
		resolved, err := catalog.ResolveSizes(ctx, &appcontract.Contract{Needs: []string{"mariadb"}, NeedSizes: map[string]appcontract.NeedSize{"mariadb": {Name: size}}})
		if err != nil {
			t.Fatal(err)
		}
		memory = resolved.NeedResources["mariadb"].MemoryBytes
		local.Command = resolved.NeedSizes["mariadb"].Command
	}
	// Each need as AppTown starts it: its own start script, then its flags.
	startNeed := func(svc *LocalService, name string, extra ...string) {
		t.Helper()
		args := append([]string{"run", "-d", "--name", name, "--network", network, "--network-alias", svc.Name}, extra...)
		for k, v := range svc.Env {
			args = append(args, "-e", k+"="+v)
		}
		if len(svc.Entrypoint) > 0 {
			args = append(args, "--entrypoint", svc.Entrypoint[0])
		}
		args = append(args, svc.Image)
		if len(svc.Entrypoint) > 1 {
			args = append(args, svc.Entrypoint[1:]...)
		}
		args = append(args, svc.Command...)
		must(args...)
	}
	startNeed(local, db, "--memory", fmt.Sprint(memory), "--memory-swap", fmt.Sprint(memory))
	if !poll.Until(ctx, 90*time.Second, 250*time.Millisecond, func() bool { _, err := run(nil, "exec", db, "sh", "-c", local.Health); return err == nil }) {
		t.Fatalf("MariaDB never became ready: %s", must("logs", db))
	}

	if cap := must("inspect", "-f", "{{.HostConfig.Memory}}", db); cap != fmt.Sprint(memory) {
		t.Fatalf("database cap %s, want %d", cap, memory)
	}
	pool := "67108864"
	connections := "20"
	if size == "small" {
		pool = "33554432"
		connections = "10"
	}
	settings := must("exec", db, "mariadb", "-uapp", "-papp", "-N", "-e", "SELECT @@innodb_buffer_pool_size, @@max_connections, @@performance_schema, @@innodb_flush_log_at_trx_commit")
	if settings != pool+"\t"+connections+"\t0\t1" {
		t.Fatalf("effective database settings: %q", settings)
	}
	if size == "small" {
		lean := must("exec", db, "mariadb", "-uapp", "-papp", "-N", "-e", "SELECT @@innodb_log_buffer_size, @@aria_pagecache_buffer_size, @@key_buffer_size, @@thread_cache_size, @@table_open_cache, @@table_definition_cache, @@tmp_table_size, @@max_heap_table_size")
		if lean != "4194304\t8388608\t65536\t2\t128\t400\t4194304\t4194304" {
			t.Fatalf("lean server settings: %q", lean)
		}
	}

	// The store as AppTown starts it: its start script makes the bucket and
	// the app's key before it says it is healthy. Nothing here makes a bucket
	// or a policy; the app's key owns the bucket and nothing is public.
	store := catalogued("s3")
	startNeed(store, s3, "--memory", "128m", "-v", store.Data)
	if !poll.Until(ctx, 60*time.Second, 250*time.Millisecond, func() bool { _, err := run(nil, "exec", s3, "sh", "-c", store.Health); return err == nil }) {
		t.Fatalf("the s3 store never became ready: %s", must("logs", s3))
	}
	env := map[string]string{"APPTOWN_ORIGINS": "https://*.preview.test,http://*.site.test"}
	for _, svc := range []*LocalService{local, store} {
		for k, v := range svc.Inject {
			env[k] = v
		}
	}
	for _, k := range []string{"AUTH_KEY", "SECURE_AUTH_KEY", "LOGGED_IN_KEY", "NONCE_KEY", "AUTH_SALT", "SECURE_AUTH_SALT", "LOGGED_IN_SALT", "NONCE_SALT"} {
		env[k] = "fixture-stable-" + k
	}
	ephemeral := func(extra []string, command ...string) (string, error) {
		args := []string{"run", "--rm", "-i", "--network", network}
		for k, v := range env {
			args = append(args, "-e", k+"="+v)
		}
		args = append(args, extra...)
		args = append(args, image)
		args = append(args, command...)
		return run(nil, args...)
	}
	initialize := func() {
		t.Helper()
		out, err := ephemeral([]string{"-e", "WP_ADMIN_PASSWORD=fixture-admin-password"}, "/app/bin/initialize")
		if err != nil {
			t.Fatalf("initialize: %v %s", err, out)
		}
		if strings.Contains(out, "fixture-admin-password") {
			t.Fatalf("initialization printed the fixture password: %s", strings.ReplaceAll(out, "fixture-admin-password", "[REDACTED]"))
		}
	}
	initialize()
	wp := func(command ...string) string {
		t.Helper()
		out, err := ephemeral(nil, append([]string{"/app/bin/wp"}, command...)...)
		if err != nil {
			t.Fatalf("WP-CLI: %v %s", err, out)
		}
		return strings.TrimSpace(out)
	}
	// Install ran with a placeholder address; none of it may stay in data the
	// configuration does not override (the administrator's website, the sample
	// post and pages).
	if got := wp("user", "get", "editor", "--field=user_url"); got != "" {
		t.Fatalf("the administrator's website kept the install address %q", got)
	}
	if got := wp("post", "list", "--post_type=post,page", "--post_status=any", "--format=count"); got != "0" {
		t.Fatalf("a fresh install kept %s example posts/pages", got)
	}
	if got := wp("option", "get", "blog_public"); got != "1" {
		t.Fatal("new production site unexpectedly excludes indexing")
	}
	if out, err := ephemeral([]string{"-e", "WP_ENVIRONMENT_TYPE=staging"}, "/app/bin/wp", "option", "get", "blog_public"); err != nil || strings.TrimSpace(out) != "0" {
		t.Fatalf("staging does not exclude indexing: %v %s", err, out)
	}
	if got := wp("option", "get", "blog_public"); got != "1" {
		t.Fatal("staging indexing setting leaked into production data")
	}
	// CLI work that stores URLs is given the deployment's address for that run.
	out, err := ephemeral([]string{"-e", "WP_HOME=http://blog.site.test:8080"}, "/app/bin/wp", "post", "create", "--post_title=Persistent story", "--post_content=Written before replacement", "--post_status=publish", "--porcelain")
	if err != nil {
		t.Fatalf("WP-CLI: %v %s", err, out)
	}
	post := strings.TrimSpace(out)
	if got := wp("post", "get", post, "--field=guid"); !strings.HasPrefix(got, "http://blog.site.test:8080/") {
		t.Fatalf("a story created with the deployment's address stored GUID %q", got)
	}
	wp("option", "update", "blogdescription", "Keep this setting")
	wp("user", "update", "editor", "--display_name=Original Editor")
	passwordHash := wp("eval", `echo get_user_by('login','editor')->user_pass;`)
	initialize()
	if got := wp("eval", `echo get_user_by('login','editor')->user_pass;`); got != passwordHash {
		t.Fatal("repeat initialization changed the user password")
	}
	if got := wp("user", "get", "editor", "--field=display_name"); got != "Original Editor" {
		t.Fatal("repeat initialization changed the user")
	}
	if got := wp("option", "get", "blogdescription"); got != "Keep this setting" {
		t.Fatal("repeat initialization reset options")
	}
	wp("rewrite", "structure", "/%postname%/")
	start := func() {
		t.Helper()
		args := []string{"run", "-d", "--name", web, "--network", network, "--network-alias", "blog.site.test", "--memory", "256m"}
		for k, v := range env {
			args = append(args, "-e", k+"="+v)
		}
		args = append(args, image)
		must(args...)
		if !poll.Until(ctx, 45*time.Second, 250*time.Millisecond, func() bool {
			_, err := run(nil, "exec", web, "curl", "-fsS", "http://localhost:8080/healthz.php")
			return err == nil
		}) {
			t.Fatalf("web not ready: %s", must("logs", web))
		}
	}
	start()
	checkPage := func() {
		t.Helper()
		body := must("exec", web, "curl", "-fsS", "http://blog.site.test:8080/")
		if !strings.Contains(body, "Persistent story") || !strings.Contains(body, "forge-site") {
			t.Fatalf("theme/front page failed: %.300s", body)
		}
		if !strings.Contains(body, `href="http://blog.site.test:8080/persistent-story/"`) {
			t.Fatalf("links do not carry the address the site was reached at: %.600s", body)
		}
		article := must("exec", web, "curl", "-fsS", "http://blog.site.test:8080/persistent-story/")
		if !strings.Contains(article, "Written before replacement") {
			t.Fatal("article rewrite did not serve content")
		}
	}
	checkPage()
	// A wildcard origin matches whole labels: notsite.test is not under *.site.test.
	for _, host := range []string{"elsewhere.example", "notsite.test"} {
		if got := must("exec", web, "curl", "-sS", "-o", "/dev/null", "-w", "%{http_code}", "-H", "Host: "+host, "http://localhost:8080/"); got != "400" {
			t.Fatalf("host %s, outside APPTOWN_ORIGINS, answered %s, want 400", host, got)
		}
	}
	if out, err := ephemeral([]string{"-e", "WP_HOME=https://canonical.example"}, "/app/bin/wp", "eval", "echo home_url();"); err != nil || strings.TrimSpace(out) != "https://canonical.example" {
		t.Fatalf("a bound WP_HOME is not the site's address: %v %s", err, out)
	}
	// Real media import goes through WordPress's S3 stream wrapper and image
	// editors. WP-CLI has no request, so it is given the site's address.
	site := "http://blog.site.test:8080"
	cli := func(command ...string) string {
		t.Helper()
		return must(append([]string{"exec", "-e", "WP_HOME=" + site, web, "/app/bin/wp"}, command...)...)
	}
	must("exec", web, "php", "-r", `$i=imagecreatetruecolor(800,600);imagefill($i,0,0,imagecolorallocate($i,42,83,61));imagepng($i,'/tmp/probe.png');`)
	attachment := cli("media", "import", "/tmp/probe.png", "--title=Persistent image", "--porcelain")
	metadata := cli("eval", fmt.Sprintf(`$m=wp_get_attachment_metadata(%s); if (empty($m['sizes']['thumbnail'])) {exit(3);} echo json_encode(['original'=>wp_get_attachment_url(%s),'thumbnail'=>wp_get_attachment_image_url(%s,'thumbnail')]);`, attachment, attachment, attachment))
	var urls map[string]string
	if err := json.Unmarshal([]byte(metadata), &urls); err != nil {
		t.Fatalf("media metadata: %v %s", err, metadata)
	}
	// What a browser gets at a URL: status, headers and the body's hash.
	type answer struct{ status, headers, sha string }
	fetch := func(url string, curlArgs ...string) answer {
		t.Helper()
		script := `curl -sS --path-as-is -o /tmp/body -D /tmp/head "$@" >/dev/null && sed -n '1p' /tmp/head | cut -d' ' -f2 && tr -d '\r' < /tmp/head && echo '--' && sha256sum /tmp/body | cut -d' ' -f1`
		out := must(append([]string{"exec", web, "sh", "-c", script, "sh"}, append(curlArgs, url)...)...)
		lines := strings.Split(out, "\n")
		return answer{status: lines[0], headers: strings.ToLower(strings.Join(lines[1:len(lines)-2], "\n")), sha: lines[len(lines)-1]}
	}
	hashes := map[string]string{}
	for kind, url := range urls {
		if !strings.HasPrefix(url, site+"/app/uploads/") {
			t.Fatalf("%s URL %s is not served by the site", kind, url)
		}
		got := fetch(url)
		if got.status != "200" || !strings.Contains(got.headers, "content-type: image/png") || !strings.Contains(got.headers, "x-content-type-options: nosniff") {
			t.Fatalf("%s through the front door: %s %s", kind, got.status, got.headers)
		}
		hashes[kind] = got.sha
	}
	if hashes["original"] == hashes["thumbnail"] {
		t.Fatal("thumbnail was not a distinct generated image")
	}
	key := strings.TrimPrefix(urls["original"], site+"/app/")
	// Nothing in the bucket is public: the store refuses an anonymous read of
	// the same object, and listing.
	for _, url := range []string{"http://s3:9000/app/" + key, "http://s3:9000/app?list-type=2"} {
		if got := must("exec", web, "curl", "-sS", "-o", "/dev/null", "-w", "%{http_code}", url); got != "403" {
			t.Fatalf("anonymous %s at the store answered %s, want 403", url, got)
		}
	}
	// The front door reads one object under uploads/ and nothing else.
	must("exec", web, "/app/bin/wp", "eval", `file_put_contents('s3://'.S3_UPLOADS_BUCKET.'/private/secret.txt','not for visitors') or exit(3); file_put_contents('s3://'.S3_UPLOADS_BUCKET.'/uploads/page.html','<html><script>1</script></html>') or exit(4);`)
	for _, probe := range []struct{ path, want string }{
		{"/app/uploads/", "404"},
		{"/app/uploads/?list-type=2", "404"},
		{"/app/uploads/2026//x.png", "404"},
		{"/app/uploads/missing.png", "404"},
	} {
		if got := fetch(site + probe.path); got.status != probe.want {
			t.Fatalf("%s answered %s, want %s", probe.path, got.status, probe.want)
		}
	}
	secret := sha256.Sum256([]byte("not for visitors"))
	for _, path := range []string{"/app/uploads/../../private/secret.txt", "/app/uploads/%2e%2e/%2e%2e/private/secret.txt", "/app/uploads/..%2fprivate/secret.txt"} {
		if got := fetch(site + path); got.status == "200" || got.sha == hex.EncodeToString(secret[:]) {
			t.Fatalf("%s, out of uploads/, answered %s with the object", path, got.status)
		}
	}
	if got := fetch(urls["original"], "-X", "PUT", "--data", "overwrite"); got.status != "405" {
		t.Fatalf("PUT through the front door answered %s, want 405", got.status)
	}
	if got := fetch(urls["original"]); got.sha != hashes["original"] {
		t.Fatal("a refused PUT changed the object")
	}
	// Same origin as the site: what could run script is a download.
	if got := fetch(site + "/app/uploads/page.html"); got.status != "200" || !strings.Contains(got.headers, "content-disposition: attachment") || !strings.Contains(got.headers, "x-content-type-options: nosniff") {
		t.Fatalf("an HTML upload is not served as a download: %s %s", got.status, got.headers)
	}
	if got := fetch(urls["original"], "-H", "Range: bytes=0-9"); got.status != "206" || !strings.Contains(got.headers, "content-range: bytes 0-9/") {
		t.Fatalf("a range request answered %s %s", got.status, got.headers)
	}
	// No request the plugin's client sends carries an ACL: a write through the
	// stream wrapper, a folder marker and a rename's copy, each seen at the
	// sign step as sent.
	probe := must("exec", web, "/app/bin/wp", "eval", `$seen = [];
S3_Uploads\Plugin::get_instance()->s3()->getHandlerList()->appendSign(Aws\Middleware::mapRequest(function ($r) use (&$seen) {
    $seen[] = ['method' => $r->getMethod(), 'acl' => $r->hasHeader('x-amz-acl'), 'copy' => $r->hasHeader('x-amz-copy-source')];
    return $r;
}), 'fixture-acl-probe');
$b = 's3://' . S3_UPLOADS_BUCKET . '/uploads/acl-probe';
file_put_contents("$b/a.txt", 'a') or exit(5);
mkdir("$b/folder") or exit(6);
rename("$b/a.txt", "$b/b.txt") or exit(7);
echo json_encode($seen);`)
	var requests []struct {
		Method    string
		ACL, Copy bool
	}
	if err := json.Unmarshal([]byte(probe), &requests); err != nil {
		t.Fatalf("ACL probe: %v %s", err, probe)
	}
	puts, copies := 0, 0
	for _, r := range requests {
		if r.ACL {
			t.Fatalf("a %s request carried an ACL: %s", r.Method, probe)
		}
		if r.Method == "PUT" {
			puts++
		}
		if r.Copy {
			copies++
		}
	}
	if puts < 3 || copies != 1 {
		t.Fatalf("the probe did not write, make a folder and copy (%d PUTs, %d copies): %s", puts, copies, probe)
	}
	for _, path := range []string{"/app/config/application.php", "/app/web/app/themes/forge-site/style.css", "/app/web/app/plugins/probe.php"} {
		if _, err := run(nil, "exec", web, "sh", "-c", `printf bad >> "$1"`, "sh", path); err == nil {
			t.Fatalf("runtime can write source: %s", path)
		}
	}
	if got := must("exec", web, "id", "-u"); got == "0" {
		t.Fatal("web runs as root")
	}
	if got := wp("eval", `wp_set_current_user(get_user_by('login','editor')->ID); echo (current_user_can('install_plugins') || !DISALLOW_FILE_MODS || !DISALLOW_FILE_EDIT) ? 'unsafe' : 'denied';`); got != "denied" {
		t.Fatal("dashboard code modification is enabled")
	}
	must("rm", "-f", web)
	initialize()
	start()
	checkPage()
	if got := wp("post", "get", post, "--field=post_content"); got != "Written before replacement" {
		t.Fatal("replacement lost content")
	}
	for kind, url := range urls {
		if got := fetch(url); got.status != "200" || got.sha != hashes[kind] {
			t.Fatalf("replacement lost %s: %s", kind, got.status)
		}
	}
	cli("media", "regenerate", attachment, "--yes")
	if got := fetch(urls["thumbnail"]); got.status != "200" || !strings.Contains(got.headers, "content-type: image/png") {
		t.Fatalf("regenerated thumbnail: %s %s", got.status, got.headers)
	}
	if oom := must("inspect", "-f", "{{.State.OOMKilled}}", db); oom != "false" {
		t.Fatalf("database OOM: %s", oom)
	}
	// Readiness remains independent when the data services become unavailable.
	must("stop", db, s3)
	if got := must("exec", web, "curl", "-fsS", "http://localhost:8080/healthz.php"); got != "ready" {
		t.Fatal("readiness depends on database or S3")
	}
	// A store that does not answer is an error, never an empty image.
	if got := fetch(urls["original"]); got.status != "502" {
		t.Fatalf("media with the store stopped answered %s, want 502", got.status)
	}
}
