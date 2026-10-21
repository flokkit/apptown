package docker

import (
	"context"
	"strings"
	"testing"

	"flokkit.io/forge/internal/check"
)

func TestStaleEndpointIsRecognised(t *testing.T) {
	t.Parallel()
	if !StaleEndpoint("docker: Error response from daemon: failed to set up container networking: endpoint with name apptown-env-hello-rust-main already exists in network bridge") {
		t.Fatal("the daemon's stale-endpoint refusal must be recognised")
	}
	if StaleEndpoint("docker: Error response from daemon: No such image") {
		t.Fatal("other failures are not a stale endpoint")
	}
}

// A data volume is handed to the image's declared user; root or no USER
// leaves it alone. The parse is the judgment, the chown is Docker's.
func TestParseImageUser(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, uid, gid string
		ok           bool
	}{
		{"", "", "", false}, {"root", "", "", false}, {"0", "", "", false}, {"0:0", "", "", false},
		{"10001", "10001", "", true}, {"10001:10001", "10001", "10001", true},
		{"app", "app", "", true}, {"app:app", "app", "app", true}, {"node:root", "node", "0", true},
		{"Not A User", "", "", false}, {"app;rm", "", "", false},
	}
	for _, c := range cases {
		uid, gid, ok := ParseImageUser(c.in)
		if uid != c.uid || gid != c.gid || ok != c.ok {
			t.Fatalf("%q: got %q %q %v, want %q %q %v", c.in, uid, gid, ok, c.uid, c.gid, c.ok)
		}
	}
}

func TestHandVolumeToImageUserRunsChownForNumericUser(t *testing.T) {
	t.Parallel()
	first := &fakeClient{out: "10001\n"}
	HandVolumeToImageUser(context.Background(), Scope{Client: first}, "apptown-data-x", "app:1", check.ShImage)
	if len(first.args) != 2 || first.args[1][0] != "run" || !strings.Contains(strings.Join(first.args[1], " "), "chown -R 10001:10001 /mnt") {
		t.Fatalf("expected an inspect then a chown, got %v", first.args)
	}
	second := &fakeClient{out: "\n"}
	HandVolumeToImageUser(context.Background(), Scope{Client: second}, "apptown-data-x", "app:1", check.ShImage)
	if len(second.args) != 1 {
		t.Fatalf("no USER: inspect only, got %v", second.args)
	}
}
