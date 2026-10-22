package portainer

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/flokkit/apptown/internal/deploy"
)

// StackVolume is a volume a stack's compose file declares, as the
// environment names it, and the need whose data it keeps ("" for an app's
// or a part's data folder).
type StackVolume struct {
	Name, Key, Need string
}

// ComposeVolumes are the volumes a compose file declares for stack (not
// external): <stack>_<key>, or the name the file sets. A need's volume key
// is <prefix>need-<base>-<need> (StackNames).
func ComposeVolumes(stack, base, compose string) ([]StackVolume, error) {
	var file struct {
		Volumes map[string]*struct {
			Name     string `yaml:"name"`
			External any    `yaml:"external"`
		} `yaml:"volumes"`
	}
	if err := yaml.Unmarshal([]byte(compose), &file); err != nil {
		return nil, err
	}
	var out []StackVolume
	for key, v := range file.Volumes {
		if v != nil && v.External != nil && v.External != false {
			continue
		}
		name := stack + "_" + key
		if v != nil && v.Name != "" {
			name = v.Name
		}
		// The stack is <prefix><base>; its need volumes <prefix>need-<base>-<need>.
		need, _ := strings.CutPrefix(key, strings.TrimSuffix(stack, base)+"need-"+base+"-")
		if need == key {
			need = ""
		}
		out = append(out, StackVolume{Name: name, Key: key, Need: need})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// volumeThere asks the environment's Docker whether a volume exists: only
// its own "no such volume" (404) is absence.
func volumeThere(ctx context.Context, cfg *Target, endpointID int64, name string) (bool, error) {
	code, raw, err := JSON(ctx, cfg, "GET", fmt.Sprintf("/api/endpoints/%d/docker/volumes/%s", endpointID, url.PathEscape(name)), nil)
	switch {
	case err != nil:
		return false, err
	case code == 200:
		return true, nil
	case code == 404:
		return false, nil
	}
	return false, fmt.Errorf("asking for the volume %s: HTTP %d %s", name, code, strings.TrimSpace(string(raw)))
}

// recordMadeVolumes records on the deployment's list each volume the
// compose declares that was not there before the deploy and is there now:
// AppTown's deploy made it. One that was there before is not recorded
// (nothing found is adopted).
func recordMadeVolumes(ctx context.Context, cfg *Target, endpointID int64, before map[string]bool, vols []StackVolume, owner deploy.Owner, logf func(string, ...any)) error {
	for _, v := range vols {
		if before[v.Name] {
			continue
		}
		there, err := volumeThere(ctx, cfg, endpointID, v.Name)
		if err != nil {
			logf("whether the stack made the volume %s is not known (%v); it is not recorded as the deployment's", v.Name, err)
			continue
		}
		if !there {
			continue
		}
		if err := owner.Created(ctx, deploy.Owned{Kind: "volume", ID: v.Name, Name: v.Name, Data: true, Need: v.Need}); err != nil {
			return fmt.Errorf("recording the volume %s the stack made: %w", v.Name, err)
		}
	}
	return nil
}

// deleteVolume deletes a volume in the environment; one already gone is
// deleted.
func deleteVolume(ctx context.Context, cfg *Target, endpointID int64, name string) error {
	code, raw, err := JSON(ctx, cfg, "DELETE", fmt.Sprintf("/api/endpoints/%d/docker/volumes/%s", endpointID, url.PathEscape(name)), nil)
	switch {
	case err != nil:
		return err
	case code == 204 || code == 200 || code == 404:
		return nil
	}
	return fmt.Errorf("HTTP %d %s", code, strings.TrimSpace(string(raw)))
}
