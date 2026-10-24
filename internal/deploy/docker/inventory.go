package docker

import (
	"bytes"
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
// is built for a target, not a deployment: it can look, never boot.
type Inventory struct {
	Scope func(context.Context) (Scope, error)
	Also  []string // containers the host also reports by name (the caller's own database on its built-in host)
	// Images is the image references the deployments on the target
	// recorded (deploy.Owned of kind image), each asked for exactly.
	Images func(context.Context) ([]string, error)
}

var _ deploy.Inventorier = Inventory{}

func (i Inventory) Inventory(ctx context.Context) ([]deploy.Resource, error) {
	dk, err := i.Scope(ctx)
	if err != nil {
		return nil, err
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
	args = append(args, "--format", "{{.Names}}\t{{.Status}}\t{{.Image}}\t"+ownerLabel+"\t{{.Label \"com.docker.compose.project\"}}")
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
		args = append(args, "--format", "{{.Name}}\t"+ownerLabel+"\t{{.Label \"com.docker.compose.project\"}}")
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
			if list.kind == "volume" && !IsNamedVolume(name) {
				r.Tags = []string{deploy.TagConfirmsOnly}
			}
			res = append(res, r)
		}
	}
	// Images are asked for by the references the deployments recorded,
	// never listed: a listing leaves out an image with no tag (Docker's
	// containerd store, and the classic one for an image pulled by digest
	// alone), and reconciliation shows only recorded images anyway.
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
	return res, nil
}

// ownerLabel is a `docker ps` or `ls` format reading who made a thing.
var ownerLabel = `{{.Label "` + dockerlabel.Owner + `"}}`

// recordedImages answers which of refs are on dk, with one `docker image
// inspect` for all of them (an attached host is an ssh round trip a call).
// A ref is present when it is one of an answered image's own names: a
// digest reference among its RepoDigests (only there: a tag cannot confirm
// a digest), a tag among its RepoTags (a bare name is Docker's implicit latest, as a
// local build is recorded), or its ID. It is absent when Docker says it has
// no such image. Anything else is unknown, and an error: a failure that is
// not "no such image", an answer that cannot be read, or a ref Docker
// neither answered by that name nor said it lacks.
func recordedImages(ctx context.Context, dk Scope, refs []string) ([]string, error) {
	refs = slices.Compact(slices.Sorted(slices.Values(refs)))
	if len(refs) == 0 {
		return nil, nil
	}
	var stdout, stderr bytes.Buffer
	args := append([]string{"image", "inspect", "--format", "{{json .RepoTags}}\t{{json .RepoDigests}}\t{{json .Id}}"}, refs...)
	_, runErr := dk.Command(ctx, Command{Args: args, Stdout: &stdout, Stderr: &stderr})
	names := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		var tags, digests []string
		var id string
		if len(f) != 3 || json.Unmarshal([]byte(f[0]), &tags) != nil || json.Unmarshal([]byte(f[1]), &digests) != nil || json.Unmarshal([]byte(f[2]), &id) != nil {
			return nil, fmt.Errorf("reading Docker's answer about the recorded images: %q", strs.Truncate(line, 200))
		}
		for _, n := range append(append(tags, digests...), id) {
			names[familiar(n)] = true
		}
	}
	missing := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		_, ref, ok := strings.Cut(line, "No such image: ")
		if !ok {
			return nil, fmt.Errorf("asking Docker for the recorded images: %s", strs.Truncate(line, 200))
		}
		missing[familiar(strings.TrimSpace(ref))] = true
	}
	if runErr != nil && len(missing) == 0 {
		return nil, fmt.Errorf("asking Docker for the recorded images: %w", runErr)
	}
	var present []string
	for _, ref := range refs {
		switch name := familiar(ref); {
		case missing[name]:
		case names[name]:
			present = append(present, ref)
		default:
			return nil, fmt.Errorf("docker did not say whether it has the recorded image %s", ref)
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
