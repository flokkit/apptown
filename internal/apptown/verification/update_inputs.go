package verification

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"

	"github.com/google/go-containerregistry/pkg/name"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// UpdateInputs records the tool declarations and image inputs from a committed
// tree. The routine check and standing assessment compare these independently
// of the model’s scope judgement.
func UpdateInputs(tree string) (map[string]any, error) {
	out := map[string]any{}
	err := filepath.WalkDir(tree, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		base := filepath.Base(name)
		config := base == "mise.toml" || base == ".mise.toml" || (strings.HasPrefix(base, "mise.") || strings.HasPrefix(base, ".mise.")) && strings.HasSuffix(base, ".toml") || strings.Contains(filepath.ToSlash(name), "/.mise/") && strings.HasSuffix(base, ".toml")
		legacy := base == ".tool-versions" || base == ".python-version" || base == ".node-version" || base == ".ruby-version" || base == ".go-version"
		docker := base == "Dockerfile"
		contract := filepath.ToSlash(name) == filepath.ToSlash(filepath.Join(tree, ".apptown/app.yaml"))
		if !config && !legacy && !docker && !contract {
			return nil
		}
		rel, err := filepath.Rel(tree, name)
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("update input %s must be a regular file", rel)
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		switch {
		case config:
			var doc map[string]any
			if err := toml.Unmarshal(raw, &doc); err != nil {
				return err
			}
			if tools, ok := doc["tools"]; ok {
				out[rel] = tools
			}
			// Includes can change which tool declarations the build reads.
			if env, ok := doc["env"]; ok {
				out[rel+":env"] = env
			}
		case docker:
			out[rel] = dockerBaseInputs(raw)
		case contract:
			var doc any
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				return err
			}
			images := map[string]any{}
			var walk func(any, string)
			walk = func(value any, path string) {
				switch value := value.(type) {
				case map[string]any:
					for k, v := range value {
						if k == "image" || k == "build" {
							images[path+"/"+k] = v
						}
						walk(v, path+"/"+k)
					}
				case []any:
					for i, v := range value {
						walk(v, fmt.Sprintf("%s/%d", path, i))
					}
				}
			}
			walk(doc, "")
			out[rel] = images
		default:
			out[rel] = string(raw)
		}
		return nil
	})
	return out, err
}

// Only single-line, backslash-free FROM operands have a bounded relaxation.
// Other instructions may continue across lines; ambiguous escapes, multiline
// FROM/ARG, escape directives and heredocs keep the complete file exact.
// ARGs and other parser directives remain exact inputs.
type dockerInputs struct {
	Exact  string
	Lines  []string
	Stages []dockerStage
}
type dockerStage struct {
	Flags, Alias, Operand string
	External              bool
	Repository, Variant   string
}

