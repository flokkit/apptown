package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/strs"
)

// Inventory answers what a Docker host holds: the standing containers
// named by AppTown, its volumes and networks, and image references, as the daemon reports
// them, over the host's scope so an attached host answers for itself. It
// is built for a target, not a deployment: it can look, never boot. What
// does not carry the scope's installation label confirms records only.
type Inventory struct {
	Scope func(context.Context) (Scope, error)
	Also  []string // containers the host also reports by name (the caller's own database on its built-in host)
	// Images is the image references the deployments on the target
	// recorded (deploy.Owned of kind image), matched against the host listing.
	Images func(context.Context) ([]string, error)
}

var _ deploy.Inventorier = Inventory{}

func (i Inventory) Inventory(ctx context.Context) ([]deploy.Resource, error) {
	dk, err := i.Scope(ctx)
	if err != nil {
		return nil, err
	}
	identity, err := dk.Run(ctx, "info", "--format", "{{.ID}}")
	if err != nil {
		return nil, fmt.Errorf("reading inventory daemon identity: %w", err)
	}
	daemonID := strings.TrimSpace(string(identity))
	if daemonID == "" {
		return nil, fmt.Errorf("Docker did not identify the inventory daemon")
	}
	// AppTown's containers are known by their names (names.go), which
	// predate the labels: a service made before labelling would read as
	// missing under a label filter. AppTown's own database is asked for
	// by its name too.
	// A deployment's app is dep-<key>.
	// One listing for all of them: Docker ORs repeated name filters, and
	// each listing is an ssh round trip to an attached host, so a listing
	// per name ran past the read's deadline on a small, busy host.
	args := []string{"ps", "-a"}
	for _, kind := range []string{keyStackPrefix, "svc-", "preview-", "init-"} {
		args = append(args, "--filter", "name=^"+string(Current)+kind)
	}
	for _, name := range i.Also {
		args = append(args, "--filter", "name=^"+name+"$")
	}
	// The labels say who made a thing (apptown.owner) and which compose
	// stack it belongs to; the page attributes leftovers by them.
	args = append(args, "--format", "{{.Names}}\t{{.Status}}\t{{.Image}}\t"+ownerLabel+"\t{{.Label \"com.docker.compose.project\"}}\t"+instanceLabel)
	out, err := dk.Run(ctx, args...)
	if err != nil {
		// The error itself too: an ssh that failed before docker said
		// anything left only "docker ps: " to go on.
		return nil, fmt.Errorf("listing containers: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var res []deploy.Resource
	seen := map[string]bool{}
	for _, l := range strings.Split(strings.TrimRight(string(out), "\r\n"), "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 2 || f[0] == "" || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		status := "stopped"
		if strings.HasPrefix(f[1], "Up") {
			status = "active"
		}
		r := deploy.Resource{Kind: "container", Name: f[0], Status: status, Detail: f[1]}
		if len(f) >= 3 {
			r.Version = f[2]
		}
		if len(f) >= 5 {
			r.Owner, r.Stack = strings.TrimSpace(f[3]), strings.TrimSpace(f[4])
		}
		if len(f) < 6 || !dk.installs(strings.TrimSpace(f[5])) {
			r.Tags = []string{deploy.TagConfirmsOnly}
		}
		res = append(res, r)
	}
	// Volumes are listed whole: AppTown names data folders and need
	// volumes, but an approved older service keeps Docker's anonymous name
	// for its data (ownership_inspection.go), and a listing filtered by the
	// AppTown's prefixes would call that present volume missing. A volume the
	// AppTown did not name is reported only to confirm a record
	// (deploy.TagConfirmsOnly), never offered as an orphan. A failed kind is
	// not an empty kind: the cache retains the last complete answer with the
	// error instead of calling present resources missing.
	for _, list := range []struct{ kind, filter string }{{"volume", ""}, {"network", "net-"}} {
		args := []string{list.kind, "ls"}
		if list.filter != "" {
			args = append(args, "--filter", "name="+string(Current)+list.filter)
		}
		args = append(args, "--format", "{{.Name}}\t"+ownerLabel+"\t{{.Label \"com.docker.compose.project\"}}\t"+instanceLabel)
		out, err := dk.Run(ctx, args...)
		if err != nil {
			return nil, fmt.Errorf("listing %ss: %w: %s", list.kind, err, strings.TrimSpace(string(out)))
		}
		for _, line := range strings.Split(strings.TrimRight(string(out), "\r\n"), "\n") {
			f := strings.Split(line, "\t")
			name := strings.TrimSpace(f[0])
			if name == "" {
				continue
			}
			r := deploy.Resource{Kind: list.kind, Name: name, Status: "present"}
			if len(f) >= 3 {
				r.Owner, r.Stack = strings.TrimSpace(f[1]), strings.TrimSpace(f[2])
			}
			if (list.kind == "volume" && !isNamedVolume(name)) || len(f) < 4 || !dk.installs(strings.TrimSpace(f[3])) {
				r.Tags = []string{deploy.TagConfirmsOnly}
			}
			res = append(res, r)
		}
	}
	// Only recorded image references are exposed from the host listing.
	if i.Images != nil {
		recorded, err := i.Images(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading the recorded images: %w", err)
		}
		present, err := recordedImages(ctx, dk, recorded)
		if err != nil {
			return nil, err
		}
		for _, ref := range present {
			res = append(res, deploy.Resource{Kind: "image", Name: ref, ID: ref, Status: "present"})
		}
	}
	for n := range res {
		res[n].ObservedDaemonID = daemonID
	}
	return res, nil
}

// ownerLabel is a `docker ps` or `ls` format reading who made a thing.
var ownerLabel = `{{.Label "` + dockerlabel.Owner + `"}}`

// instanceLabel is a `docker ps` or `ls` format reading which installation
// made a thing. Only what this installation made can be not accounted for:
// anything else (another AppTown's, or made before the label) is reported
// only to confirm a record (deploy.TagConfirmsOnly).
var instanceLabel = `{{.Label "` + dockerlabel.Instance + `"}}`

// recordedImages matches recorded names against one complete daemon listing.
// An unread listing cannot establish absence.
func recordedImages(ctx context.Context, dk Scope, refs []string) ([]string, error) {
	out, err := dk.Run(ctx, "image", "ls", "--all", "--digests", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		return nil, fmt.Errorf("listing Docker images: %w", err)
	}
	names := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var row struct{ Repository, Tag, Digest, ID string }
		if json.Unmarshal([]byte(line), &row) != nil || row.ID == "" || row.Repository == "" || row.Tag == "" || row.Digest == "" {
			return nil, fmt.Errorf("reading Docker's image listing: %q", strs.Truncate(line, 200))
		}
		names[familiar(row.ID)] = true
		if row.Repository != "<none>" {
			if row.Tag != "<none>" {
				names[familiar(row.Repository+":"+row.Tag)] = true
			}
			if row.Digest != "<none>" {
				names[familiar(row.Repository+"@"+row.Digest)] = true
			}
		}
	}
	var present []string
	for _, ref := range slices.Compact(slices.Sorted(slices.Values(refs))) {
		if names[familiar(ref)] {
			present = append(present, ref)
		}
	}
	return present, nil
}

// familiar is ref as Docker spells it in its answers: Docker Hub's host and
// its library/ namespace left out, and a name with neither tag nor digest
// given Docker's implicit latest (a local build is recorded bare,
// BuildTag). An image ID (sha256:…) is left as it is.
func familiar(ref string) string {
	for _, hub := range []string{"docker.io/", "index.docker.io/"} {
		if rest, ok := strings.CutPrefix(ref, hub); ok {
			ref = rest
			break
		}
	}
	if rest, ok := strings.CutPrefix(ref, "library/"); ok && !strings.Contains(strings.SplitN(rest, "@", 2)[0], "/") {
		ref = rest
	}
	name := strings.SplitN(ref, "@", 2)[0]
	last := name[strings.LastIndex(name, "/")+1:]
	if !strings.Contains(ref, "@") && !strings.Contains(last, ":") {
		ref += ":latest"
	}
	return ref
}
