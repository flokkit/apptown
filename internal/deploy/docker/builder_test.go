package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Every build names the selected daemon's own builder: "default" for a
// daemon chosen by --host, the context's own otherwise, and only when that
// builder is the daemon's (docker driver). A builder of another driver is
// refused before anything is sent; without buildx the daemon's legacy
// builder builds, which nothing can redirect.
func TestABuildNamesTheSelectedDaemonsOwnBuilder(t *testing.T) {
	t.Parallel()
	answer := func(driver string, buildx bool) func(Command) ([]byte, error) {
		return func(req Command) ([]byte, error) {
			switch strings.Join(req.Args, " ") {
			case "buildx version":
				if !buildx {
					return []byte("docker: 'buildx' is not a docker command."), errors.New("exit status 1")
				}
				return []byte("github.com/docker/buildx v0.36.1"), nil
			case "context show":
				return []byte("desktop-linux\n"), nil
			}
			if req.Args[0] == "buildx" && req.Args[1] == "inspect" {
				return []byte("Name:          " + req.Args[2] + "\nDriver:        " + driver + "\n"), nil
			}
			return nil, nil
		}
	}
	builds := func(f *fakeClient) []string {
		var out []string
		for _, a := range f.args {
			if a[0] == "build" {
				out = append(out, strings.Join(a, " "))
			}
		}
		return out
	}
	attached := &fakeClient{reply: answer("docker", true)}
	if _, err := (Scope{Client: attached}).WithHost("ssh://lab", "lab", "").Build(context.Background(), "-t", "x", "ctx"); err != nil || strings.Join(builds(attached), "|") != "build --label apptown.built=1 --builder default -t x ctx" {
		t.Fatalf("a daemon chosen by --host: %q %v", builds(attached), err)
	}
	own := &fakeClient{reply: answer("docker", true)}
	if _, err := (Scope{Client: own}).Build(context.Background(), "-t", "x", "ctx"); err != nil || strings.Join(builds(own), "|") != "build --label apptown.built=1 --builder desktop-linux -t x ctx" {
		t.Fatalf("AppTown's own daemon: %q %v", builds(own), err)
	}
	remote := &fakeClient{reply: answer("remote", true)}
	if _, err := (Scope{Client: remote}).Build(context.Background(), "-t", "x", "ctx"); err == nil || !strings.Contains(err.Error(), `its driver is "remote"`) || len(builds(remote)) != 0 {
		t.Fatalf("a remote builder: %q %v", builds(remote), err)
	}
	legacy := &fakeClient{reply: answer("", false)}
	if _, err := (Scope{Client: legacy}).Build(context.Background(), "-t", "x", "ctx"); err != nil || strings.Join(builds(legacy), "|") != "build --label apptown.built=1 -t x ctx" {
		t.Fatalf("no buildx: %q %v", builds(legacy), err)
	}
}

// A preview's build carries its cache in its image and builds from the
// image its tag names, when the daemon has one (never asking a registry for
// a repository of that name otherwise). The image the tag named before goes,
// by ID, once the tag names another and nothing else names it: one another
// tag or a digest still names is another owner's (two parts sharing an
// image), and one a container uses stays.
func TestAPreviewBuildCachesFromItsKeptImageAndRemovesTheOneItReplaced(t *testing.T) {
	t.Parallel()
	const tag, other = "apptown-preview-build-apptown-preview-change-7", "apptown-preview-build-apptown-preview-change-7-worker"
	// images: tag → ID, as the daemon names them; digests: IDs with a digest.
	run := func(images map[string]string, digests map[string]bool, after string, used bool) ([]string, map[string]string) {
		t.Helper()
		f := &fakeClient{reply: func(req Command) ([]byte, error) {
			a := req.Args
			switch {
			case strings.Join(a, " ") == "buildx version":
				return []byte("docker: 'buildx' is not a docker command."), errors.New("exit status 1")
			case a[0] == "image" && a[1] == "inspect" && a[3] == "{{.Id}}":
				if id, ok := images[a[4]]; ok {
					return []byte(id + "\n"), nil
				}
				return []byte("Error: No such image: " + a[4]), errors.New("exit status 1")
			case a[0] == "image" && a[1] == "inspect":
				tags := 0
				for _, id := range images {
					if id == a[4] {
						tags++
					}
				}
				d := 0
				if digests[a[4]] {
					d = 1
				}
				return []byte(fmt.Sprintf("%d %d\n", tags, d)), nil
			case a[0] == "build":
				images[tag] = after
			case a[0] == "rmi" && used:
				return []byte("Error response from daemon: conflict: unable to delete " + a[1] + " (cannot be forced) - image is being used by running container 0123"), errors.New("exit status 1")
			case a[0] == "rmi":
				for t, id := range images {
					if id == a[1] {
						delete(images, t)
					}
				}
			}
			return nil, nil
		}}
		if _, err := buildPreviewImage(context.Background(), Scope{Client: f}, tag, "/ctx"); err != nil {
			t.Fatal(err)
		}
		var calls []string
		for _, a := range f.args {
			if a[0] == "build" || a[0] == "rmi" {
				calls = append(calls, strings.Join(a, " "))
			}
		}
		return calls, images
	}
	if got, _ := run(map[string]string{}, nil, "sha256:new", false); strings.Join(got, "\n") != "build --label apptown.built=1 -t "+tag+" --build-arg BUILDKIT_INLINE_CACHE=1 /ctx" {
		t.Fatalf("a first build: %v", got)
	}
	want := "build --label apptown.built=1 -t " + tag + " --build-arg BUILDKIT_INLINE_CACHE=1 --cache-from " + tag + " /ctx"
	if got, _ := run(map[string]string{tag: "sha256:old"}, nil, "sha256:new", false); strings.Join(got, "\n") != want+"\nrmi sha256:old" {
		t.Fatalf("a rebuild to a new image: %v", got)
	}
	if got, _ := run(map[string]string{tag: "sha256:old"}, nil, "sha256:old", false); strings.Join(got, "\n") != want {
		t.Fatalf("a rebuild to the same image removed something: %v", got)
	}
	if got, _ := run(map[string]string{tag: "sha256:old"}, nil, "sha256:new", true); strings.Join(got, "\n") != want+"\nrmi sha256:old" {
		t.Fatalf("a replaced image in use: %v", got)
	}
	// Another part's tag names the same image: it is not this build's.
	got, left := run(map[string]string{tag: "sha256:shared", other: "sha256:shared"}, nil, "sha256:new", false)
	if strings.Join(got, "\n") != want || left[other] != "sha256:shared" {
		t.Fatalf("an image another tag names: %v, left %v", got, left)
	}
	// A digest still names it (a release pulled by digest).
	if got, _ := run(map[string]string{tag: "sha256:old"}, map[string]bool{"sha256:old": true}, "sha256:new", false); strings.Join(got, "\n") != want {
		t.Fatalf("an image a digest names: %v", got)
	}
}
