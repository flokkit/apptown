package docker

import (
	"context"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
)

func inventoryFixture(t *testing.T, fail string) ([]deploy.Resource, error) {
	t.Helper()
	client := &fakeClient{reply: func(req Command) ([]byte, error) {
		line := strings.Join(req.Args, " ")
		if fail != "" && strings.HasPrefix(line, fail) {
			return nil, errors.New("listing unavailable")
		}
		switch {
		case strings.HasPrefix(line, "info "):
			return []byte("daemon-a\n"), nil
		case strings.HasPrefix(line, "ps -a"):
			if strings.Contains(line, "name=^apptown-dep-") {
				return []byte("apptown-dep-k1\tUp 1 hour\tapp\tdep-k1\t\tinst-a\napptown-dep-k2\tUp 1 hour\tapp\tdep-k2\t\tinst-b\napptown-dep-k3\tUp 1 hour\tapp\tdep-k3\t\t\n"), nil
			}
		case strings.HasPrefix(line, "volume ls"):
			if strings.Contains(line, "--filter") {
				t.Errorf("volumes are listed whole: an approved older volume has no AppTown prefix: %s", line)
			}
			return []byte("apptown-data-shop\tdep-k1\t\tinst-a\napptown-need-shop\tdep-k1\t\tinst-a\n0a1b2c\t\t\tinst-a\napptown-data-old\tdep-k3\t\t\n"), nil
		case strings.HasPrefix(line, "network ls"):
			if !strings.Contains(line, "name=apptown-net-") {
				t.Errorf("network listing must be scoped: %s", line)
			}
			return []byte("apptown-net-dep-k1\tdep-k1\t\tinst-a\napptown-net-dep-k2\tdep-k2\t\tinst-b\n"), nil
		case strings.HasPrefix(line, "image inspect"):
			return inspectAnswer(req,
				[]string{`["reg/shop:latest","reg/shop:alias"]` + "\t" + `["reg/shop@sha256:aaa"]` + "\t" + `"sha256:1"`,
					`["reg/shop:old"]` + "\t" + `["reg/shop@sha256:bbb"]` + "\t" + `"sha256:2"`,
					`["` + Current.BuildTag("shop") + `:latest"]` + "\t[]\t" + `"sha256:3"`},
				"Error response from daemon: No such image: reg/gone@sha256:ccc")
		}
		return nil, nil
	}}
	images := func(context.Context) ([]string, error) {
		return []string{"reg/shop@sha256:aaa", "reg/shop@sha256:bbb", Current.BuildTag("shop"), "reg/gone@sha256:ccc", "reg/shop@sha256:aaa"}, nil
	}
	out, err := (Inventory{Scope: func(context.Context) (Scope, error) {
		return Scope{Client: client, Host: "ssh://target", Instance: "inst-a"}, nil
	}, Images: images}).Inventory(t.Context())
	for _, host := range client.hosts {
		if host != "ssh://target" {
			t.Fatalf("inventory escaped its target: %q", host)
		}
	}
	return out, err
}

func TestInventoryConfirmsNeedVolumesNetworksAndExactImageReferences(t *testing.T) {
	t.Parallel()
	out, err := inventoryFixture(t, "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range out {
		if r.ObservedDaemonID != "daemon-a" {
			t.Fatalf("inventory lost its daemon: %+v", r)
		}
		got = append(got, r.Kind+":"+r.Name)
	}
	for _, want := range []string{"container:apptown-dep-k1", "volume:apptown-data-shop", "volume:apptown-need-shop", "volume:0a1b2c", "network:apptown-net-dep-k1", "image:reg/shop@sha256:aaa", "image:reg/shop@sha256:bbb",
		// The local build as it is recorded: bare, Docker's implicit latest.
		"image:" + Current.BuildTag("shop")} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	// A recorded image Docker says it lacks is not reported (the merge reads
	// it missing), and an image nobody recorded is never asked about.
	for _, r := range out {
		if r.Kind == "image" && (r.Name == "reg/gone@sha256:ccc" || strings.Contains(r.Name, ":alias")) {
			t.Errorf("reported %+v", r)
		}
	}
	for _, r := range out {
		// A volume AppTown did not name, and anything another installation
		// made or made before the installation label, only confirms a record.
		notOurs := map[string]bool{"0a1b2c": true, "apptown-dep-k2": true, "apptown-dep-k3": true, "apptown-data-old": true, "apptown-net-dep-k2": true}
		if confirms := slices.Contains(r.Tags, deploy.TagConfirmsOnly); r.Kind != "image" && confirms != notOurs[r.Name] {
			t.Errorf("confirm-only marking wrong: %+v", r)
		}
		if (r.Kind == "volume" || r.Kind == "network") && r.Name != "0a1b2c" && !notOurs[r.Name] {
			if r.Owner != "dep-k1" {
				t.Errorf("lost attribution: %+v", r)
			}
		}
	}
	seen := map[string]bool{}
	for _, r := range out {
		key := r.Kind + ":" + r.Name
		if seen[key] {
			t.Errorf("duplicate %s", key)
		}
		seen[key] = true
	}
}

