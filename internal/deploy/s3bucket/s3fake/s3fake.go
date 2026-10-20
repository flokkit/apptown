// Package s3fake is an in-memory S3-compatible store for the s3bucket
// driver's tests and assembled Forge tests: the API, path-style, and each
// bucket's website address. It is not a deployment driver.
package s3fake

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Call is one API request as the store saw it.
type Call struct{ Method, Bucket, Key, Query string }

// Object is one stored object.
type Object struct {
	Body         []byte
	ContentType  string
	CacheControl string
	Public       bool
	// ACL is the canned ACL the object was written with ("" when none).
	ACL string
	// SHA256 is the x-amz-meta-sha256 the writer sent.
	SHA256 string
}

type bucket struct {
	website bool
	objects map[string]Object
}

// Store is one account. Its API answers only requests signed with
// AccessKey; anonymous API requests are refused, as a provider refuses
// anonymous listing. Website serves each bucket's public objects under
// /<bucket>/ when its website configuration is set.
type Store struct {
	AccessKey string
	PageSize  int // objects per list page; 0 is 1000

	mu      sync.Mutex
	buckets map[string]*bucket
	// grants are keys other than AccessKey: access key -> the one bucket it
	// reaches, "" for every bucket (Grant). A key for one bucket can do
	// anything to that bucket's objects and nothing else: it cannot make,
	// delete or list buckets, as DigitalOcean's readwrite keys cannot.
	grants        map[string]string
	calls         []Call
	fail          map[string]int // "METHOD bucket/key" -> status to answer
	frozen        map[string]*bucket
	websiteStatus int

	// Mangle, when set, changes what the store keeps of a written object,
	// as a store that lost or altered bytes would; its answer to a later
	// HEAD or GET is what it kept.
	Mangle func(key string, body []byte) []byte

	// before, when set (SetBefore), is told of each API request before it
	// is served, outside the store's lock.
	before func(Call)

	API, Website *httptest.Server
}

// New starts a store whose servers close with the test.
func New(t testing.TB, accessKey string) *Store {
	s := &Store{AccessKey: accessKey, buckets: map[string]*bucket{}, fail: map[string]int{}, grants: map[string]string{}}
	s.API = httptest.NewServer(http.HandlerFunc(s.serveAPI))
	s.Website = httptest.NewServer(http.HandlerFunc(s.serveWebsite))
	t.Cleanup(s.API.Close)
	t.Cleanup(s.Website.Close)
	return s
}

// WebsiteTemplate is the website address template for this store.
func (s *Store) WebsiteTemplate() string { return s.Website.URL + "/{bucket}" }

// Fail makes the next requests of method on bucket/key answer status; a
// key "*suffix" is every key ending in suffix.
func (s *Store) Fail(method, bucket, key string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[method+" "+bucket+"/"+key] = status
}

// WebsiteAnswers makes every website request answer status (0: serve
// normally), as a host in an outage would.
func (s *Store) WebsiteAnswers(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.websiteStatus = status
}

func (s *Store) failing(method, bucket, key string) (int, bool) {
	if status, ok := s.fail[method+" "+bucket+"/"+key]; ok {
		return status, true
	}
	for k, status := range s.fail {
		if suffix, ok := strings.CutPrefix(k, method+" "+bucket+"/*"); ok && strings.HasSuffix(key, suffix) {
			return status, true
		}
	}
	return 0, false
}

// ClearFailures ends every injected failure.
func (s *Store) ClearFailures() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = map[string]int{}
}

// Freeze makes the website answer what is stored now, whatever changes
// after, as a cache would; Thaw ends it.
func (s *Store) Freeze() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frozen = map[string]*bucket{}
	for name, b := range s.buckets {
		cp := &bucket{website: b.website, objects: map[string]Object{}}
		for k, o := range b.objects {
			cp.objects[k] = o
		}
		s.frozen[name] = cp
	}
}

func (s *Store) Thaw() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frozen = nil
}

// SetBefore has the store tell f of each API request before serving it.
func (s *Store) SetBefore(f func(Call)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.before = f
}

// Calls is every API request so far.
func (s *Store) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// Buckets is the names of the buckets that exist.
func (s *Store) Buckets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for n := range s.buckets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Keys is a bucket's object keys, sorted; nil when it does not exist.
func (s *Store) Keys(name string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.buckets[name]
	if b == nil {
		return nil
	}
	out := []string{}
	for k := range b.objects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Get is one object.
func (s *Store) Get(name, key string) (Object, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b := s.buckets[name]; b != nil {
		o, ok := b.objects[key]
		return o, ok
	}
	return Object{}, false
}

// Put stores an object directly, as something other than AppTown would.
func (s *Store) Put(name, key string, o Object) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b := s.buckets[name]; b != nil {
		b.objects[key] = o
	}
}

// AddBucket makes a bucket as someone else would.
func (s *Store) AddBucket(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buckets[name] = &bucket{objects: map[string]Object{}}
}

// Grant lets access reach bucket ("" for every bucket, a full-access key).
func (s *Store) Grant(access, bucket string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grants[access] = bucket
}

// Revoke ends a Grant.
func (s *Store) Revoke(access string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.grants, access)
}

// allowed says whether the request's key may do this to bucket name; key
// is the object, "" for the bucket itself.
func (s *Store) allowed(r *http.Request, name, key string) bool {
	auth := r.Header.Get("Authorization")
	if s.AccessKey != "" && strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential="+s.AccessKey+"/") {
		return true
	}
	for access, scope := range s.grants {
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential="+access+"/") {
			continue
		}
		if scope == "" {
			return true
		}
		// A key for one bucket reads and writes its objects and lists it;
		// it makes, deletes and lists no bucket.
		bucketOp := key == "" && (r.Method == "PUT" || r.Method == "DELETE")
		return name == scope && !bucketOp
	}
	return false
}

