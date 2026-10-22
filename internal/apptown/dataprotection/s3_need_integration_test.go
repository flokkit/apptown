//go:build integration

package dataprotection

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockertest"
	"github.com/flokkit/apptown/internal/poll"
)

// On a real daemon the stock S3 need's image pulls and its start script
// readies the store on a fresh volume: the bucket app exists before the
// app could start, and the app's key (local) puts and gets objects there
// and is refused making a bucket, as a key for one bucket is on a cloud
// store. Its backup is the kind's copy through the S3 API, run beside the
// store: each object's bytes and headers (content type, cache control,
// user metadata), one uploaded in parts among them, and no key. Restored
// into another deployment's store it puts the bucket back exactly (an
// object that deployment made after the copy is gone) and is verified,
// with every header as it was. The kind's verify holds a store to a copy
// by its bytes: an object changed to the same size is caught, whether its
// ETag is its MD5 or it was uploaded in parts and must be read back.
func TestIntegrationStockS3IsCopiedThroughItsAPIAndRestoredVerified(t *testing.T) {
	s, pid, s3Of := needWorld(t, "s3")
	network := func(container string) string {
		out, err := dockertest.Output("inspect", "--format", "{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{end}}", container)
		if err != nil {
			t.Fatalf("the network of %s: %v", container, err)
		}
		return strings.TrimSpace(string(out))
	}
	// envOf is the environment a deployment's store was started with.
	envOf := func(deployment string) map[string]string {
		out, err := dockertest.Output("inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", s3Of[deployment])
		if err != nil {
			t.Fatalf("the store of %s: %v", deployment, err)
		}
		env := map[string]string{}
		for _, line := range strings.Split(string(out), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				env[k] = v
			}
		}
		return env
	}
	// s3 asks the deployment's S3 server, by its network alias, with its
	// own key, as the app would.
	s3 := func(deployment string, args ...string) (string, error) {
		env := envOf(deployment)
		cmd := append([]string{"run", "--rm", "--network", network(s3Of[deployment]), "curlimages/curl:8.22.0", "-sS", "-f",
			"--aws-sigv4", "aws:amz:us-east-1:s3", "--user", env["APP_ACCESS_KEY_ID"] + ":" + env["APP_SECRET_ACCESS_KEY"]}, args...)
		out, err := dockertest.CombinedOutput(cmd...)
		return string(out), err
	}
	must := func(deployment string, args ...string) string {
		t.Helper()
		out, err := s3(deployment, args...)
		if err != nil {
			t.Fatalf("%s %v: %v %s", deployment, args, err, out)
		}
		return out
	}
	// inParts uploads body as one part of a multipart upload, as an SDK
	// does for a large file: its ETag is then not its MD5.
	inParts := func(deployment, key, body string) {
		t.Helper()
		url := "http://s3:9000/app/" + key
		upload := regexp.MustCompile(`<UploadId>([^<]+)</UploadId>`).FindStringSubmatch(must(deployment, "-X", "POST", "-H", "Content-Type: application/octet-stream", url+"?uploads"))
		if upload == nil {
			t.Fatal("no upload id")
		}
		head := must(deployment, "-X", "PUT", "--data-binary", body, "-D", "-", "-o", "/dev/null", url+"?partNumber=1&uploadId="+upload[1])
		etag := regexp.MustCompile(`(?i)etag: *("[^"]+")`).FindStringSubmatch(head)
		if etag == nil {
			t.Fatalf("no part ETag in %q", head)
		}
		must(deployment, "-X", "POST", "-H", "Content-Type: application/xml", "--data-binary",
			"<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>"+etag[1]+"</ETag></Part></CompleteMultipartUpload>", url+"?uploadId="+upload[1])
	}
	// headers are an object's headers the copy keeps, as the store answers them.
	headers := func(deployment, key string) map[string]string {
		t.Helper()
		got := map[string]string{}
		for _, line := range strings.Split(must(deployment, "-I", "http://s3:9000/app/"+key), "\n") {
			name, value, ok := strings.Cut(strings.TrimSpace(line), ":")
			name = strings.ToLower(name)
			if ok && (name == "content-type" || name == "cache-control" || name == "content-disposition" || strings.HasPrefix(name, "x-amz-meta-")) {
				got[name] = strings.TrimSpace(value)
			}
		}
		return got
	}
	for _, d := range []string{"main", "fresh"} {
		health := ""
		if !poll.Until(context.Background(), 60*time.Second, 250*time.Millisecond, func() bool {
			out, _ := dockertest.Output("inspect", "-f", "{{.State.Health.Status}}", s3Of[d])
			health = strings.TrimSpace(string(out))
			return health == "healthy"
		}) {
			t.Fatalf("the S3 store of %s did not mark itself ready: %s", d, health)
		}
		// Ready means the bucket is there, empty, for the app's key.
		if out, err := s3(d, "http://s3:9000/app?list-type=2"); err != nil || !strings.Contains(out, "<Name>app</Name>") {
			t.Fatalf("the bucket of %s: %v %s", d, err, out)
		}
	}
	// The app's key cannot make a bucket, not even its own again.
	for _, bucket := range []string{"other", "app"} {
		if out, err := s3("main", "-X", "PUT", "http://s3:9000/"+bucket); err == nil || !strings.Contains(out, "403") {
			t.Fatalf("the app's key made the bucket %s: %v %s", bucket, err, out)
		}
	}
	// Each deployment's keys are its own, and the gateway's root key is
	// AppTown's, not the app's.
	mainEnv, freshEnv := envOf("main"), envOf("fresh")
	if key := mainEnv["APP_ACCESS_KEY_ID"]; len(key) != 64 || key == freshEnv["APP_ACCESS_KEY_ID"] || mainEnv["ROOT_ACCESS_KEY_ID"] == key || mainEnv["APP_SECRET_ACCESS_KEY"] == freshEnv["APP_SECRET_ACCESS_KEY"] {
		t.Fatalf("the stores' keys: main %v, fresh %v", mainEnv, freshEnv)
	}

	must("main", "-X", "PUT", "-H", "Content-Type: text/plain; charset=utf-8", "-H", "Cache-Control: max-age=60", "-H", "x-amz-meta-owner: bls",
		"--data-binary", "kept object", "http://s3:9000/app/notes/one.txt")
	must("main", "-X", "PUT", "-H", "Content-Type: image/png", "--data-binary", "\x89PNG not really", "http://s3:9000/app/media/a%20photo.png")
	// Free text in a header keeps its runs of spaces, which signing a
	// request collapses and the store does not.
	must("main", "-X", "PUT", "-H", "Content-Type: application/pdf", "-H", "x-amz-meta-title: Board  report", "-H", `Content-Disposition: attachment; filename="board  report.pdf"`,
		"--data-binary", "%PDF not really", "http://s3:9000/app/papers/board.pdf")
	inParts("main", "media/large.bin", "uploaded in parts")
	want := map[string]map[string]string{}
	for _, key := range []string{"notes/one.txt", "media/a%20photo.png", "media/large.bin", "papers/board.pdf"} {
		want[key] = headers("main", key)
	}
	if want["notes/one.txt"]["x-amz-meta-owner"] != "bls" || want["media/a%20photo.png"]["content-type"] != "image/png" || want["papers/board.pdf"]["x-amz-meta-title"] != "Board  report" {
		t.Fatalf("the store did not keep the headers it was given: %v", want)
	}

	b, err := s.TakeBackup(context.Background(), pid, "main", "manual", "bls")
	if err != nil || b.Status != BackupOK || len(b.Items) != 1 || b.Items[0].Copy != CopyDeclared || b.Items[0].Format != "s3-objects" {
		t.Fatalf("take: %+v %v", b, err)
	}
	copyPath := filepath.Join(b.Path, b.Items[0].File)
	raw, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	// The copy is the objects and what was said of them: no key, of the
	// app's or the gateway's.
	for _, secret := range []string{mainEnv["APP_SECRET_ACCESS_KEY"], mainEnv["ROOT_SECRET_ACCESS_KEY"], mainEnv["ROOT_ACCESS_KEY_ID"]} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("the copy holds %s", secret)
		}
	}
	if names := tarEntries(t, copyPath); len(names) != 9 || names[len(names)-1] != "manifest.json" {
		t.Fatalf("the copy holds %v", names)
	}

	// fresh has an object of its own, made after the copy: the restore puts
	// the bucket back as the copy holds it, so it goes.
	must("fresh", "-X", "PUT", "--data-binary", "made later", "http://s3:9000/app/late.txt")
	res, err := s.restoreBackup(context.Background(), nil, pid, "fresh", b.ID, "bls")
	if err != nil || res.Status != RestoreDone || len(res.Items) != 1 || res.Items[0].Outcome != RestoreVerified {
		t.Fatalf("restore: %+v %v", res, err)
	}
	for key, h := range want {
		if got := headers("fresh", key); !equalMaps(got, h) {
			t.Fatalf("%s came back with %v, not %v", key, got, h)
		}
	}
	if out := must("fresh", "http://s3:9000/app/media/large.bin"); out != "uploaded in parts" {
		t.Fatalf("the object uploaded in parts came back as %q", out)
	}
	if out, err := s3("fresh", "http://s3:9000/app/late.txt"); err == nil {
		t.Fatalf("an object not in the copy is still there: %q", out)
	}

	// The kind's verify is what the restore was checked by: it catches an
	// object changed to the same size, by its ETag for one put whole and by
	// reading back one uploaded in parts.
	verify := func() error {
		kind, err := s.catalog.ByCapability(context.Background(), "s3")
		if err != nil {
			t.Fatal(err)
		}
		dk := docker.Scope{Client: docker.CLI{Binary: "docker"}}
		conn, _, err := containerConnection(context.Background(), dk, kind, s3Of["fresh"])
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(copyPath)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		_, env := copyEnv(kind.Copy, conn)
		step := besideNeed(true, s3Of["fresh"], clientImage(kind.Copy, ""), env, kind.Copy.Verify)
		step.Stdin, step.Stdout = f, io.Discard
		_, err = dk.RunStep(context.Background(), step)
		return err
	}
	if err := verify(); err != nil {
		t.Fatalf("the restored store does not pass its own copy's check: %v", err)
	}
	must("fresh", "-X", "PUT", "-H", "Content-Type: text/plain; charset=utf-8", "-H", "Cache-Control: max-age=60", "-H", "x-amz-meta-owner: bls",
		"--data-binary", "KEPT OBJECT", "http://s3:9000/app/notes/one.txt")
	if err := verify(); err == nil {
		t.Fatal("an object changed to the same size passed the check")
	}
	if _, err := s.restoreBackup(context.Background(), nil, pid, "fresh", b.ID, "bls"); err != nil {
		t.Fatal(err)
	}
	inParts("fresh", "media/large.bin", "UPLOADED IN PARTS")
	if err := verify(); err == nil {
		t.Fatal("an object uploaded in parts and changed to the same size passed the check")
	}
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// tarEntries are the entries of a tar file, in order.
func tarEntries(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := tar.NewReader(f)
	var names []string
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
}
