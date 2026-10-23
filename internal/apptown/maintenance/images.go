package maintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"log"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/clock"
)

// ---- images ---------------------------------------------------------------
//
// The container sweep leaves the IMAGES a landing builds: one preview-build
// image per run and one AppTown-conform image per commit. Neither is ever
// used again once its owner is finished — a run's preview is never booted
// again, a conformance image is rebuilt from the commit — and left alone
// they fill a disk within a day. Same rule as containers: an image whose
// owner is finished is reaped. A change's preview's build images are
// Preview's: kept thirty days after the last look at it, for a reopen
// (preview.Service.Tend). An environment's image, the agent image and
// anything not AppTown's are never touched.

const conformImageTTL = 24 * time.Hour

// ownerRecord is what the reaper read of an image's owner: its state, or
// that it no longer exists (its project was deleted). A failed read is
// neither, and keeps the image.
type ownerRecord[S any] struct {
	State S
	Gone  bool
	Known bool
}

// reapImageDecision says whether one image is reapable, given its owner's
// record. created is when the image was last made or tagged here (see
// Images). Pure, so it is tested without a daemon. An owner that no longer
// exists is finished: a deleted project's images were otherwise kept
// forever. An image a container still uses is not removed whatever this
// says: the removal does not force.
func reapImageDecision(ref string, created, now time.Time, runStatus func(ids.ID) ownerRecord[workflow.RunState]) (reap bool, why string) {
	name := ref
	if i := strings.LastIndex(name, ":"); i > 0 {
		name = name[:i]
	}
	switch {
	case runBuildPrefix(name) != "":
		id := previewOwnerID(name, runBuildPrefix(name))
		if id.IsZero() {
			return false, ""
		}
		st := runStatus(id)
		switch {
		case !st.Known:
			return false, ""
		case st.Gone:
			return true, "its run no longer exists"
		case st.State == 0 || st.State == workflow.RunRunning:
			return false, ""
		}
		return true, "its run " + st.State.String()
	case conformImage(ref):
		if !created.IsZero() && now.Sub(created) > conformImageTTL {
			return true, "conformance image older than a day; rebuilt from the commit on demand"
		}
	}
	return false, ""
}

func conformImage(ref string) bool {
	return strings.HasPrefix(ref, "apptown-conform:") || strings.HasPrefix(ref, "forge-conform:")
}

// lastTagged is when Docker last tagged ref: a build, a rebuild from cache
// or a reuse that tags it again. Zero when Docker cannot say.
func (r *Reaper) lastTagged(ctx context.Context, ref string) time.Time {
	out, err := r.docker.Run(ctx, "image", "inspect", "--format", "{{json .Metadata.LastTagTime}}", ref)
	if err != nil {
		return time.Time{}
	}
	var t time.Time
	if json.Unmarshal(out, &t) != nil {
		return time.Time{}
	}
	return t
}

// runBuildPrefix is the run-preview build-image prefix name carries ("" when
// none).
func runBuildPrefix(name string) string {
	if pre := string(docker.Current) + "preview-build-" + string(docker.Current) + "preview-run-"; strings.HasPrefix(name, pre) {
		return pre
	}
	return ""
}

// previewOwnerID reads the owner id after prefix: the whole rest, or the
// rest up to a part's suffix (apptown-preview-run-<id>-web is the run's web
// part). Nil when it is not an id.
func previewOwnerID(name, prefix string) ids.ID {
	return leadingID(strings.TrimPrefix(name, prefix))
}

// leadingID reads the id that starts s: its 36 characters, then the end or
// a "-" before a suffix. Nil when absent.
func leadingID(s string) ids.ID {
	if len(s) < 36 || (len(s) > 36 && s[36] != '-') {
		return ids.Nil
	}
	id, _ := ids.Parse(s[:36])
	return id
}

// parseTrailingID reads the trailing -<id> of a name; Nil when absent.
func parseTrailingID(name string) ids.ID {
	if len(name) < 37 || name[len(name)-37] != '-' {
		return ids.Nil
	}
	id, _ := ids.Parse(name[len(name)-36:])
	return id
}

// reapImages sweeps AppTown's build images once. Returns what it reaped
// (or would have, in log-only mode).
func (r *Reaper) Images(ctx context.Context, logOnly bool) []string {
	if r.docker.Absent() {
		return nil // no local Docker, nothing of it to reap
	}
	out, err := r.docker.Run(ctx, "image", "ls", "--format", "{{.Repository}}:{{.Tag}}\t{{.CreatedAt}}")
	if err != nil {
		return nil
	}
	runStatus := func(id ids.ID) ownerRecord[workflow.RunState] {
		r, err := workflow.ReadRunIn(ctx, r.db, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ownerRecord[workflow.RunState]{Gone: true, Known: true}
		}
		if err != nil || r == nil {
			return ownerRecord[workflow.RunState]{}
		}
		return ownerRecord[workflow.RunState]{State: r.Status, Known: true}
	}
	var reaped []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.SplitN(line, "\t", 2)
		if len(f) < 1 || f[0] == "" {
			continue
		}
		created := time.Time{}
		if len(f) > 1 {
			// Preserve Docker's UTC offset and retain images whose age is unknown.
			if t, err := time.Parse("2006-01-02 15:04:05 -0700 MST", strings.TrimSpace(f[1])); err == nil {
				created = t
			}
		}
		// A rebuild from cache keeps the image's old creation time, so a
		// conformance image built minutes ago can look a day old. The last
		// time Docker tagged it says when it was last built or reused.
		if conformImage(f[0]) {
			if tagged := r.lastTagged(ctx, f[0]); tagged.After(created) {
				created = tagged
			}
		}
		// The daemon stamped the image's creation and tag on the machine's clock.
		ok, why := reapImageDecision(f[0], created, clock.System().UTC(), runStatus)
		if !ok {
			continue
		}
		if logOnly {
			reaped = append(reaped, f[0])
			log.Printf("reaper: WOULD remove image %s (%s)", f[0], why)
			continue
		}
		// An image a container still uses stays, and that is no news: it
		// is tried again each sweep, and said only once it goes.
		switch inUse, err := r.docker.RemoveImageUnlessUsed(ctx, f[0]); {
		case err != nil:
			log.Printf("reaper: image %s not removed: %v", f[0], err)
		case !inUse:
			reaped = append(reaped, f[0])
			log.Printf("reaper: removed image %s (%s)", f[0], why)
		}
	}
	return reaped
}
