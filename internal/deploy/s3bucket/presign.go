package s3bucket

import (
	"encoding/hex"
	"errors"
	"github.com/flokkit/apptown/internal/clock"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Presign gives a job access to exactly one object for a bounded time.
// The store key stays in AppTown. PUT uses one unsigned-payload request;
// the caller must read the object back with its own key before recording it.
func (c *Client) Presign(method, bucket, key string, lifetime time.Duration) (string, error) {
	if (method != "GET" && method != "PUT") || lifetime < time.Second || lifetime > 7*24*time.Hour {
		return "", errors.New("an object URL needs GET or PUT and a lifetime of one second to seven days")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return "", err
	}
	u.Path = "/" + bucket + "/" + key
	u.RawPath = escapePath(u.Path)
	now := clock.Or(c.Now)().UTC()
	day := now.Format("20060102")
	date := now.Format("20060102T150405Z")
	scope := day + "/" + c.Region + "/s3/aws4_request"
	q := url.Values{"X-Amz-Algorithm": {"AWS4-HMAC-SHA256"}, "X-Amz-Credential": {c.AccessKey + "/" + scope}, "X-Amz-Date": {date}, "X-Amz-Expires": {strconv.FormatInt(int64(lifetime/time.Second), 10)}, "X-Amz-SignedHeaders": {"host"}}
	canonical := strings.Join([]string{method, u.EscapedPath(), canonicalQuery(q), "host:" + u.Host + "\n", "host", "UNSIGNED-PAYLOAD"}, "\n")
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", date, scope, sha256Hex([]byte(canonical))}, "\n")
	k := []byte("AWS4" + c.SecretKey)
	for _, s := range []string{day, c.Region, "s3", "aws4_request"} {
		k = hmacSHA256(k, s)
	}
	q.Set("X-Amz-Signature", hex.EncodeToString(hmacSHA256(k, toSign)))
	u.RawQuery = canonicalQuery(q)
	return u.String(), nil
}
