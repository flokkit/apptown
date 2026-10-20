package pi

import (
	"strings"
	"testing"
)

// A chat's Docker builds use the buildx builder provisioning created: the
// console selects it by name (BUILDX_BUILDER), and that name is the one
// buildx create made (review of the AppTown rename, R1).
//
// Kills: the selection or the created name changed alone.
func TestConsoleSelectsTheBuilderItCreated(t *testing.T) {
	r, in, f := fixture(t)
	if err := r.provision(in); err != nil {
		t.Fatal(err)
	}
	var builder, selected string
	for _, args := range f.calls {
		call := strings.Join(args, " ")
		if strings.Contains(call, "buildx create") {
			for i, arg := range args {
				if arg == "--name" && i+1 < len(args) {
					builder = args[i+1]
				}
			}
		}
		if args[0] == "create" {
			for _, arg := range args {
				if value, ok := strings.CutPrefix(arg, "BUILDX_BUILDER="); ok {
					selected = value
				}
			}
		}
	}
	if builder == "" || selected != builder {
		t.Fatalf("console selects builder %q but provision created %q", selected, builder)
	}
}
