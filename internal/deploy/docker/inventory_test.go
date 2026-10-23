package docker

import (
	"context"
	"errors"
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
		case strings.HasPrefix(line, "ps -a"):
			if strings.Contains(line, "name=^apptown-dep-") {
				return []byte("apptown-dep-k1\tUp 1 hour\tapp\tdep-k1\t\n"), nil
			}
		case strings.HasPrefix(line, "volume ls"):
			if strings.Contains(line, "--filter") {
				t.Errorf("volumes are listed whole: an approved older volume has no AppTown prefix: %s", line)
			}
			return []byte("apptown-data-shop\tdep-k1\t\napptown-need-shop\tdep-k1\t\n0a1b2c\t\t\n"), nil
		case strings.HasPrefix(line, "network ls"):
			if !strings.Contains(line, "name=apptown-net-") {
				t.Errorf("network listing must be scoped: %s", line)
			}
			return []byte("apptown-net-dep-k1\tdep-k1\t\n"), nil
		case strings.HasPrefix(line, "image ls"):
			return []byte("reg/shop\tlatest\tsha256:aaa\nreg/shop\told\tsha256:bbb\nreg/shop\talias\tsha256:aaa\napptown-preview-build-shop\tlatest\t<none>\n<none>\t<none>\t<none>\n"), nil
		}
		return nil, nil
	}}
	out, err := (Inventory{Scope: func(context.Context) (Scope, error) { return Scope{Client: client, Host: "ssh://target"}, nil }}).Inventory(t.Context())
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
		got = append(got, r.Kind+":"+r.Name)
	}
	for _, want := range []string{"container:apptown-dep-k1", "volume:apptown-data-shop", "volume:apptown-need-shop", "volume:0a1b2c", "network:apptown-net-dep-k1", "image:reg/shop@sha256:aaa", "image:reg/shop@sha256:bbb", "image:apptown-preview-build-shop:latest",
		// The local build as it is recorded: bare, Docker's implicit latest.
		"image:" + Current.BuildTag("shop")} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	for _, r := range out {
		// A volume AppTown did not name only confirms a record.
		if confirms := slices.Contains(r.Tags, deploy.TagConfirmsOnly); confirms != (r.Name == "0a1b2c") {
			t.Errorf("confirm-only marking wrong: %+v", r)
		}
		if (r.Kind == "volume" || r.Kind == "network") && r.Name != "0a1b2c" {
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
	for _, command := range []string{"ps -a", "volume ls", "network ls", "image ls"} {
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
	out, err := (Inventory{Scope: func(context.Context) (Scope, error) { return Scope{Client: &fakeClient{}}, nil }}).Inventory(t.Context())
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
		Scope: func(context.Context) (Scope, error) { return Scope{Client: client, Host: "ssh://target"}, nil },
		Also:  []string{"apptown-postgres"},
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
