package instanceupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

const maxDocument = 4 << 20

type publicHTTP struct{ client *http.Client }

func newPublicHTTP(transport http.RoundTripper) publicHTTP {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return publicHTTP{client: &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !allowedPublicURL(r.URL) {
			return fmt.Errorf("release redirect refused")
		}
		return nil
	}}}
}

func allowedPublicURL(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	switch u.Host {
	case "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com", "tuf-repo-cdn.sigstore.dev":
		return true
	}
	return false
}

func (h publicHTTP) get(ctx context.Context, rawURL, etag string, limit int64) ([]byte, string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil || !allowedPublicURL(req.URL) {
		return nil, "", 0, fmt.Errorf("release URL refused")
	}
	req.Header.Set("User-Agent", "AppTown-release-check")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, "", 0, fmt.Errorf("release request unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, resp.Header.Get("ETag"), resp.StatusCode, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", resp.StatusCode, fmt.Errorf("release server returned HTTP %d", resp.StatusCode)
	}
	if limit <= 0 || limit > maxDocument {
		limit = maxDocument
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", resp.StatusCode, fmt.Errorf("release response unreadable: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, "", resp.StatusCode, fmt.Errorf("release response exceeds limit")
	}
	return body, resp.Header.Get("ETag"), resp.StatusCode, nil
}

type trustFetcher struct {
	ctx  context.Context
	http publicHTTP
}

func (f trustFetcher) DownloadFile(rawURL string, maxLength int64, _ time.Duration) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host != "tuf-repo-cdn.sigstore.dev" || !strings.HasPrefix(u.Path, "/") {
		return nil, fmt.Errorf("trust metadata URL refused")
	}
	body, _, status, err := f.http.get(f.ctx, rawURL, "", maxLength)
	if status == http.StatusNotFound {
		return nil, &metadata.ErrDownloadHTTP{StatusCode: status}
	}
	return body, err
}

func (h publicHTTP) trustedRoot(ctx context.Context, cache string) (root.TrustedMaterial, error) {
	// DefaultRoot is the library's authenticated bootstrap, never release input.
	// The cache has its own TUF expiry checks; refresh failure is not trusted data.
	opts := tuf.DefaultOptions().WithCachePath(cache).WithCacheValidity(1).WithContext(ctx).WithFetcher(trustFetcher{ctx: ctx, http: h})
	return root.FetchTrustedRootWithOptions(opts)
}