func TestInventoryNeverPublishesAPartialSuccessfulListing(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"info", "ps -a", "volume ls", "network ls", "image inspect"} {
		t.Run(command, func(t *testing.T) {
			out, err := inventoryFixture(t, command)
			if err == nil || out != nil {
				t.Fatalf("%s failure published %+v, %v", command, out, err)
			}
		})
	}
}

func TestInventoryEmptyHostIsAnAnswer(t *testing.T) {
	t.Parallel()
	out, err := (Inventory{Scope: func(context.Context) (Scope, error) {
		return Scope{Client: &fakeClient{reply: func(req Command) ([]byte, error) {
			if req.Args[0] == "info" {
				return []byte("empty-host"), nil
			}
			return nil, nil
		}}}, nil
	}}).Inventory(t.Context())
	if err != nil || len(out) != 0 {
		t.Fatalf("%+v %v", out, err)
	}
}

// Each container listing is an ssh round trip to an attached host; one per
// name ran past the read's deadline on a small, busy host. The fake answers
// as Docker does: every name filter is a regular expression, and repeated
// name filters are ORed.
func TestInventoryListsContainersOnceWithEveryName(t *testing.T) {
	t.Parallel()
	host := []string{
		"apptown-dep-k1-app", "apptown-svc-shop-db", "apptown-preview-shop-7", "apptown-init-shop",
		"apptown-postgres",
		// Not AppTown's: a prefix mid-name, a longer name than the one asked for, another kind,
		// and the names things had before 1.0.
		"my-apptown-dep-k1", "apptown-postgres-2", "apptown-other",
		"apptown-env-shop-main", "forge-dep-k2-app", "forge-env-old-main", "forge-svc-old-db", "forge-preview-old-3", "forge-init-old",
	}
	psCalls := 0
	client := &fakeClient{reply: func(req Command) ([]byte, error) {
		if len(req.Args) > 0 && req.Args[0] == "info" {
			return []byte("daemon-a"), nil
		}
		if len(req.Args) < 2 || req.Args[0] != "ps" {
			return nil, nil
		}
		psCalls++
		var names []*regexp.Regexp
		for i, a := range req.Args {
			if a == "--filter" && i+1 < len(req.Args) && strings.HasPrefix(req.Args[i+1], "name=") {
				names = append(names, regexp.MustCompile(strings.TrimPrefix(req.Args[i+1], "name=")))
			}
		}
		var out strings.Builder
		for _, name := range host {
			if slices.ContainsFunc(names, func(re *regexp.Regexp) bool { return re.MatchString(name) }) {
				out.WriteString(name + "\tUp 1 hour\timg\towner\tstack\n")
			}
		}
		return []byte(out.String()), nil
	}}
	inv := Inventory{
		Scope: func(context.Context) (Scope, error) {
			return Scope{Client: client, Host: "ssh://target", Instance: "inst-a"}, nil
		},
		Also: []string{"apptown-postgres"},
	}
	out, err := inv.Inventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if psCalls != 1 {
		t.Errorf("listed containers %d times, want once", psCalls)
	}
	var got []string
	for _, r := range out {
		if r.Kind == "container" {
			got = append(got, r.Name)
		}
	}
	slices.Sort(got)
	want := slices.Clone(host[:5])
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("containers %v, want %v", got, want)
	}
}

// inspectAnswer answers a `docker image inspect` as Docker does: a line on
// stdout for each image it has, an error line on stderr for each it lacks,
// and a failed exit when there was any.
func inspectAnswer(req Command, found []string, errs ...string) ([]byte, error) {
	if req.Stdout == nil || req.Stderr == nil {
		return nil, errors.New("inspect read without separate streams")
	}
	for _, l := range found {
		_, _ = io.WriteString(req.Stdout, l+"\n")
	}
	for _, l := range errs {
		_, _ = io.WriteString(req.Stderr, l+"\n")
	}
	if len(errs) > 0 {
		return nil, errors.New("exit status 1")
	}
	return nil, nil
}

func inspectInventory(t *testing.T, recorded []string, found []string, errs ...string) ([]deploy.Resource, error) {
	t.Helper()
	client := &fakeClient{reply: func(req Command) ([]byte, error) {
		switch line := strings.Join(req.Args, " "); {
		case strings.HasPrefix(line, "info "):
			return []byte("daemon-a"), nil
		case strings.HasPrefix(line, "image inspect"):
			return inspectAnswer(req, found, errs...)
		case strings.HasPrefix(line, "image ls"):
			// Docker's containerd store lists no image without a tag.
			return []byte("busybox\tlatest\tsha256:other\n"), nil
		}
		return nil, nil
	}}
	return Inventory{
		Scope:  func(context.Context) (Scope, error) { return Scope{Client: client}, nil },
		Images: func(context.Context) ([]string, error) { return recorded, nil },
	}.Inventory(t.Context())
}

