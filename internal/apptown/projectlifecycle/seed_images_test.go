package projectlifecycle

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/flokkit/apptown/internal/apptown/imagelines"
	"github.com/flokkit/apptown/internal/imageline"
	"github.com/flokkit/apptown/stock"
)

type seedResolver struct{ refused bool }

func (r seedResolver) Answer(_ context.Context, ref string, scope imageline.Scope) (imagelines.Reading, error) {
	if r.refused {
		return imagelines.Reading{}, errors.New("registry.test unreachable")
	}
	s, err := imageline.Parse(ref, scope)
	if err != nil {
		return imagelines.Reading{}, err
	}
	tag := s.Prefix + s.Variant
	if ref == "python:3.12-slim" {
		tag = "3.12.99-slim"
	}
	return imagelines.Reading{Answer: imageline.Answer{Tag: s.Repository.Tag(tag).Name(), Digest: "sha256:" + strings.Repeat("a", 64), Registry: "registry.test"}, ReadAt: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC), Stale: true, Error: "registry offline"}, nil
}
func TestSeedImagesArePinnedBeforeAnyFileIsWritten(t *testing.T) {
	t.Parallel()
	seed := fstest.MapFS{
		"Dockerfile":        {Data: []byte("FROM --platform=linux/amd64 python:3.12-slim AS build\nFROM scratch\nFROM build AS final\n")},
		".apptown/app.yaml": {Data: []byte("app:\n  web: {static: ., public: true}\n")},
		"a.txt":             {Data: []byte("starter")},
	}
	dir := t.TempDir()
	written, _, images, err := writePackSeeds(t.Context(), dir, seed, seedResolver{})
	if err != nil || len(written) != 3 || len(images) != 1 {
		t.Fatalf("%v %+v %v", written, images, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "FROM --platform=linux/amd64 index.docker.io/library/python:3.12.99-slim@sha256:"+strings.Repeat("a", 64)+" AS build\nFROM scratch\nFROM build AS final\n" {
		t.Fatal(string(raw))
	}
	raw, err = os.ReadFile(filepath.Join(dir, ".apptown/app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// A static seed is served from the line (imagelines.StaticServer):
	// creation writes no staticServer: pin for it.
	if string(raw) != "app:\n  web: {static: ., public: true}\n" {
		t.Fatalf("the static seed's contract was changed: %s", raw)
	}
	for _, im := range images {
		if !im.Stale || im.ReadAt.IsZero() || !strings.Contains(im.Evidence, "registry unreachable") {
			t.Fatal(im)
		}
	}
	refused := t.TempDir()
	if _, _, _, err := writePackSeeds(t.Context(), refused, seed, seedResolver{refused: true}); err == nil || !strings.Contains(err.Error(), "registry.test unreachable") || !strings.Contains(err.Error(), "python:3.12-slim") {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(refused)
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial seed %v %v", entries, err)
	}
}
func TestExplicitSeedDigestsRemainTheAuthorsPin(t *testing.T) {
	t.Parallel()
	const body = "FROM example.test/own/app:1@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"
	dir := t.TempDir()
	const contract = "staticServer: caddy:2.11.9-alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\napp:\n  web: {static: ., public: true}\n"
	_, _, images, err := writePackSeeds(t.Context(), dir, fstest.MapFS{"Dockerfile": {Data: []byte(body)}, ".apptown/app.yaml": {Data: []byte(contract)}}, seedResolver{refused: true})
	if err != nil || len(images) != 0 {
		t.Fatalf("%+v %v", images, err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, ".apptown/app.yaml")); err != nil || string(raw) != contract {
		t.Fatalf("the author's static server pin: %s %v", raw, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil || string(raw) != body {
		t.Fatalf("%s %v", raw, err)
	}
}
func TestStockSeedFromOperandsNameLines(t *testing.T) {
	t.Parallel()
	err := fs.WalkDir(stock.FS, "seeds", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Base(path) != "Dockerfile" {
			return nil
		}
		raw, err := fs.ReadFile(stock.FS, path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 || !strings.EqualFold(f[0], "FROM") {
				continue
			}
			s, err := imageline.Parse(f[1], imageline.Line)
			if err != nil {
				return err
			}
			if strings.Contains(f[1], "@") || len(strings.Split(s.Prefix, ".")) >= 3 {
				t.Errorf("%s names an exact build: %s", path, line)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSeedPinsOnlyExternalFromInstructions(t *testing.T) {
	t.Parallel()
	body := "FROM --platform=$BUILDPLATFORM \\\n  example.test/team/python:3.12-slim AS build\nRUN echo hello \\\nFROM pretend:1\nRUN cat <<'SCRIPT'\nFROM heredoc:1\nSCRIPT\nFROM build AS final\nFROM ${BASE}\n"
	var refs []string
	got, err := pinDockerfile([]byte(body), func(ref string) (string, error) {
		refs = append(refs, ref)
		return ref + "@sha256:" + strings.Repeat("a", 64), nil
	})
	if err != nil || len(refs) != 1 || refs[0] != "example.test/team/python:3.12-slim" {
		t.Fatalf("%s %v %v", got, refs, err)
	}
	want := strings.Replace(body, refs[0], refs[0]+"@sha256:"+strings.Repeat("a", 64), 1)
	if string(got) != want {
		t.Fatalf("operands or formatting changed:\n%s", got)
	}
}

func (r seedResolver) Digest(context.Context, string) (string, error) {
	return "sha256:" + strings.Repeat("a", 64), nil
}

func (r seedResolver) Cached(ctx context.Context, ref string, scope imageline.Scope) (imagelines.Reading, bool, error) {
	a, e := r.Answer(ctx, ref, scope)
	return a, e == nil, e
}
