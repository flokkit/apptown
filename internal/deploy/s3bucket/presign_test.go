package s3bucket

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// This fixed vector was calculated independently with SHA-256/HMAC and
// RFC 3986 query encoding. It pins method, object, host, expiry and date.
func TestObjectURLSignatureBindsTheMethodObjectAndExpiry(t *testing.T) {
	c := &Client{Endpoint: "https://store.example", Region: "us-east-1", AccessKey: "example-access", SecretKey: "example-secret", Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }}
	raw, err := c.Presign("PUT", "backups", "app/one file+.dump", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.EscapedPath() != "/backups/app/one%20file%2B.dump" || u.Query().Get("X-Amz-Expires") != "3600" || u.Query().Get("X-Amz-SignedHeaders") != "host" {
		t.Fatalf("URL does not scope one object: %s", raw)
	}
	if u.Query().Get("X-Amz-Signature") != "b64c4fe19013c79011daed8b46924ebbbfd29f48cd7e4d7241f38566fef04c83" {
		t.Fatal("signature differs from fixed vector")
	}
	get, err := c.Presign("GET", "backups", "app/one file+.dump", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if get == raw || strings.Contains(raw, c.SecretKey) {
		t.Fatal("object URL exposed the secret key or did not bind the method")
	}
	for _, method := range []string{"DELETE", "POST"} {
		if _, err := c.Presign(method, "backups", "file", time.Hour); err == nil {
			t.Fatalf("method %s was allowed", method)
		}
	}
	for _, duration := range []time.Duration{0, 8 * 24 * time.Hour} {
		if _, err := c.Presign("PUT", "backups", "file", duration); err == nil {
			t.Fatal("unbounded object URL was allowed")
		}
	}
}
