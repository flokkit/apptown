package docker

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestHoldsDigestDistinguishesMismatchFromAnUnavailableDaemon(t *testing.T) {
	t.Parallel()
	ref := "registry.test/team/web@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, out string
		err       error
		held      bool
	}{
		{name: "held under another repository spelling", out: `sha256:local ["other.test/web@sha256:` + strings.Repeat("a", 64) + `"]`, held: true},
		{name: "different digest", out: `sha256:local ["registry.test/team/web@sha256:` + strings.Repeat("b", 64) + `"]`},
		{name: "another image ID", out: `sha256:other ["` + ref + `"]`},
		{name: "unavailable daemon", err: errors.New("cannot connect")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeClient{out: tc.out, err: tc.err}
			held, err := HoldsDigest(t.Context(), Scope{Client: fake}, "sha256:local", ref)
			if held != tc.held || !errors.Is(err, tc.err) {
				t.Fatalf("got %v, %v; want %v, %v", held, err, tc.held, tc.err)
			}
			want := []string{"image", "inspect", "--format", "{{.Id}} {{json .RepoDigests}}", "sha256:local"}
			if len(fake.args) != 1 || !reflect.DeepEqual(fake.args[0], want) {
				t.Fatalf("commands: %v", fake.args)
			}
		})
	}
}
