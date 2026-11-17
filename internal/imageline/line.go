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
	repo, options, err := registryAccess(ctx, s.Repository, access)
	if err != nil {
		return Answer{}, err
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

// Digest reads the current tag's digest without a cached registry reading.
func Digest(ctx context.Context, ref string, access Access) (string, error) {
	operand, _, _ := strings.Cut(ref, "@")
	tag, err := name.NewTag(operand)
	if err != nil {
		return "", err
	}
	repo, options, err := registryAccess(ctx, tag.Context(), access)
	if err != nil {
		return "", err
	}
	desc, err := remote.Head(repo.Tag(tag.TagStr()), options...)
	if err != nil {
		return "", fmt.Errorf("%s: %w", repo.RegistryStr(), err)
	}
	return desc.Digest.String(), nil
}

func registryAccess(ctx context.Context, original name.Repository, access Access) (name.Repository, []remote.Option, error) {
	repo := original
	if repo.RegistryStr() == name.DefaultRegistry && access.Mirror != "" {
		u, err := url.Parse(access.Mirror)
		if err != nil || u.Host == "" || u.Path != "" && u.Path != "/" || u.Scheme != "https" && u.Scheme != "http" {
			return name.Repository{}, nil, fmt.Errorf("invalid Docker Hub mirror URL %q", access.Mirror)
		}
		opts := []name.Option{}
		if u.Scheme == "http" {
			opts = append(opts, name.Insecure)
		}
		repo, err = name.NewRepository(u.Host+"/"+repo.RepositoryStr(), opts...)
		if err != nil {
			return name.Repository{}, nil, err
		}
	}
	options := []remote.Option{remote.WithContext(ctx)}
	if access.Transport != nil {
		options = append(options, remote.WithTransport(access.Transport))
	}
	return repo, options, nil
}

// UpdateLine chooses the longest numeric declaration agreeing with the current
// tag on their shared components. Other images retain the supplied scope.
func UpdateLine(ref string, scope Scope, declarations []string) (string, Scope, error) {
	s, err := Parse(ref, Line)
	if err != nil {
		return "", scope, err
	}
	chosen := ""
	for _, d := range declarations {
		if version.FindString(d) != d || d == "" || s.Prefix == "" {
			continue
		}
		if (prefix(d, s.Prefix) || prefix(s.Prefix, d)) && (chosen == "" || len(strings.Split(d, ".")) > len(strings.Split(chosen, "."))) {
			chosen = d
		}
	}
	if chosen == "" {
		return ref, scope, nil
	}
	return s.Repository.Tag(chosen + s.Variant).Name(), Line, nil
}

func prefix(value, line string) bool { return value == line || strings.HasPrefix(value, line+".") }

// Allows checks the selected line and prevents a version downgrade. The caller
// separately checks repository, variant and registry digest evidence.
func Allows(old, next, line string, scope Scope) error {
	s, err := Parse(line, scope)
	if err != nil {
		return err
	}
	a, err := Parse(old, Line)
	if err != nil {
		return err
	}
	b, err := Parse(next, Line)
	if err != nil {
		return err
	}
	if a.Repository != b.Repository || a.Variant != b.Variant ||
		s.Prefix != "" && !prefix(b.Prefix, s.Prefix) ||
		a.Prefix == "" && b.Prefix != "" || a.Prefix != "" && b.Prefix != "" && newer(a.Prefix, b.Prefix) {
		return fmt.Errorf("image %s must stay on %s without a downgrade", next, s.Key())
	}
	return nil
}
