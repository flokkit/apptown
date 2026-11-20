package projectlifecycle

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/flokkit/apptown/internal/apptown/imagelines"
	"github.com/flokkit/apptown/internal/imageline"
)

type seedImage struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	ReadAt   time.Time `json:"readAt"`
	Stale    bool      `json:"stale"`
	Evidence string    `json:"evidence"`
}

var seedWords = regexp.MustCompile(`\S+`)

func pinSeed(ctx context.Context, resolver imagelines.Resolver, files map[string][]byte) ([]seedImage, error) {
	var images []seedImage
	answer := func(from string) (string, error) {
		if resolver == nil {
			return "", fmt.Errorf("resolving %s: image registry readings are unavailable", from)
		}
		r, err := resolver.Answer(ctx, from, imageline.Line)
		if err != nil {
			return "", fmt.Errorf("resolving %s: %w", from, err)
		}
		images = append(images, seedImage{from, r.Image(), r.ReadAt, r.Stale, r.Evidence()})
		return r.Image(), nil
	}
	for _, file := range slices.Sorted(maps.Keys(files)) {
		raw := files[file]
		if filepath.Base(file) != "Dockerfile" {
			continue
		}
		pinned, err := pinDockerfile(raw, answer)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		files[file] = pinned
	}
	return images, nil
}
