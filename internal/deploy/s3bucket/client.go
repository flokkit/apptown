package s3bucket

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/deploy"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/strs"
)

// Client speaks the part of the S3 API a static site needs, path-style,
// signed with AWS Signature Version 4. The key pair is resolved for one
// operation and never logged: errors carry the provider's code and
// message, not the request.
type Client struct {
	Endpoint  string // https://syd1.digitaloceanspaces.com
	Region    string // syd1
	AccessKey string
	SecretKey string
	HTTP      *http.Client
	// Now is the client's clock, which dates each request's signature;
	// nil is the real clock. The provider refuses a signature dated far
	// from its own time, so only a test moves it.
	Now func() time.Time
}

// Error is the provider's answer to a request it refused.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("%d %s", e.Status, http.StatusText(e.Status))
	}
	return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message)
}

// IsCode says whether err is the provider refusing with code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// Object is one listed object.
type Object struct {
	Key  string
	Size int64
}

// ListBuckets asks for the account's buckets: the check that the key pair
// is accepted.
func (c *Client) ListBuckets(ctx context.Context) ([]string, error) {
	var out struct {
		Buckets []struct {
			Name string `xml:"Name"`
		} `xml:"Buckets>Bucket"`
	}
	if err := c.do(ctx, "GET", "", "", nil, nil, nil, &out); err != nil {
		return nil, err
	}
	names := make([]string, len(out.Buckets))
	for i, b := range out.Buckets {
		names[i] = b.Name
	}
	return names, nil
}

// CreateBucket makes bucket, private. A name that exists, whoever holds
// it, is the provider's refusal (BucketAlreadyExists or
// BucketAlreadyOwnedByYou): a bucket is never adopted.
func (c *Client) CreateBucket(ctx context.Context, bucket string) error {
	body := []byte(`<CreateBucketConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><LocationConstraint>` + c.Region + `</LocationConstraint></CreateBucketConfiguration>`)
	return c.do(ctx, "PUT", bucket, "", nil, map[string]string{"x-amz-acl": "private"}, body, nil)
}

// PutWebsite makes bucket answer its website address, / with index.html.
func (c *Client) PutWebsite(ctx context.Context, bucket string) error {
	body := []byte(`<WebsiteConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IndexDocument><Suffix>index.html</Suffix></IndexDocument></WebsiteConfiguration>`)
	return c.do(ctx, "PUT", bucket, "", url.Values{"website": {""}}, map[string]string{"Content-Type": "application/xml"}, body, nil)
}

// PutObject writes one object, readable by anyone at the website address.
func (c *Client) PutObject(ctx context.Context, bucket, key, contentType string, noStore bool, body []byte) error {
	h := map[string]string{"Content-Type": contentType, "x-amz-acl": "public-read"}
	if noStore {
		h["Cache-Control"] = "no-store"
	}
	return c.do(ctx, "PUT", bucket, key, nil, h, body, nil)
}

// List is every object in bucket, page by page.
func (c *Client) List(ctx context.Context, bucket string) ([]Object, error) {
	var all []Object
	token := ""
	for {
		q := url.Values{"list-type": {"2"}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		var page struct {
			Contents []struct {
				Key  string `xml:"Key"`
				Size int64  `xml:"Size"`
			} `xml:"Contents"`
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
		}
		if err := c.do(ctx, "GET", bucket, "", q, nil, nil, &page); err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			all = append(all, Object{Key: o.Key, Size: o.Size})
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return all, nil
		}
		token = page.NextContinuationToken
	}
}

// DeleteObject removes one object; one already gone is no error.
func (c *Client) DeleteObject(ctx context.Context, bucket, key string) error {
	return c.do(ctx, "DELETE", bucket, key, nil, nil, nil, nil)
}

// DeleteBucket removes an empty bucket.
func (c *Client) DeleteBucket(ctx context.Context, bucket string) error {
	return c.do(ctx, "DELETE", bucket, "", nil, nil, nil, nil)
}

