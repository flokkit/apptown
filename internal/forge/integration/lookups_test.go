package integration

import (
	"context"
	"testing"
)

func TestLookups(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	b := New(db)
	for _, r := range [][3]string{{"syd", "cloud-account", "digitalocean"}, {"nyc", "cloud-account", "digitalocean"}, {"hub", "cloud-account", "other"}, {"gitlab", "registry", "gitlab"}} {
		if _, err := b.Save(context.Background(), r[0], r[1], r[2], `{"url":"https://x"}`, "tester"); err != nil {
			t.Fatal(err)
		}
	}
	if names, err := b.NamesByProvider(t.Context(), "cloud-account", "digitalocean"); err != nil || len(names) != 2 || names[0] != "nyc" {
		t.Fatalf("by provider: %v %v", err, names)
	}
	if names, _ := b.NamesByProvider(t.Context(), "cloud-account", "none"); len(names) != 0 {
		t.Fatal("no bindings from that provider")
	}
	if name, found, err := b.FirstName(t.Context(), "registry"); err != nil || !found || name != "gitlab" {
		t.Fatalf("first: %v %v %q", err, found, name)
	}
	if _, found, _ := b.FirstName(t.Context(), "nothing"); found {
		t.Fatal("none")
	}
	if snap, found, err := b.FirstNamed(t.Context(), "cloud-account", ""); err != nil || !found || snap.Name != "hub" {
		t.Fatalf("first named: %v %v %+v", err, found, snap)
	}
	if snap, found, _ := b.FirstNamed(t.Context(), "cloud-account", "syd"); !found || snap.Provider != "digitalocean" || snap.ConfigJSON == "" {
		t.Fatalf("named: %v %+v", found, snap)
	}
	if _, found, _ := b.FirstNamed(t.Context(), "cloud-account", "mars"); found {
		t.Fatal("unknown name")
	}
}