func errXML(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message></Error>", code, code)
}

func (s *Store) serveAPI(w http.ResponseWriter, r *http.Request) {
	name, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	s.mu.Lock()
	before := s.before
	s.mu.Unlock()
	if before != nil {
		before(Call{Method: r.Method, Bucket: name, Key: key, Query: r.URL.RawQuery})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, Call{Method: r.Method, Bucket: name, Key: key, Query: r.URL.RawQuery})
	if !s.allowed(r, name, key) {
		errXML(w, 403, "AccessDenied")
		return
	}
	if status, ok := s.failing(r.Method, name, key); ok {
		errXML(w, status, "InternalError")
		return
	}
	b := s.buckets[name]
	q := r.URL.Query()
	switch {
	case name == "" && r.Method == "GET":
		var out struct {
			XMLName xml.Name `xml:"ListAllMyBucketsResult"`
			Names   []string `xml:"Buckets>Bucket>Name"`
		}
		for n := range s.buckets {
			out.Names = append(out.Names, n)
		}
		sort.Strings(out.Names)
		_ = xml.NewEncoder(w).Encode(out)
	case key == "" && r.Method == "PUT" && q.Has("website"):
		if b == nil {
			errXML(w, 404, "NoSuchBucket")
			return
		}
		b.website = true
	case key == "" && r.Method == "PUT":
		if b != nil {
			errXML(w, 409, "BucketAlreadyOwnedByYou")
			return
		}
		s.buckets[name] = &bucket{objects: map[string]Object{}}
	case key == "" && r.Method == "GET":
		if b == nil {
			errXML(w, 404, "NoSuchBucket")
			return
		}
		keys := make([]string, 0, len(b.objects))
		for k := range b.objects {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		size := s.PageSize
		if size <= 0 {
			size = 1000
		}
		start := 0
		if tok := q.Get("continuation-token"); tok != "" {
			start, _ = strconv.Atoi(tok)
		}
		end := min(start+size, len(keys))
		var out struct {
			XMLName  xml.Name `xml:"ListBucketResult"`
			Contents []struct {
				Key  string `xml:"Key"`
				Size int    `xml:"Size"`
			} `xml:"Contents"`
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken,omitempty"`
		}
		for _, k := range keys[start:end] {
			out.Contents = append(out.Contents, struct {
				Key  string `xml:"Key"`
				Size int    `xml:"Size"`
			}{k, len(b.objects[k].Body)})
		}
		if end < len(keys) {
			out.IsTruncated, out.NextContinuationToken = true, strconv.Itoa(end)
		}
		_ = xml.NewEncoder(w).Encode(out)
	case key == "" && r.Method == "DELETE":
		if b == nil {
			errXML(w, 404, "NoSuchBucket")
			return
		}
		if len(b.objects) > 0 {
			errXML(w, 409, "BucketNotEmpty")
			return
		}
		delete(s.buckets, name)
		w.WriteHeader(204)
	case r.Method == "PUT":
		if b == nil {
			errXML(w, 404, "NoSuchBucket")
			return
		}
		body, _ := io.ReadAll(r.Body)
		// A store checks the body against the signed payload hash.
		if sum := sha256.Sum256(body); r.Header.Get("x-amz-content-sha256") != hex.EncodeToString(sum[:]) {
			errXML(w, 400, "XAmzContentSHA256Mismatch")
			return
		}
		if s.Mangle != nil {
			body = s.Mangle(key, body)
		}
		b.objects[key] = Object{Body: body, ContentType: r.Header.Get("Content-Type"), CacheControl: r.Header.Get("Cache-Control"), Public: r.Header.Get("x-amz-acl") == "public-read",
			ACL: r.Header.Get("x-amz-acl"), SHA256: r.Header.Get("x-amz-meta-sha256")}
	case r.Method == "GET" || r.Method == "HEAD":
		if b == nil {
			errXML(w, 404, "NoSuchBucket")
			return
		}
		o, ok := b.objects[key]
		if !ok {
			errXML(w, 404, "NoSuchKey")
			return
		}
		sum := md5.Sum(o.Body)
		w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(o.Body)))
		if o.SHA256 != "" {
			w.Header().Set("x-amz-meta-sha256", o.SHA256)
		}
		if r.Method == "GET" {
			_, _ = w.Write(o.Body)
		}
	case r.Method == "DELETE":
		if b == nil {
			errXML(w, 404, "NoSuchBucket")
			return
		}
		delete(b.objects, key)
		w.WriteHeader(204)
	default:
		errXML(w, 400, "NotImplemented")
	}
}

func (s *Store) serveWebsite(w http.ResponseWriter, r *http.Request) {
	name, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	s.mu.Lock()
	if s.websiteStatus != 0 {
		status := s.websiteStatus
		s.mu.Unlock()
		errXML(w, status, "ServiceUnavailable")
		return
	}
	buckets := s.buckets
	if s.frozen != nil {
		buckets = s.frozen
	}
	b := buckets[name]
	var o Object
	ok := false
	if b != nil && b.website {
		if key == "" || strings.HasSuffix(key, "/") {
			key += "index.html"
		}
		o, ok = b.objects[key]
	}
	s.mu.Unlock()
	switch {
	case b == nil || !b.website:
		errXML(w, 404, "NoSuchWebsiteConfiguration")
	case !ok:
		errXML(w, 404, "NoSuchKey")
	case !o.Public:
		errXML(w, 403, "AccessDenied")
	default:
		w.Header().Set("Content-Type", o.ContentType)
		w.Write(o.Body)
	}
}
