package s3bucket

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/strs"
	"github.com/flokkit/apptown/internal/trust"
)

// Private objects: what a backup store holds. Unlike the website's
// PutObject, which makes every object readable by anyone, these are written
// private, and a stored file is checked after it is written: the store's
// own answer for its size and its ETag must match the file sent, and the
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
// It trusts the AppTown use (internal/trust): the store is a provider API.
var transport = trust.Lazy(trust.AppTown)

// PutPrivate writes a small object, private.
func (c *Client) PutPrivate(ctx context.Context, bucket, key, contentType string, body []byte) error {
	return c.do(ctx, "PUT", bucket, key, nil, map[string]string{"Content-Type": contentType, "x-amz-acl": "private", MetaSHA256: sha256Hex(body)}, body, nil)
}

// partSize is the size of each part of an upload but the last. S3 and
// Spaces take up to 10,000 parts, so a file of up to 625 GiB.
const partSize = 64 << 20

// PutPrivateFile streams a file into the store, private, as a multipart
// upload of partSize parts (a small file is one part): one way for every
// size, with no single-request limit. Each part is sent with its SHA-256 as
// the signed payload hash, so a store that received other bytes refuses it,
// and its ETag (the part's MD5) is checked against the part's. After the
// upload completes, what the store holds is checked: its size, and its
// ETag, which for a multipart upload is the MD5 of the parts' MD5s. The
// file's SHA-256 travels as the object's metadata, and the parts sent must
// hash to it together. On a failure after the store began the upload, the
// upload is aborted so the store keeps no parts; a failed abort is named in
// the error. (An answer to the beginning that never arrived leaves an upload
// whose id AppTown never learned.)
func (c *Client) PutPrivateFile(ctx context.Context, bucket, key, path string) (Stored, error) {
	return c.putPrivateFile(ctx, bucket, key, path, partSize)
}

func (c *Client) putPrivateFile(ctx context.Context, bucket, key, path string, partSize int64) (Stored, error) {
	f, err := os.Open(path)
	if err != nil {
		return Stored{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Stored{}, err
	}
	size := info.Size()
	whole := sha256.New()
	if _, err := io.Copy(whole, f); err != nil {
		return Stored{}, err
	}
	sum := hex.EncodeToString(whole.Sum(nil))
	var created struct {
		UploadID string `xml:"UploadId"`
	}
	if err := c.do(ctx, "POST", bucket, key, url.Values{"uploads": {""}}, map[string]string{"Content-Type": "application/octet-stream", "x-amz-acl": "private", MetaSHA256: sum}, nil, &created); err != nil {
		return Stored{}, fmt.Errorf("beginning the upload of %s to the store: %w", key, err)
	}
	if created.UploadID == "" {
		return Stored{}, fmt.Errorf("the store began no upload of %s", key)
	}
	upload := url.Values{"uploadId": {created.UploadID}}
	stored, err := c.putParts(ctx, bucket, key, upload, f, size, partSize, sum)
	if err != nil {
		// Abort on the caller's context or after it ended: the parts would
		// otherwise be kept, and billed, by the store.
		if aerr := c.do(context.WithoutCancel(ctx), "DELETE", bucket, key, upload, nil, nil, nil); aerr != nil {
			err = fmt.Errorf("%w (and aborting the upload failed: %v; the store may keep its parts)", err, aerr)
		}
		return Stored{}, err
	}
	head, err := c.Head(ctx, bucket, key)
	if err != nil {
		return Stored{}, fmt.Errorf("reading back what the store holds at %s: %w", key, err)
	}
	if head.Size != size {
		return Stored{}, fmt.Errorf("the store holds %d bytes at %s; %d were sent", head.Size, key, size)
	}
	if !strings.EqualFold(head.ETag, stored) {
		return Stored{}, fmt.Errorf("the store's digest of %s (%s) is not the file's (%s)", key, head.ETag, stored)
	}
	return Stored{Key: key, Size: size, SHA256: sum}, nil
}

// putParts uploads f in parts and completes the upload, and answers the
// ETag the store must then give the object. The parts it sent must hash,
// together, to sum, the SHA-256 the object's metadata and the caller are
// given: a file that changed while it went up is not completed.
func (c *Client) putParts(ctx context.Context, bucket, key string, upload url.Values, f *os.File, size, partSize int64, sum string) (string, error) {
	type part struct {
		PartNumber int    `xml:"PartNumber"`
		ETag       string `xml:"ETag"`
	}
	var parts []part
	var md5s []byte
	whole := sha256.New()
	for n, offset := 1, int64(0); n == 1 || offset < size; n, offset = n+1, offset+partSize {
		length := min(partSize, size-offset)
		s, m := sha256.New(), md5.New()
		if _, err := io.Copy(io.MultiWriter(s, m, whole), io.NewSectionReader(f, offset, length)); err != nil {
			return "", err
		}
		partMD5 := m.Sum(nil)
		q := url.Values{"partNumber": {strconv.Itoa(n)}, "uploadId": upload["uploadId"]}
		res, err := c.send(ctx, "PUT", bucket, key, q, nil, io.NewSectionReader(f, offset, length), length, hex.EncodeToString(s.Sum(nil)), streaming)
		if err != nil {
			return "", fmt.Errorf("writing part %d of %s to the store: %w", n, key, err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if etag := strings.Trim(res.Header.Get("ETag"), `"`); !strings.EqualFold(etag, hex.EncodeToString(partMD5)) {
			return "", fmt.Errorf("the store holds other bytes for part %d of %s: its digest %s is not the part's %x", n, key, etag, partMD5)
		}
		parts = append(parts, part{PartNumber: n, ETag: `"` + hex.EncodeToString(partMD5) + `"`})
		md5s = append(md5s, partMD5...)
	}
	if got := hex.EncodeToString(whole.Sum(nil)); got != sum {
		return "", fmt.Errorf("%s changed while it was sent to the store (its digest was %s and is now %s)", key, sum, got)
	}
	body, err := xml.Marshal(struct {
		XMLName xml.Name `xml:"CompleteMultipartUpload"`
		Parts   []part   `xml:"Part"`
	}{Parts: parts})
	if err != nil {
		return "", err
	}
	// A store may take minutes to complete a large upload, answering 200 at
	// once and its result at the end, so the completion is bounded by the
	// caller's context, not a request timeout; and it can answer 200 with an
	// error in its body.
	res, err := c.send(ctx, "POST", bucket, key, upload, map[string]string{"Content-Type": "application/xml"}, bytes.NewReader(body), int64(len(body)), sha256Hex(body), streaming)
	if err != nil {
		return "", fmt.Errorf("completing the upload of %s: %w", key, err)
	}
	answer, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	if err != nil {
		return "", fmt.Errorf("completing the upload of %s: %w", key, err)
	}
	var done struct {
		XMLName xml.Name
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if err := xml.Unmarshal(answer, &done); err != nil {
		return "", fmt.Errorf("completing the upload of %s: the store's answer is not XML: %w", key, err)
	}
	if done.XMLName.Local == "Error" {
		return "", fmt.Errorf("completing the upload of %s: %w", key, &Error{Status: 200, Code: done.Code, Message: strs.Truncate(done.Message, 200)})
	}
	all := md5.Sum(md5s)
	return hex.EncodeToString(all[:]) + "-" + strconv.Itoa(len(parts)), nil
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
