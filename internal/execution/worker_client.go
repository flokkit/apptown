package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// WorkerClient is AppTown's one transport to a worker: every request, a
// task's or a chat session's, is built, sent and refused here. Its client
// must present the installation's certificate. What each endpoint means
// stays with WorkerTasks and the worker's SessionClient.
type WorkerClient struct {
	URL    string
	Client *http.Client
}

// Do sends one request to path on the worker. A request that got no answer
// is ErrWorkerUnavailable: what the worker did is unknown.
func (c WorkerClient) Do(ctx context.Context, method, path string, body io.Reader, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrWorkerUnavailable, err)
	}
	return resp, nil
}

// Call sends in (none when nil) as JSON with method to path and decodes
// the answer, read to at most limit bytes, into out (when not nil). An
// answer of 300 or over is refuse's error; a broken answer is
// ErrWorkerUnavailable.
func (c WorkerClient) Call(ctx context.Context, method, path string, in, out any, limit int64, refuse func(*http.Response) error) error {
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	return c.Send(ctx, method, path, body, "application/json", out, limit, refuse)
}

// Send is Call with a raw body of contentType.
func (c WorkerClient) Send(ctx context.Context, method, path string, body io.Reader, contentType string, out any, limit int64, refuse func(*http.Response) error) error {
	resp, err := c.Do(ctx, method, path, body, http.Header{"Content-Type": {contentType}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return refuse(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, limit)).Decode(out); err != nil {
		return fmt.Errorf("%w: reading its answer: %w", ErrWorkerUnavailable, err)
	}
	return nil
}

// EnsureBlob sends a blob (a tar with one entry named "content", named by
// its SHA-256) unless the worker already has it. Task inputs and a chat's
// mounts travel this way.
func (c WorkerClient) EnsureBlob(ctx context.Context, sum string, tar []byte) error {
	resp, err := c.Do(ctx, http.MethodHead, "/v1/blobs/"+sum, nil, nil)
	if err != nil {
		return err
	}
	err = nil
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		err = WorkerRefusal(resp)
	}
	resp.Body.Close()
	if err != nil || resp.StatusCode == http.StatusOK {
		return err
	}
	return c.Send(ctx, http.MethodPut, "/v1/blobs/"+sum, bytes.NewReader(tar), "application/x-tar", nil, 0, WorkerRefusal)
}
