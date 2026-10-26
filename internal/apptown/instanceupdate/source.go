package instanceupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

var errMetadataMissing = errors.New("release lacks authenticated update metadata")
var errAuthentication = errors.New("release authentication failed")

type releaseMetadata struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

type pageCache struct {
	ETag string          `json:"etag"`
	Body json.RawMessage `json:"body"`
}

type releaseSource struct {
	http   publicHTTP
	verify releaseVerifier
}

// discover uses only fixed public paths. Release bodies, author names and API
// asset URLs are not authority inputs; notes come from the signed checksum set.
func (s releaseSource) discover(ctx context.Context, channel string, cached map[string]pageCache) (string, map[string]pageCache, error) {
	if channel != "stable" && channel != "prerelease" {
		return "", nil, fmt.Errorf("unsupported release channel")
	}
	pages := map[string]pageCache{}
	seen := map[string]bool{}
	best := ""
	for page := 1; page <= 5; page++ {
		key := strconv.Itoa(page)
		previous := cached[key]
		if len(previous.Body) == 0 {
			previous.ETag = ""
		}
		body, etag, status, err := s.http.get(ctx, "https://api.github.com/repos/flokkit/apptown/releases?per_page=100&page="+key, previous.ETag, maxDocument)
		if err != nil {
			return "", nil, err
		}
		if status == http.StatusNotModified {
			if len(previous.Body) == 0 {
				return "", nil, fmt.Errorf("release server returned unchanged without cached metadata")
			}
			body = previous.Body
			etag = previous.ETag
		}
		var releases []releaseMetadata
		if err := json.Unmarshal(body, &releases); err != nil {
			return "", nil, fmt.Errorf("release metadata is invalid")
		}
		if releases == nil {
			return "", nil, fmt.Errorf("release metadata is not a list")
		}
		if len(releases) > 100 {
			return "", nil, fmt.Errorf("release page exceeds advertised limit")
		}
		pages[key] = pageCache{ETag: etag, Body: json.RawMessage(body)}
		for _, r := range releases {
			if r.Draft {
				continue
			}
			if !validTag(r.Tag) {
				return "", nil, fmt.Errorf("published release has an unsupported version")
			}
			if seen[r.Tag] {
				return "", nil, fmt.Errorf("duplicate published release identity")
			}
			seen[r.Tag] = true
			if channel == "stable" && (r.Prerelease || semver.Prerelease(r.Tag) != "") {
				continue
			}
			if best == "" || semver.Compare(r.Tag, best) > 0 {
				best = r.Tag
			} else if r.Tag != best && semver.Compare(r.Tag, best) == 0 {
				return "", nil, fmt.Errorf("published releases have ambiguous version precedence")
			}
		}
		if len(releases) < 100 {
			return best, pages, nil
		}
	}
	return "", nil, fmt.Errorf("release pagination is incomplete")
}

func (s releaseSource) fetch(ctx context.Context, tag string) (signedRelease, error) {
	if !validTag(tag) {
		return signedRelease{}, fmt.Errorf("invalid release identity")
	}
	base := "https://github.com/flokkit/apptown/releases/download/" + url.PathEscape(tag) + "/"
	documents := make(map[string][]byte, 4)
	for _, name := range []string{ManifestName, ChecksumsName, BundleName, NotesName} {
		limit := int64(maxDocument)
		if name == ChecksumsName {
			limit = 16 << 10
		}
		if name == NotesName {
			limit = 256 << 10
		}
		body, _, status, err := s.http.get(ctx, base+name, "", limit)
		if err != nil {
			if status == http.StatusNotFound {
				return signedRelease{}, errMetadataMissing
			}
			return signedRelease{}, err
		}
		documents[name] = body
	}
	release, err := s.verify.verify(ctx, tag, documents[ManifestName], documents[ChecksumsName], documents[BundleName], documents[NotesName])
	if err != nil {
		return signedRelease{}, fmt.Errorf("%w: %w", errAuthentication, err)
	}
	if strings.TrimSpace(string(release.Notes)) == "" {
		return signedRelease{}, fmt.Errorf("%w: authenticated release notes are empty", errAuthentication)
	}
	return release, nil
}
