package verification

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// UpdateInputs records the tool declarations and image inputs from a committed
// tree before listing commands can modify it. Both the routine check and the
// standing assessment compare these independently of package listings.
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

// Simple Dockerfiles may change application instructions while retaining their
// base operands and ARG defaults. More complex syntax is compared byte for byte
// so a continuation, heredoc or parser directive cannot hide an image change.
func dockerBaseInputs(raw []byte) string {
	if bytes.ContainsAny(raw, "\\\x00\r`") || bytes.Contains(raw, []byte("<<")) {
		return string(raw)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		if strings.EqualFold(fields[0], "FROM") || strings.EqualFold(fields[0], "ARG") || strings.HasPrefix(trimmed, "#") && strings.Contains(trimmed, "=") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func CompareUpdateInputs(before, after map[string]any) error {
	if !reflect.DeepEqual(before, after) {
		return fmt.Errorf("toolchain versions or base-image inputs changed; a person must approve this change")
	}
	return nil
}
