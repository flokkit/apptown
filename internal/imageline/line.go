// Package imageline selects stable container builds using registry tags and
// manifest HEADs. It has no application state or stock image defaults.
package imageline

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

type Scope string

const (
	Line  Scope = "line"
	Patch Scope = "patch"
	Minor Scope = "minor"
	Major Scope = "major"
)

type Access struct {
	Mirror string
	// Transport is used for tag lists and manifest HEADs. Nil uses the
	// registry client's default transport.
	Transport http.RoundTripper
}
type Answer struct {
	Tag      string `json:"tag"`
	Digest   string `json:"digest"`
	Registry string `json:"registry"`
}

func (a Answer) Image() string { return a.Tag + "@" + a.Digest }

var version = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*`)

type selection struct {
	Repository      name.Repository
	Prefix, Variant string
	Scope           Scope
}

// Parse normalizes a line, retaining the tag's exact variant.
func Parse(ref string, scope Scope) (selection, error) {
	switch scope {
	case Line, Patch, Minor, Major:
	default:
		return selection{}, fmt.Errorf("unknown image scope %q: use line, patch, minor or major", scope)
	}
	operand, _, _ := strings.Cut(ref, "@")
	tag, err := name.NewTag(operand)
	if err != nil {
		return selection{}, err
	}
	prefix := version.FindString(tag.TagStr())
	variant := strings.TrimPrefix(tag.TagStr(), prefix)
	if prefix != "" {
		parts := strings.Split(prefix, ".")
		switch scope {
		case Minor:
			parts = parts[:1]
		case Patch:
			if len(parts) > 2 {
				parts = parts[:2]
			}
		case Major:
			parts = nil
		}
		prefix = strings.Join(parts, ".")
	}
	return selection{tag.Context(), prefix, variant, scope}, nil
}
func (s selection) Key() string {
	return s.Repository.Name() + ":" + s.Prefix + s.Variant + "@" + string(s.Scope)
}
func newer(a, b string) bool {
	x, y := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(x) && i < len(y); i++ {
		xx, _ := new(big.Int).SetString(x[i], 10)
		yy, _ := new(big.Int).SetString(y[i], 10)
		if c := xx.Cmp(yy); c != 0 {
			return c > 0
		}
	}
	if len(x) != len(y) {
		return len(x) > len(y)
	}
	return a > b // deterministic even for noncanonical numeric spellings
}

// Newest reads a tags list (including every page), then the chosen manifest's
// index digest with HEAD. Docker Hub alone uses a bound mirror; reads are anonymous.
func Newest(ctx context.Context, ref string, scope Scope, access Access) (Answer, error) {
	s, err := Parse(ref, scope)
	if err != nil {
		return Answer{}, err
	}
	repo := s.Repository
	if repo.RegistryStr() == name.DefaultRegistry && access.Mirror != "" {
		u, err := url.Parse(access.Mirror)
		if err != nil || u.Host == "" || u.Path != "" && u.Path != "/" || u.Scheme != "https" && u.Scheme != "http" {
			return Answer{}, fmt.Errorf("invalid Docker Hub mirror URL %q", access.Mirror)
		}
		opts := []name.Option{}
		if u.Scheme == "http" {
			opts = append(opts, name.Insecure)
		}
		repo, err = name.NewRepository(u.Host+"/"+repo.RepositoryStr(), opts...)
		if err != nil {
			return Answer{}, err
		}
	}
	options := []remote.Option{remote.WithContext(ctx)}
	if access.Transport != nil {
		options = append(options, remote.WithTransport(access.Transport))
	}
	tags, err := remote.List(repo, options...)
	if err != nil {
		return Answer{}, fmt.Errorf("%s: %w", repo.RegistryStr(), err)
	}
	original, _ := name.NewTag(strings.Split(ref, "@")[0])
	numeric := version.FindString(original.TagStr()) != ""
	chosen, best := "", ""
	for _, tag := range tags {
		v := version.FindString(tag)
		if !numeric {
			if tag == original.TagStr() {
				chosen = tag
			}
			continue
		}
		if v == "" || strings.TrimPrefix(tag, v) != s.Variant || s.Prefix != "" && v != s.Prefix && !strings.HasPrefix(v, s.Prefix+".") {
			continue
		}
		if chosen == "" || newer(v, best) {
			chosen, best = tag, v
		}
	}
	if chosen == "" {
		return Answer{}, fmt.Errorf("%s: no stable tag on %s", repo.RegistryStr(), s.Key())
	}
	desc, err := remote.Head(repo.Tag(chosen), options...)
	if err != nil {
		return Answer{}, fmt.Errorf("%s: %w", repo.RegistryStr(), err)
	}
	return Answer{Tag: s.Repository.Tag(chosen).Name(), Digest: desc.Digest.String(), Registry: repo.RegistryStr()}, nil
}
