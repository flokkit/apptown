package docker

import (
	"context"
	"flokkit.io/forge/internal/dockerlabel"
	"fmt"
	"strings"

	"flokkit.io/forge/internal/deploy"
)

// Inventory answers what a Docker host holds: the standing containers
// named by Forge, its volumes and networks, and image references, as the daemon reports
// them, over the host's scope so an attached host answers for itself. It
// is built for a target, not a deployment: it can look, never boot.
type Inventory struct {
	Scope func(context.Context) (Scope, error)
	Also  []string // containers the host also reports by name (the caller's own database on its built-in host)
}

var _ deploy.Inventorier = Inventory{}

func (i Inventory) Inventory(ctx context.Context) ([]deploy.Resource, error) {
	dk, err := i.Scope(ctx)
	if err != nil {
		return nil, err
	}
	// The forge's containers are known by their names (names.go), which
	// predate the labels: a service made before labelling would read as
	// missing under a label filter. The forge's own database is asked for
	// by its name too.
	filters := [][]string{{"--filter", "name=^forge-env-"}, {"--filter", "name=^forge-svc-"}, {"--filter", "name=^forge-preview-"}, {"--filter", "name=^forge-init-"}}
	for _, name := range i.Also {
		filters = append(filters, []string{"--filter", "name=^" + name + "$"})
	}
	var res []deploy.Resource
	seen := map[string]bool{}
	for _, filter := range filters {
		args := append([]string{"ps", "-a"}, filter...)
		// The labels say who made a thing (forge.owner) and which compose
		// stack it belongs to; the page attributes leftovers by them.
		args = append(args, "--format", "{{.Names}}\t{{.Status}}\t{{.Image}}\t{{.Label \""+dockerlabel.Owner+"\"}}\t{{.Label \"com.docker.compose.project\"}}")
		out, err := dk.Run(ctx, args...)
		if err != nil {
			// The error itself too: an ssh that failed before docker said
			// anything left only "docker ps: " to go on.
			return nil, fmt.Errorf("listing containers: %w: %s", err, strings.TrimSpace(string(out)))
		}
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
			res = append(res, r)
		}
	}
	// Volumes are listed whole: the forge names data folders and need
	// volumes, but an approved older service keeps Docker's anonymous name
	// for its data (ownership_inspection.go), and a listing filtered by the
	// forge's prefixes would call that present volume missing. A volume the
	// forge did not name is reported only to confirm a record
	// (deploy.TagConfirmsOnly), never offered as an orphan. A failed kind is
	// not an empty kind: the cache retains the last complete answer with the
	// error instead of calling present resources missing.
	for _, list := range []struct{ kind, filter string }{{"volume", ""}, {"network", netPrefix}} {
		args := []string{list.kind, "ls"}
		if list.filter != "" {
			args = append(args, "--filter", "name="+list.filter)
		}
		args = append(args, "--format", "{{.Name}}\t{{.Label \""+dockerlabel.Owner+"\"}}\t{{.Label \"com.docker.compose.project\"}}")
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
			if list.kind == "volume" && !strings.HasPrefix(name, dataVolumePrefix) && !strings.HasPrefix(name, needVolumePrefix) {
				r.Tags = []string{deploy.TagConfirmsOnly}
			}
			res = append(res, r)
		}
	}
	// --digests returns repository/digest aliases, including several for one
	// image. Tags cannot confirm an immutable digest. Local build ownership
	// also records tags, so report those separately. Reconciliation exposes
	// only recorded images, not unrelated host images as cleanup candidates.
	out, err := dk.Run(ctx, "image", "ls", "--digests", "--no-trunc", "--format", "{{.Repository}}\t{{.Tag}}\t{{.Digest}}")
	if err != nil {
		return nil, fmt.Errorf("listing images: %w: %s", err, strings.TrimSpace(string(out)))
	}
	refs := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 || f[0] == "" || f[0] == "<none>" {
			continue
		}
		var aliases []string
		if strings.HasPrefix(f[2], "sha256:") {
			aliases = append(aliases, f[0]+"@"+f[2])
		}
		if f[1] != "" && f[1] != "<none>" {
			aliases = append(aliases, f[0]+":"+f[1])
		}
		// A bare name is Docker's implicit latest: a local build is tagged
		// and recorded bare (BuildTag), and the daemon lists it as
		// name:latest. Both spellings name the one image.
		if f[1] == "latest" {
			aliases = append(aliases, f[0])
		}
		for _, ref := range aliases {
			if refs[ref] {
				continue
			}
			refs[ref] = true
			res = append(res, deploy.Resource{Kind: "image", Name: ref, ID: ref, Status: "present"})
		}
	}
	return res, nil
}
