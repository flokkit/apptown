package verification

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// UpdateInputMoves names observed toolchain and image moves in stable order.
// It consumes the same committed-tree facts as the routine's scope check.
func UpdateInputMoves(before, after map[string]any) []workflow.PackageMove {
	var tools, images []workflow.PackageMove
	keys := make([]string, 0, len(before))
	for k := range before {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	add := func(out *[]workflow.PackageMove, ecosystem, name, from, to string) {
		if from != "" && to != "" && from != to {
			*out = append(*out, workflow.PackageMove{Ecosystem: ecosystem, Package: name, From: from, To: to})
		}
	}
	for _, k := range keys {
		a, b := before[k], after[k]
		base := filepath.Base(k)
		switch a := a.(type) {
		case dockerInputs:
			next, ok := b.(dockerInputs)
			if !ok {
				continue
			}
			for i, old := range a.Stages {
				if i < len(next.Stages) && old.External && next.Stages[i].External {
					add(&images, "docker", imagePackage(old.Operand), old.Operand, next.Stages[i].Operand)
				}
			}
		case map[string]any:
			if !strings.HasSuffix(base, ".toml") && filepath.ToSlash(k) != ".apptown/app.yaml" {
				continue
			}
			next, ok := b.(map[string]any)
			if !ok {
				continue
			}
			names := make([]string, 0, len(a))
			for name := range a {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				old, ok := tomlVersion(a[name])
				value, valid := tomlVersion(next[name])
				if ok && valid {
					if filepath.ToSlash(k) == ".apptown/app.yaml" {
						if strings.HasSuffix(name, "/image") {
							add(&images, "docker", imagePackage(old), old, value)
						}
					} else {
						add(&tools, "toolchain", name, old, value)
					}
				}
			}
		case string:
			next, ok := b.(string)
			if !ok {
				continue
			}
			if k == "staticServer" {
				add(&images, "docker", imagePackage(a), a, next)
			} else if base == ".tool-versions" {
				versions := map[string]string{}
				for _, line := range strings.Split(next, "\n") {
					f := strings.Fields(line)
					if len(f) == 2 {
						versions[f[0]] = f[1]
					}
				}
				for _, line := range strings.Split(a, "\n") {
					f := strings.Fields(line)
					if len(f) == 2 {
						add(&tools, "toolchain", f[0], f[1], versions[f[0]])
					}
				}
			} else if strings.HasPrefix(base, ".") && strings.HasSuffix(base, "-version") {
				add(&tools, "toolchain", strings.TrimSuffix(strings.TrimPrefix(base, "."), "-version"), strings.TrimSpace(a), strings.TrimSpace(next))
			}
		}
	}
	return append(tools, images...)
}

// tomlVersion is a tool's version in a mise-style declaration: the string
// itself, or the version key of a structured table ([tools.node] version = …),
// whose other options are not versions.
func tomlVersion(v any) (string, bool) {
	if table, ok := v.(map[string]any); ok {
		v = table["version"]
	}
	s, ok := v.(string)
	return s, ok
}

// The tag and digest remain in From/To; the row and title name the image.
func imagePackage(reference string) string {
	repository, _, _ := strings.Cut(reference, "@")
	if tag := strings.LastIndex(repository, ":"); tag > strings.LastIndex(repository, "/") {
		repository = repository[:tag]
	}
	repository = strings.TrimPrefix(repository, "index.docker.io/")
	return strings.TrimPrefix(repository, "library/")
}
