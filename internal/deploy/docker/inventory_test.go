package docker

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy"
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
			if strings.Contains(line, "name=^apptown-env-") {
				return []byte("apptown-env-shop-main\tUp 1 hour\tapp\tenv-shop-main\t\n"), nil
			}
		case strings.HasPrefix(line, "volume ls"):
			if strings.Contains(line, "--filter") {
				t.Errorf("volumes are listed whole: an approved older volume has no AppTown prefix: %s", line)
			}
			return []byte("apptown-data-shop\tenv-shop-main\t\napptown-need-shop\tenv-shop-main\t\n0a1b2c\t\t\n"), nil
		case strings.HasPrefix(line, "network ls"):
			if !strings.Contains(line, "name=apptown-net-") {
				t.Errorf("network listing must be scoped: %s", line)
			}
			return []byte("apptown-net-env-shop-main\tenv-shop-main\t\n"), nil
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
	for _, want := range []string{"container:apptown-env-shop-main", "volume:apptown-data-shop", "volume:apptown-need-shop", "volume:0a1b2c", "network:apptown-net-env-shop-main", "image:reg/shop@sha256:aaa", "image:reg/shop@sha256:bbb", "image:apptown-preview-build-shop:latest",
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
			if r.Owner != "env-shop-main" {
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