var tagVersion = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*`)
var inputWords = regexp.MustCompile(`\S+`)
var patchVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func dockerBaseInputs(raw []byte) dockerInputs {
	exact := dockerInputs{Exact: string(raw)}
	if bytes.ContainsAny(raw, "\x00\r`") || bytes.Contains(raw, []byte("<<")) {
		return exact
	}
	for _, c := range raw {
		if c == 127 || c < 32 && c != '\n' && c != '\t' {
			return exact
		}
	}
	out := dockerInputs{}
	aliases := map[string]bool{}
	lines := strings.Split(string(raw), "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			key, _, directive := strings.Cut(strings.TrimSpace(strings.TrimPrefix(trimmed, "#")), "=")
			if directive && strings.EqualFold(strings.TrimSpace(key), "escape") {
				return exact
			}
			if directive {
				out.Lines = append(out.Lines, line)
			}
			continue
		}
		multiline := false
		for {
			end := strings.TrimRight(line, " \t")
			if strings.HasSuffix(end, `\\`) {
				// An escaped terminal backslash must never hide the next FROM/ARG.
				return exact
			}
			if !strings.HasSuffix(end, `\`) {
				break
			}
			multiline = true
			line = strings.TrimSuffix(end, `\`)
			for i++; i < len(lines); i++ {
				next := strings.TrimSpace(lines[i])
				if next != "" && !strings.HasPrefix(next, "#") {
					line += lines[i]
					break
				}
			}
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if (strings.EqualFold(fields[0], "FROM") || strings.EqualFold(fields[0], "ARG")) && (multiline || strings.Contains(line, `\`)) {
			return exact
		}
		if strings.EqualFold(fields[0], "ARG") {
			out.Lines = append(out.Lines, line)
		}
		if !strings.EqualFold(fields[0], "FROM") {
			continue
		}
		out.Lines = append(out.Lines, "FROM")
		f := fields[1:]
		stage := dockerStage{}
		for len(f) > 0 && strings.HasPrefix(f[0], "--") {
			stage.Flags += f[0] + " "
			f = f[1:]
		}
		if len(f) != 1 && !(len(f) == 3 && strings.EqualFold(f[1], "AS")) {
			return exact
		}
		stage.Operand = f[0]
		if len(f) == 3 {
			stage.Alias = f[2]
		}
		stage.External = !aliases[strings.ToLower(stage.Operand)] && !strings.EqualFold(stage.Operand, "scratch") && !strings.Contains(stage.Operand, "$")
		if stage.External {
			operand, digest, hasDigest := strings.Cut(stage.Operand, "@")
			if hasDigest {
				if _, err := name.NewDigest(operand + "@" + digest); err != nil {
					return exact
				}
			}
			ref, err := name.NewTag(operand)
			if err != nil {
				return exact
			}
			stage.Repository = ref.Context().Name()
			// An absent tag has the same variant as Docker's implicit latest.
			tag := ref.TagStr()
			stage.Variant = tagVersion.ReplaceAllString(tag, "")
		}
		out.Stages = append(out.Stages, stage)
		if stage.Alias != "" {
			aliases[strings.ToLower(stage.Alias)] = true
		}
	}
	return out
}

// CompareUpdateInputs is the shared deterministic boundary for agent updates.
// Security targets alone permit exact same-minor toolchain patch moves.
func CompareUpdateInputs(before, after map[string]any, allowToolchainPatch bool) error {
	keys := make([]string, 0, len(before)+len(after))
	for k := range before {
		keys = append(keys, k)
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		a, aok := before[k]
		b, bok := after[k]
		if aok && bok && reflect.DeepEqual(a, b) {
			continue
		}
		refuse := func(why string) error { return fmt.Errorf("%s: %s; a person must approve this change", k, why) }
		if aok && bok {
			if x, ok := a.(dockerInputs); ok {
				y, ok := b.(dockerInputs)
				if !ok || x.Exact != "" || y.Exact != "" || !reflect.DeepEqual(x.Lines, y.Lines) || len(x.Stages) != len(y.Stages) {
					return refuse("Dockerfile structure or ARG inputs changed")
				}
				for i, old := range x.Stages {
					next := y.Stages[i]
					why := ""
					switch {
					case old.Flags != next.Flags || old.Alias != next.Alias:
						why = "flags or stage alias changed"
					case !old.External || !next.External:
						if old.Operand != next.Operand {
							why = "stage, scratch or variable operand changed"
						}
					case old.Repository != next.Repository:
						why = fmt.Sprintf("%s → %s changes the image", old.Operand, next.Operand)
					case old.Variant != next.Variant:
						why = fmt.Sprintf("variant %s → %s", old.Variant, next.Variant)
					}
					if why != "" {
						return refuse(fmt.Sprintf("stage %d: %s", i+1, why))
					}
				}
				continue
			}
			if allowToolchainPatch && !strings.Contains(k, ":") && k != ".apptown/app.yaml" && toolchainPatch(a, b) {
				continue
			}
		}
		return refuse("toolchain or build inputs changed")
	}
	return nil
}

func toolchainPatch(a, b any) bool {
	switch a := a.(type) {
	case map[string]any:
		next, ok := b.(map[string]any)
		if !ok || len(a) != len(next) {
			return false
		}
		for k, v := range a {
			w, ok := next[k]
			if !ok || !reflect.DeepEqual(v, w) && !toolchainPatch(v, w) {
				return false
			}
		}
		return true
	case string:
		next, ok := b.(string)
		if !ok {
			return false
		}
		oldFields, nextFields := strings.Fields(a), strings.Fields(next)
		if len(oldFields) != len(nextFields) || inputWords.ReplaceAllString(a, "") != inputWords.ReplaceAllString(next, "") {
			return false
		}
		for i, v := range oldFields {
			w := nextFields[i]
			if v == w {
				continue
			}
			if !patchVersion.MatchString(v) || !patchVersion.MatchString(w) || v[:strings.LastIndex(v, ".")] != w[:strings.LastIndex(w, ".")] {
				return false
			}
		}
		return true
	}
	return false
}
