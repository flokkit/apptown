package docker

import (
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

func TestPublicAppCommandPreservesArguments(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, declaration string
		want              []string
	}{
		{"argv", `["printf", "%s", "two words", "$HOME; touch /unexpected"]`, []string{"printf", "%s", "two words", "$HOME; touch /unexpected"}},
		{"shell", `'printf "%s" "two words"'`, []string{"sh", "-c", `printf "%s" "two words"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeClient{}
			f.reply = func(req Command) ([]byte, error) {
				if len(req.Args) > 0 && req.Args[0] == "port" {
					return []byte("0.0.0.0:43210\n"), nil
				}
				return nil, nil
			}
			dk := Scope{Client: f}
			c, err := appcontract.Parse([]byte("version: 1\napp:\n  web:\n    image: app:chosen\n    public: true\n    command: " + tc.declaration + "\n"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewHost(Dependencies{StaticServer: testStaticServer, DFImage: testDF}).StartAppContainer(t.Context(), BootRequest{Docker: dk, Name: "apptown-preview-argv", Workspace: t.TempDir()}, appcontract.Prepare(c, nil).PublicPart(), "", nil); err != nil {
				t.Fatal(err)
			}
			for _, args := range f.args {
				if len(args) > 1 && args[0] == "run" && args[1] == "-d" {
					for i, arg := range args {
						if arg == "app:chosen" && reflect.DeepEqual(args[i+1:], tc.want) {
							return
						}
					}
					t.Fatalf("command changed at execution: %q; want tail %q", args, tc.want)
				}
			}
			t.Fatal("no public app container started")
		})
	}
}
