// Package staticsite is a static release as files a host serves as they
// are (design/subsystems/deploy-targets.md, "Bucket websites"): the site's files from
// its release image, plus the two files AppTown's file server would
// otherwise compute, and the witness that a public address serves exactly
// this publication. It knows no provider; a file-host driver uploads the
// set in its order and asks the witness.
package staticsite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/oci"
	"flokkit.io/forge/internal/strs"
)

// Root is where a self-serving static image keeps its site.
const Root = "/srv"

// MaxBytes bounds a published site: every release uploads all of it, and
// AppTown holds it in memory while it does. Larger sites keep their own
// server on a container target.
const MaxBytes = 200 << 20

// EnvKey and ReleaseKey are the two files AppTown writes into every set.
var (
	EnvKey     = strings.TrimPrefix("/_apptown/env.js", "/")
	ReleaseKey = strings.TrimPrefix(appcontract.ReleasePath, "/")
)

// Pass is when a file is uploaded: assets first, then the entry files
// (HTML and the browser values), then the marker, last of all.
type Pass int

const (
	Assets Pass = iota + 1
	Entries
	Marker
)

// File is one object of a published set.
type File struct {
	Key         string
	Body        []byte
	ContentType string
	Pass        Pass
}

// NoStore: entry files and AppTown's files are never cached, so a
// reload asks for the current page and values.
func (f File) NoStore() bool { return f.Pass != Assets }

// Set is one publication: its files in upload order, its identity (what
// the marker answers) and a digest over every key and body.
type Set struct {
	Files    []File
	Identity string
	Digest   string
}

// Keys are the set's object keys, sorted.
func (s Set) Keys() []string {
	out := make([]string, len(s.Files))
	for i, f := range s.Files {
		out[i] = f.Key
	}
	sort.Strings(out)
	return out
}

// Identity is what a publication's marker answers:
// <part>@<revision>+<set>, where <set> names the image and the browser
// values together, so the same revision published with other values is
// another publication. It is computed before the marker is written.
func Identity(release, imageDigest string, browser map[string]string) (string, error) {
	values, err := canonical(browser)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(imageDigest + "\n" + values))
	return release + "+" + hex.EncodeToString(sum[:])[:16], nil
}

func canonical(browser map[string]string) (string, error) {
	if len(browser) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(browser) // encoding/json sorts map keys
	return string(b), err
}

// Publish is the set for a release: the site's files, the browser values
// as /_apptown/env.js and the identity as /_apptown/release, replacing any
// file of those names. release is <part>@<revision>.
func Publish(files []oci.File, release, imageDigest string, browser map[string]string) (Set, error) {
	if release == "" {
		return Set{}, errors.New("a published site needs its release identity")
	}
	id, err := Identity(release, imageDigest, browser)
	if err != nil {
		return Set{}, err
	}
	values, err := canonical(browser)
	if err != nil {
		return Set{}, err
	}
	var set Set
	for _, f := range files {
		key := strings.TrimPrefix(path.Clean("/"+f.Path), "/")
		if key == EnvKey || key == ReleaseKey {
			continue
		}
		pass := Assets
		if ext := strings.ToLower(path.Ext(key)); ext == ".html" || ext == ".htm" {
			pass = Entries
		}
		set.Files = append(set.Files, File{Key: key, Body: f.Body, ContentType: contentType(key), Pass: pass})
	}
	set.Files = append(set.Files,
		File{Key: EnvKey, Body: []byte(docker.BrowserEnvScript(values)), ContentType: "text/javascript; charset=utf-8", Pass: Entries},
		File{Key: ReleaseKey, Body: []byte(id), ContentType: "text/plain; charset=utf-8", Pass: Marker})
	sort.SliceStable(set.Files, func(i, j int) bool {
		if set.Files[i].Pass != set.Files[j].Pass {
			return set.Files[i].Pass < set.Files[j].Pass
		}
		return set.Files[i].Key < set.Files[j].Key
	})
	h := sha256.New()
	for _, f := range set.Files {
		sum := sha256.Sum256(f.Body)
		fmt.Fprintf(h, "%s %s\n", f.Key, hex.EncodeToString(sum[:]))
	}
	set.Identity, set.Digest = id, "sha256:"+hex.EncodeToString(h.Sum(nil))
	return set, nil
}

func contentType(key string) string {
	switch strings.ToLower(path.Ext(key)) {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".wasm":
		return "application/wasm"
	}
	if t := mime.TypeByExtension(path.Ext(key)); t != "" {
		return t
	}
	return "application/octet-stream"
}

// ErrNotWitnessed: the address did not answer this publication within the
// budget. It is not a failure: a host or its cache may still be catching
// up, and the attempt stays uncertain for a later look.
var ErrNotWitnessed = errors.New("the address does not answer this publication yet")

// Witness asks address for its marker, tries times interval apart, until
// it answers exactly identity. Every request carries a fresh query and
// no-cache, so no cache answers for the host. Anything else after the last
// try is ErrNotWitnessed, saying what answered.
func Witness(ctx context.Context, address, identity string, tries int, interval time.Duration, logf func(string, ...any)) error {
	url := strings.TrimRight(address, "/") + appcontract.ReleasePath
	var last string
	for i := 0; i < tries; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(interval):
			}
		}
		status, body, err := fetch(ctx, url, i)
		switch {
		case err != nil:
			last = err.Error()
		case status == http.StatusOK && body == identity:
			logf("the address serves this publication: %s answers %s", url, identity)
			return nil
		default:
			last = fmt.Sprintf("%d %q", status, strs.Truncate(body, 80))
		}
	}
	return fmt.Errorf("%w: %s should answer %s and answered %s after %d tries", ErrNotWitnessed, url, identity, last, tries)
}

// Gone asks address for its marker until the host answers that it serves
// nothing there (404): what take-down witnesses. Only that answer is
// absence. No answer, a server error or any other status is not evidence
// that the site stopped, so after the last try the withdrawal is
// unconfirmed, saying what answered.
func Gone(ctx context.Context, address string, tries int, interval time.Duration) error {
	url := strings.TrimRight(address, "/") + appcontract.ReleasePath
	var last string
	for i := 0; i < tries; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(interval):
			}
		}
		status, body, err := fetch(ctx, url, i)
		switch {
		case err != nil:
			last = "no answer: " + err.Error()
		case status == http.StatusNotFound:
			return nil
		default:
			last = fmt.Sprintf("%d %q", status, strs.Truncate(body, 80))
		}
	}
	return fmt.Errorf("whether %s still serves the site is unknown: it answered %s after %d tries, not 404", url, last, tries)
}

// Answer is what address answers for its marker now, past caches.
func Answer(ctx context.Context, address string) (status int, body string, err error) {
	return fetch(ctx, strings.TrimRight(address, "/")+appcontract.ReleasePath, 0)
}

func fetch(ctx context.Context, url string, try int) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s?forge-witness=%s-%d", url, rand.Text(), try), nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Cache-Control", "no-cache")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	// A body that could not be read whole is no answer: a prefix that
	// happens to match the identity is not the marker.
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<10))
	if err != nil {
		return 0, "", fmt.Errorf("reading the answer from %s: %w", url, err)
	}
	return res.StatusCode, strings.TrimSpace(string(body)), nil
}
