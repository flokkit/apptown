package dataprotection

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/s3bucket/s3fake"
	"flokkit.io/forge/internal/forge/dependency"
)

// fakeBuckets runs the s3 kind's copy jobs on the fake copy runner: against
// the account's Spaces (the s3fake store), only as a key the account holds
// now with a grant on the job's bucket; and against the new store a check
// starts, by its network alias, with the kind's own local key. A copy is a
// JSON list of objects; import fills only an empty bucket, restore makes the
// bucket the copy, verify passes only when the bucket holds exactly it.
type fakeBuckets struct {
	mu     sync.Mutex
	store  *s3fake.Store
	keys   func() []map[string]any // the account's Spaces keys now
	spaces string                  // the store's host:port
	copy   *dependency.Copy
	// emulator is the new store a check fills, bucket "app".
	emulator map[string]s3fake.Object
	// failExport fails every bucket export; failCheck every verify.
	failExport, failCheck bool
	// atExport, when set, is called as an export starts; atCheck as a
	// check's import into a new store starts. Both outside the lock.
	atExport, atCheck func()
	// jobs is each job's step and S3 environment, in order.
	jobs []bucketJob
	// started is the environment the check's new store was started with.
	started map[string]string
}

type bucketJob struct {
	step string
	env  map[string]string
}

type fakeObject struct {
	Key, Type string
	Body      []byte
}

func newFakeBuckets(store *s3fake.Store, keys func() []map[string]any, copy *dependency.Copy) *fakeBuckets {
	u, _ := url.Parse(store.API.URL)
	return &fakeBuckets{store: store, keys: keys, spaces: u.Host, copy: copy, emulator: map[string]s3fake.Object{}}
}

func (f *fakeBuckets) reply(c docker.Command) ([]byte, error, bool) {
	if i := slices.Index(c.Args, "--network-alias"); len(c.Args) > 0 && c.Args[0] == "run" && i > 0 && i+1 < len(c.Args) && c.Args[i+1] == "s3" {
		env := map[string]string{}
		for j, a := range c.Args {
			if a == "-e" && j+1 < len(c.Args) {
				k, v, _ := strings.Cut(c.Args[j+1], "=")
				env[k] = v
			}
		}
		f.mu.Lock()
		f.started = env
		f.mu.Unlock()
		return nil, nil, false
	}
	if len(c.Args) < 3 || c.Args[0] != "run" || !strings.Contains(c.Args[len(c.Args)-1], "$S3_COPY_PROGRAM") {
		return nil, nil, false
	}
	command := c.Args[len(c.Args)-1]
	env := map[string]string{}
	for _, kv := range c.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	step := ""
	for _, s := range []string{"export", "import", "restore", "verify"} {
		if strings.HasSuffix(command, `" `+s) {
			step = s
		}
	}
	f.mu.Lock()
	hook := f.atExport
	if step != "export" {
		hook = nil
		if step == "import" && env["S3_HOST"] == "s3" {
			hook = f.atCheck
		}
	}
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = append(f.jobs, bucketJob{step: step, env: env})
	for _, a := range c.Args {
		if strings.Contains(a, "SECRET") && strings.Contains(a, "=") {
			return []byte("a secret is in the job's arguments"), errors.New("exit status 9"), true
		}
	}
	get, put, list, err := f.place(env)
	if err != nil {
		return []byte(err.Error()), errors.New("exit status 1"), true
	}
	switch step {
	case "export":
		if f.failExport {
			return []byte("the store answered 503"), errors.New("exit status 1"), true
		}
		var out []fakeObject
		for _, k := range list() {
			o, _ := get(k)
			out = append(out, fakeObject{Key: k, Type: o.ContentType, Body: o.Body})
		}
		raw, _ := json.Marshal(out)
		_, _ = c.Stdout.Write(raw)
	case "import", "restore":
		raw, _ := io.ReadAll(c.Stdin)
		var in []fakeObject
		if err := json.Unmarshal(raw, &in); err != nil {
			return []byte("the copy cannot be read"), errors.New("exit status 1"), true
		}
		if step == "import" && len(list()) > 0 {
			return []byte("the bucket already holds objects"), errors.New("exit status 1"), true
		}
		for _, o := range in {
			put(o.Key, s3fake.Object{Body: o.Body, ContentType: o.Type})
		}
	case "verify":
		raw, _ := io.ReadAll(c.Stdin)
		var in []fakeObject
		if err := json.Unmarshal(raw, &in); err != nil || f.failCheck {
			return []byte("objects differ from the copy"), errors.New("exit status 1"), true
		}
		want := map[string]fakeObject{}
		for _, o := range in {
			want[o.Key] = o
		}
		got := list()
		if !slices.Equal(got, slices.Sorted(maps.Keys(want))) {
			return []byte("the bucket holds other objects than the copy"), errors.New("exit status 1"), true
		}
		for _, k := range got {
			if o, _ := get(k); !bytes.Equal(o.Body, want[k].Body) || o.ContentType != want[k].Type {
				return []byte("objects differ from the copy"), errors.New("exit status 1"), true
			}
		}
	default:
		return nil, nil, false
	}
	return nil, nil, true
}

// place is the store a job's connection reaches, refused unless its key may
// reach that bucket. f.mu is held.
func (f *fakeBuckets) place(env map[string]string) (get func(string) (s3fake.Object, bool), put func(string, s3fake.Object), list func() []string, err error) {
	bucket := env["S3_BUCKET"]
	switch env["S3_HOST"] + ":" + env["S3_PORT"] {
	case f.spaces:
		if env["S3_TLS"] != "disable" {
			return nil, nil, nil, errors.New("the fake store speaks no TLS")
		}
		held := slices.ContainsFunc(f.keys(), func(k map[string]any) bool {
			if k["access_key"] != env["S3_ACCESS_KEY_ID"] {
				return false
			}
			grants, _ := json.Marshal(k["grants"])
			return strings.Contains(string(grants), `"bucket":"`+bucket+`"`)
		})
		if !held || env["S3_SECRET_ACCESS_KEY"] == "" {
			return nil, nil, nil, errors.New("the store answered 403: no such key for the bucket " + bucket)
		}
		list = func() []string { k := f.store.Keys(bucket); slices.Sort(k); return k }
		return func(k string) (s3fake.Object, bool) { return f.store.Get(bucket, k) }, func(k string, o s3fake.Object) { f.store.Put(bucket, k, o) }, list, nil
	case "s3:9000":
		// The new store answers the key it was started with: the
		// operation's own, never the catalogue's.
		if key := f.started["APP_ACCESS_KEY_ID"]; key == "" || key == "local" || env["S3_ACCESS_KEY_ID"] != key || env["S3_SECRET_ACCESS_KEY"] != f.started["APP_SECRET_ACCESS_KEY"] || bucket != "app" {
			return nil, nil, nil, errors.New("the new store answered 403")
		}
		list = func() []string { return slices.Sorted(maps.Keys(f.emulator)) }
		return func(k string) (s3fake.Object, bool) { o, ok := f.emulator[k]; return o, ok }, func(k string, o s3fake.Object) { f.emulator[k] = o }, list, nil
	}
	return nil, nil, nil, errors.New("could not connect to " + env["S3_HOST"])
}

// steps are the jobs' steps so far.
func (f *fakeBuckets) steps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, j := range f.jobs {
		out = append(out, j.step)
	}
	return out
}