// A recorded image pulled by digest alone is there though no listing shows
// it: the inventory asks Docker for the recorded reference.
//
// Kills: reading images from `docker image ls`.
func TestInventoryConfirmsARecordedDigestOnlyImage(t *testing.T) {
	t.Parallel()
	ref := "busybox@sha256:bdf5"
	out, err := inspectInventory(t, []string{ref}, []string{`[]` + "\t" + `["` + ref + `"]` + "\t" + `"sha256:ad4f"`})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Kind != "image" || out[0].ID != ref || out[0].Status != "present" {
		t.Fatalf("the digest-only image read as %+v", out)
	}
}

// Docker answers in its familiar spelling (no docker.io/library/, a bare
// name as :latest), present or missing; a recorded ref is compared in it.
func TestInventoryReadsDockersFamiliarSpelling(t *testing.T) {
	t.Parallel()
	out, err := inspectInventory(t, []string{"docker.io/library/caddy:2-alpine", "apptown-build-gone"},
		[]string{`["caddy:2-alpine"]` + "\t" + `["caddy@sha256:24ec"]` + "\t" + `"sha256:c7d6"`},
		"Error response from daemon: No such image: apptown-build-gone:latest")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != "docker.io/library/caddy:2-alpine" {
		t.Fatalf("got %+v", out)
	}
}

// Unknown is never missing: what Docker did not plainly answer fails the
// read, and the cache keeps the last good list under the error.
func TestInventoryNeverReadsAnUnansweredImageAsMissing(t *testing.T) {
	t.Parallel()
	ref := "reg/app@sha256:aaa"
	for name, c := range map[string]struct {
		found []string
		errs  []string
	}{
		"a failure that is not no such image": {errs: []string{"permission denied while trying to connect to the Docker daemon socket"}},
		"an answer that cannot be read":       {found: []string{"not json"}},
		"a failed exit with nothing said":     {errs: []string{""}},
		// A tag never confirms a digest: the image answered only by its tag.
		"neither answered by its name nor said missing": {found: []string{`["reg/app:latest"]` + "\t" + `["reg/app@sha256:bbb"]` + "\t" + `"sha256:1"`}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out, err := inspectInventory(t, []string{ref}, c.found, c.errs...)
			if err == nil || out != nil {
				t.Fatalf("read as %+v, %v", out, err)
			}
		})
	}
}

func TestInventoryRefusesAnUnidentifiedDaemon(t *testing.T) {
	t.Parallel()
	client := &fakeClient{reply: func(req Command) ([]byte, error) {
		if req.Args[0] != "info" {
			t.Errorf("listed resources before daemon identity: %v", req.Args)
		}
		return []byte(" \n"), nil
	}}
	resources, err := (Inventory{Scope: func(context.Context) (Scope, error) { return Scope{Client: client}, nil }}).Inventory(t.Context())
	if err == nil || resources != nil {
		t.Fatalf("blank identity published inventory: %+v %v", resources, err)
	}
}

// What a deployment makes on a host carries the installation label, so the
// inventory can tell this installation's things from anyone else's.
func TestDeploymentThingsCarryTheInstallationLabel(t *testing.T) {
	t.Parallel()
	client := &fakeClient{reply: func(req Command) ([]byte, error) {
		if req.Args[0] == "network" && req.Args[1] == "inspect" {
			return nil, errors.New("No such network")
		}
		return nil, nil
	}}
	dk := Scope{Client: client, Instance: "inst-a"}
	own := Ownership{Record: func(deploy.Owned) error { return nil }}
	if err := createVolume(t.Context(), dk, "apptown-data-shop", "dep-k1", "", own); err != nil {
		t.Fatal(err)
	}
	if err := ensureNetwork(t.Context(), dk, "apptown-net-dep-k1", own); err != nil {
		t.Fatal(err)
	}
	labelled := map[string]bool{}
	for _, args := range client.args {
		if len(args) > 1 && args[1] == "create" && slices.Contains(args, "apptown.instance=inst-a") {
			labelled[args[0]] = true
		}
	}
	if !labelled["volume"] || !labelled["network"] {
		t.Fatalf("created without the installation label: %v", client.args)
	}
	if !slices.Contains(dk.Labels("env", "dep-k1"), "apptown.instance=inst-a") {
		t.Fatal("container labels lack the installation")
	}
}

// An attached host's scope is derived with WithHost; it keeps the
// installation, so what is made there carries the mark and what carries it
// is this installation's.
func TestAnAttachedHostKeepsTheInstallationsMark(t *testing.T) {
	t.Parallel()
	client := &fakeClient{reply: func(req Command) ([]byte, error) {
		switch req.Args[0] {
		case "info":
			return []byte("attached-daemon"), nil
		case "ps":
			return []byte("apptown-dep-k1\tUp 1 hour\timage\tdep-k1\t\tinst-a\n"), nil
		}
		return nil, nil
	}}
	attached := (Scope{Client: client, Instance: "inst-a"}).WithHost("ssh://attached", "", "")
	if !slices.Contains(attached.Labels("env", "dep-k1"), "apptown.instance=inst-a") {
		t.Error("an attached host makes things without this installation's mark")
	}
	got, err := (Inventory{Scope: func(context.Context) (Scope, error) { return attached, nil }}).Inventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || slices.Contains(got[0].Tags, deploy.TagConfirmsOnly) {
		t.Fatalf("this installation's container on an attached host: %+v", got)
	}
}
