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
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/imageline"
	"gopkg.in/yaml.v3"
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
	if raw, ok := files[".apptown/app.yaml"]; ok {
		c, err := appcontract.Parse(raw)
		if err != nil {
			return nil, err
		}
		if c.HasStaticParts() && c.StaticServer == "" {
			to, err := answer(appcontract.StaticServerLine)
			if err != nil {
				return nil, err
			}
			var doc yaml.Node
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				return nil, err
			}
			fields := doc.Content[0].Content
			replaced := false
			for i := 0; i < len(fields); i += 2 {
				if fields[i].Value == "staticServer" {
					fields[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: to}
					replaced = true
				}
			}
			if !replaced {
				doc.Content[0].Content = append(fields, &yaml.Node{Kind: yaml.ScalarNode, Value: "staticServer"}, &yaml.Node{Kind: yaml.ScalarNode, Value: to})
			}

			files[".apptown/app.yaml"], err = yaml.Marshal(&doc)
			if err != nil {
				return nil, err
			}
		}
	}
	return images, nil
}
