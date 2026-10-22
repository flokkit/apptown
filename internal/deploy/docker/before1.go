package docker

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/flokkit/apptown/internal/dockerlabel"
)

// What a host still carries from before 1.0, read while live and the demo
// move their deployments to names by key
// (design/subsystems/deployment.md, "Names on a host"): labels under the
// forge.* keys, on a standing deployment's things not yet moved and on the
// built-in database.
//
// REMOVE-AFTER resource move: this file goes whole once every deployment
// is moved and the built-in database made again.

// labelEither is a `docker ps` or `ls` format reading a label by its
// current key, or by the key a thing made before 1.0 carries.
func labelEither(key string) string {
	return `{{or (.Label "` + key + `") (.Label "` + dockerlabel.Before1(key) + `")}}`
}

// ConfigLabelEither is labelEither for a container's `docker inspect`.
func ConfigLabelEither(key string) string {
	return `{{or (index .Config.Labels "` + key + `") (index .Config.Labels "` + dockerlabel.Before1(key) + `")}}`
}

// VolumeLabelEither is labelEither for a volume's `docker volume inspect`.
func VolumeLabelEither(key string) string {
	return `{{or (index .Labels "` + key + `") (index .Labels "` + dockerlabel.Before1(key) + `")}}`
}

// ListLabelledEither runs a listing (list: "ps -aq --no-trunc", "volume ls
// -q") filtered by every key=value in labels, once under the current keys
// and once under the keys things made before 1.0 carry, and answers both
// together, each once. A failed listing is an error: it confirms nothing.
func (dk Scope) ListLabelledEither(ctx context.Context, list []string, labels [][2]string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, key := range []func(string) string{func(k string) string { return k }, dockerlabel.Before1} {
		args := append([]string{}, list...)
		for _, l := range labels {
			args = append(args, "--filter", "label="+key(l[0])+"="+l[1])
		}
		found, err := dk.List(ctx, args...)
		if err != nil {
			return nil, err
		}
		for _, f := range found {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	return out, nil
}

// BeforeKeyOwner reads an owner label of the form env-<slug>-<name>, the
// stack of a deployment named before its names were by key, against the
// projects' slugs (longest first, so a slug that begins another never wins).
func BeforeKeyOwner(owner string, slugs []string) (slug, deployment string, ok bool) {
	rest, isEnv := strings.CutPrefix(owner, beforeStackPrefix)
	if !isEnv {
		return "", "", false
	}
	for _, s := range slugs {
		if e, found := strings.CutPrefix(rest, s+"-"); found && e != "" {
			return s, e, true
		}
	}
	return "", "", false
}

// before1Label finds a forge.* key in a `docker … ls` Labels column
// (k=v,k=v).
var before1Label = regexp.MustCompile(`(?:^|,)forge\.[a-z][a-z0-9.-]*=`)

// keptBefore1 are the classes of things made before 1.0 that the sweep
// never touches: a standing deployment's (moved, not swept) and
// infrastructure (the built-in and test databases).
var keptBefore1 = map[string]bool{"env": true, "infra": true}

// SweepBefore1 removes, once at start, every disposable thing an AppTown
// before 1.0 made on this daemon: each container and network carrying a
// forge.* label whose forge.class is neither env nor infra (previews and
// their services, steps and init containers, step and chat Docker, chats,
// checks, probes, private Docker, tasks), and each volume whose forge.class
// is one of those (chat caches). A standing deployment's things and its
// data and need volumes (which carry only forge.owner) stay.
//
// Readers of the current keys cannot see these, so a folder an old
// container still writes would read as free; that is why a listing or a
// removal that fails is an error naming what is left, and the start stops.
func SweepBefore1(ctx context.Context, dk Scope) error {
	ctx = context.WithoutCancel(ctx)
	var failed []error
	class := dockerlabel.Before1(dockerlabel.Class)
	for _, kind := range []struct {
		list   []string
		remove func(context.Context, string) error
		volume bool
	}{
		{[]string{"ps", "-a", "--format", `{{.ID}}` + "\t" + `{{.Label "` + class + `"}}` + "\t" + `{{.Labels}}`}, dk.RemoveWithVolumes, false},
		{[]string{"network", "ls", "--format", `{{.ID}}` + "\t" + `{{.Label "` + class + `"}}` + "\t" + `{{.Labels}}`}, dk.RemoveNetwork, false},
		{[]string{"volume", "ls", "--format", `{{.Name}}` + "\t" + `{{.Label "` + class + `"}}` + "\t" + `{{.Labels}}`}, dk.RemoveVolume, true},
	} {
		lines, err := dk.List(ctx, kind.list...)
		if err != nil {
			failed = append(failed, err)
			continue
		}
		for _, l := range lines {
			f := strings.SplitN(l, "\t", 3)
			if len(f) < 3 || !before1Label.MatchString(f[2]) || keptBefore1[f[1]] || (kind.volume && f[1] == "") {
				continue
			}
			if err := kind.remove(ctx, f[0]); err != nil {
				failed = append(failed, err)
			}
		}
	}
	if err := errors.Join(failed...); err != nil {
		return fmt.Errorf("removing what an AppTown before 1.0 left on this Docker: %w", err)
	}
	return nil
}
