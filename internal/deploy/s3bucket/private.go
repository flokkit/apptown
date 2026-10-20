package s3bucket

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// Private objects: what a backup store holds. Unlike the website's
// PutObject, which makes every object readable by anyone, these are written
// private, and a stored file is checked after it is written: the store's
// own answer for its size and its MD5 must match the file sent, and the
// SHA-256 AppTown computed travels as the object's metadata.

// MetaSHA256 is the metadata header carrying an object's SHA-256.
const MetaSHA256 = "x-amz-meta-sha256"

// ObjectHead is what the store says of one object.
type ObjectHead struct {
	Size   int64
	ETag   string // as the store gives it, without quotes
	SHA256 string // AppTown's digest, from the object's metadata
}

// Stored is a file written to the store and checked there.
type Stored struct {
	Key    string
	Size   int64
	SHA256 string
}

// streaming bounds no request by time: a copy can take as long as its size
// needs; the caller's context bounds it.
var streaming = &http.Client{Transport: transport}

// transport is this package's pool of connections: one that nothing else in
// the process closes. A connection pooled on http.DefaultTransport is closed
// under a request whenever anything calls CloseIdleConnections on it (every
// httptest.Server.Close does), and the request fails with "connection
// broken" though the store never answered.
var transport = http.DefaultTransport.(*http.Transport).Clone()

// PutPrivate writes a small object, private.
func (c *Client) PutPrivate(ctx context.Context, bucket, key, contentType string, body []byte) error {
	return c.do(ctx, "PUT", bucket, key, nil, map[string]string{"Content-Type": contentType, "x-amz-acl": "private", MetaSHA256: sha256Hex(body)}, body, nil)
}

// PutPrivateFile streams a file into the store, private, and checks what
// the store then holds: its size must be the file's and its ETag the
// file's MD5 (a single-part upload's ETag is its MD5). The request carries
// the file's SHA-256 as its signed payload hash, so a store that received
// other bytes refuses it.
func (c *Client) PutPrivateFile(ctx context.Context, bucket, key, path string) (Stored, error) {
	size, sum, md, err := digests(path)
	if err != nil {
		return Stored{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Stored{}, err
	}
	defer f.Close()
	res, err := c.send(ctx, "PUT", bucket, key, nil, map[string]string{"Content-Type": "application/octet-stream", "x-amz-acl": "private", MetaSHA256: sum}, f, size, sum, streaming)
	if err != nil {
		return Stored{}, fmt.Errorf("writing %s to the store: %w", key, err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	head, err := c.Head(ctx, bucket, key)
	if err != nil {
		return Stored{}, fmt.Errorf("reading back what the store holds at %s: %w", key, err)
	}
	if head.Size != size {
		return Stored{}, fmt.Errorf("the store holds %d bytes at %s; %d were sent", head.Size, key, size)
	}
	if !strings.EqualFold(head.ETag, md) {
		return Stored{}, fmt.Errorf("the store's digest of %s (%s) is not the file's (%s)", key, head.ETag, md)
	}
	return Stored{Key: key, Size: size, SHA256: sum}, nil
}

// Head asks the store about one object.
func (c *Client) Head(ctx context.Context, bucket, key string) (ObjectHead, error) {
	res, err := c.send(ctx, "HEAD", bucket, key, nil, nil, bytes.NewReader(nil), 0, sha256Hex(nil), c.HTTP)
	if err != nil {
		return ObjectHead{}, err
	}
	res.Body.Close()
	size, err := strconv.ParseInt(res.Header.Get("Content-Length"), 10, 64)
	if err != nil {
		return ObjectHead{}, fmt.Errorf("the store gave %s no size", key)
	}
	return ObjectHead{Size: size, ETag: strings.Trim(res.Header.Get("ETag"), `"`), SHA256: res.Header.Get(MetaSHA256)}, nil
}

// GetObject reads a small object (a manifest, a check's probe).
func (c *Client) GetObject(ctx context.Context, bucket, key string) ([]byte, error) {
	res, err := c.send(ctx, "GET", bucket, key, nil, nil, bytes.NewReader(nil), 0, sha256Hex(nil), c.HTTP)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return io.ReadAll(io.LimitReader(res.Body, 8<<20))
}

// GetToFile streams one object into a new file at path and returns its size
// and SHA-256, for the caller to hold against its record.
func (c *Client) GetToFile(ctx context.Context, bucket, key, path string) (int64, string, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, "", err
	}
	n, sum, err := c.GetTo(ctx, bucket, key, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return 0, "", err
	}
	return n, sum, nil
}

// GetTo streams one object into w, through memory only, and returns its
// size and SHA-256 for the caller to hold against its record.
func (c *Client) GetTo(ctx context.Context, bucket, key string, w io.Writer) (int64, string, error) {
	res, err := c.send(ctx, "GET", bucket, key, nil, nil, bytes.NewReader(nil), 0, sha256Hex(nil), streaming)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), res.Body)
	if err != nil {
		return 0, "", fmt.Errorf("reading %s from the store: %w", key, err)
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// digests is a file's size, SHA-256 and MD5, read once.
func digests(path string) (int64, string, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", "", err
	}
	defer f.Close()
	s, m := sha256.New(), md5.New()
	n, err := io.Copy(io.MultiWriter(s, m), f)
	if err != nil {
		return 0, "", "", err
	}
	return n, hex.EncodeToString(s.Sum(nil)), hex.EncodeToString(m.Sum(nil)), nil
}
