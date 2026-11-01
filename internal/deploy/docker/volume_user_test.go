package docker

import (
	"context"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/stock"
)

func TestStaleEndpointIsRecognised(t *testing.T) {
	t.Parallel()
	if !staleEndpoint("docker: Error response from daemon: failed to set up container networking: endpoint with name apptown-env-hello-rust-main already exists in network bridge") {
		t.Fatal("the daemon's stale-endpoint refusal must be recognised")
	}
	if staleEndpoint("docker: Error response from daemon: No such image") {
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
		uid, gid, ok := parseImageUser(c.in)
		if uid != c.uid || gid != c.gid || ok != c.ok {
			t.Fatalf("%q: got %q %q %v, want %q %q %v", c.in, uid, gid, ok, c.uid, c.gid, c.ok)
		}
	}
}

func TestHandVolumeToImageUserRunsChownForNumericUser(t *testing.T) {
	t.Parallel()
	first := &fakeClient{out: "10001\n"}
	handVolumeToImageUser(context.Background(), Scope{Client: first}, "apptown-data-x", "app:1", stock.Images().Sh)
	if len(first.args) != 2 || first.args[1][0] != "run" || !strings.Contains(strings.Join(first.args[1], " "), "chown -R 10001:10001 /mnt") {
		t.Fatalf("expected an inspect then a chown, got %v", first.args)
	}
	second := &fakeClient{out: "\n"}
	handVolumeToImageUser(context.Background(), Scope{Client: second}, "apptown-data-x", "app:1", stock.Images().Sh)
	if len(second.args) != 1 {
		t.Fatalf("no USER: inspect only, got %v", second.args)
	}
}

// A named user's ids are the image's own answer, read from a file in its
// container, so a lost stdout is no answer: the volume is left as it is,
// never handed to a wrong or empty owner.
//
// Kills: ids read from what the container printed.
func TestANamedUsersIdsComeFromTheImagesAnswer(t *testing.T) {
	t.Parallel()
	for _, answer := range [][]byte{[]byte("1000\n1001\n"), nil} {
		var chowns []string
		results := &containerresulttest.Daemon{Answers: func(c containerresulttest.Command) (containerresulttest.Answer, bool) {
			return containerresulttest.Answer{Answer: answer}, c.Script == "id -u app && id -g app"
		}}
		client := clientFunc(func(c Command) ([]byte, error) {
			if out, err, ok := results.Run(c.Args); ok {
				return out, err
			}
			switch c.Args[0] {
			case "image":
				return []byte("app\n"), nil
			case "run":
				chowns = append(chowns, strings.Join(c.Args, " "))
			}
			return nil, nil
		})
		handVolumeToImageUser(context.Background(), Scope{Client: client}, "apptown-data-x", "app:1", stock.Images().Sh)
		switch {
		case answer != nil && (len(chowns) != 1 || !strings.Contains(chowns[0], "chown -R 1000:1001 /mnt")):
			t.Fatalf("the image's ids: chowns %v", chowns)
		case answer == nil && len(chowns) != 0:
			t.Fatalf("no answer came back, and the volume was handed over: %v", chowns)
		}
	}
}

type clientFunc func(Command) ([]byte, error)

func (f clientFunc) Run(_ context.Context, _ string, c Command) ([]byte, error) { return f(c) }