func (c *Client) do(ctx context.Context, method, bucket, key string, query url.Values, headers map[string]string, body []byte, out any) error {
	res, err := c.send(ctx, method, bucket, key, query, headers, bytes.NewReader(body), int64(len(body)), sha256Hex(body), c.HTTP)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return err
	}
	if out != nil {
		if err := xml.Unmarshal(data, out); err != nil {
			return fmt.Errorf("reading the answer to %s %s/%s: %w", method, bucket, key, err)
		}
	}
	return nil
}

// send signs and sends one request whose body is length bytes hashing to
// payload, and returns the provider's answer when it is a success; a
// refusal is an *Error with the provider's code and message. The caller
// closes the answer's body. A nil httpc is a client with a minute's bound.
func (c *Client) send(ctx context.Context, method, bucket, key string, query url.Values, headers map[string]string, body io.Reader, length int64, payload string, httpc *http.Client) (*http.Response, error) {
	base, err := url.Parse(strings.TrimRight(c.Endpoint, "/"))
	if err != nil {
		return nil, fmt.Errorf("the endpoint %q is not a URL: %w", c.Endpoint, err)
	}
	p := "/"
	if bucket != "" {
		p += bucket
		if key != "" {
			p += "/" + key
		}
	}
	u := *base
	u.Path = p
	u.RawPath = escapePath(p)
	u.RawQuery = canonicalQuery(query)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.ContentLength = length
	c.sign(req, payload, clock.Or(c.Now)().UTC())
	if httpc == nil {
		httpc = &http.Client{Timeout: 60 * time.Second, Transport: transport}
	}
	res, err := httpc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, deploy.ConnectionError(err)
	}
	if res.StatusCode >= 300 {
		defer res.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		var e struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		}
		_ = xml.Unmarshal(data, &e)
		// An answer to HEAD has no body: a 404 there is the object's absence.
		if e.Code == "" && method == "HEAD" && res.StatusCode == http.StatusNotFound {
			e.Code = "NoSuchKey"
		}
		return nil, &Error{Status: res.StatusCode, Code: e.Code, Message: strs.Truncate(e.Message, 200)}
	}
	return res, nil
}

// sign adds Signature Version 4 headers. Every header set on the request
// is signed.
func (c *Client) sign(req *http.Request, payload string, now time.Time) {
	amz, day := now.Format("20060102T150405Z"), now.Format("20060102")
	req.Header.Set("x-amz-date", amz)
	req.Header.Set("x-amz-content-sha256", payload)
	names := []string{"host"}
	for k := range req.Header {
		names = append(names, strings.ToLower(k))
	}
	sort.Strings(names)
	var canon strings.Builder
	for _, n := range names {
		v := req.Host
		if v == "" {
			v = req.URL.Host
		}
		if n != "host" {
			v = strings.TrimSpace(req.Header.Get(n))
		}
		canon.WriteString(n + ":" + v + "\n")
	}
	signed := strings.Join(names, ";")
	creq := strings.Join([]string{req.Method, req.URL.EscapedPath(), req.URL.RawQuery, canon.String(), signed, payload}, "\n")
	scope := day + "/" + c.Region + "/s3/aws4_request"
	sts := strings.Join([]string{"AWS4-HMAC-SHA256", amz, scope, sha256Hex([]byte(creq))}, "\n")
	k := []byte("AWS4" + c.SecretKey)
	for _, part := range []string{day, c.Region, "s3", "aws4_request"} {
		k = hmacSHA256(k, part)
	}
	sig := hex.EncodeToString(hmacSHA256(k, sts))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.AccessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func hmacSHA256(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

// escapePath is S3's path encoding: every byte but unreserved ones and /.
func escapePath(p string) string {
	var b strings.Builder
	for _, c := range []byte(p) {
		if c == '/' || unreserved(c) {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func unreserved(c byte) bool {
	return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~'
}

func canonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, v := range q[k] {
			parts = append(parts, escapePath(k)+"="+strings.ReplaceAll(escapePath(v), "/", "%2F"))
		}
	}
	return strings.Join(parts, "&")
}
