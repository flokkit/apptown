package fix

import (
	"github.com/flokkit/apptown/internal/check"
	"strings"
	"testing"
)

// Listings are independently observed on the two trees: code beside the update
// is outside this classifier's input and is judged by the workflow's checks.
func TestUpdateBoundsAndFixedTargets(t *testing.T) {
	for _, ecosystem := range []string{"npm", "Go", "PyPI", "RubyGems"} {
		t.Run(ecosystem, func(t *testing.T) {
			before := []Listing{{Ecosystem: ecosystem, Package: "example", Version: "1.2.3", Source: "registry", Attributes: map[string]any{"installScript": false}}}
			targets := []check.Finding{{Package: &check.PackageRef{Name: "example", Ecosystem: ecosystem, FixedIn: "1.2.4"}}}
			for _, tc := range []struct {
				name, version, source string
				extra, attrs          bool
				want                  string
			}{
				{name: "forward", version: "1.2.4", source: "registry"},
				{name: "minor", version: "1.3.0", source: "registry"},
				{name: "major", version: "2.0.0", source: "registry", want: "bound"},
				{name: "backward", version: "1.2.2", source: "registry", want: "forward"},
				{name: "new package", version: "1.2.4", source: "registry", extra: true, want: "new package"},
				{name: "source", version: "1.2.4", source: "other", want: "source"},
				{name: "attribute", version: "1.2.4", source: "registry", attrs: true, want: "attributes"},
				{name: "non semver", version: "garbage", source: "registry", want: "non-semver"},
				{name: "below FixedIn", version: "1.2.3", source: "registry", want: "FixedIn"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					after := []Listing{{Ecosystem: ecosystem, Package: "example", Version: tc.version, Source: tc.source, Attributes: before[0].Attributes}}
					if tc.extra {
						after = append(after, Listing{Ecosystem: ecosystem, Package: "new", Version: "1.0.0", Source: "registry"})
					}
					if tc.attrs {
						after[0].Attributes = map[string]any{"installScript": true}
					}
					_, err := CompareUpdate(before, after, "routine", targets)
					if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
						t.Fatalf("got %v, want %q", err, tc.want)
					}
				})
			}
		})
	}
}
func TestTargetedCommandNeverUsesWholeUpdate(t *testing.T) {
	d := Declaration{Kind: "update", Ecosystem: "npm", Run: "npm update"}
	if _, err := UpdateCommand(d, []string{"example"}, true); err == nil {
		t.Fatal("missing runFor accepted")
	}
	d.RunFor = "npm update {packages}"
	got, err := UpdateCommand(d, []string{"@scope/example", "a'$(touch /bad)"}, true)
	if err != nil || got != "npm update '@scope/example' 'a'\"'\"'$(touch /bad)'" {
		t.Fatalf("command=%q err=%v", got, err)
	}
	if _, err := UpdateCommand(d, []string{"--all"}, true); err == nil {
		t.Fatal("option accepted as package")
	}
}

func TestRoutineZeroAndRemovedPackages(t *testing.T) {
	for _, tc := range []struct{ name, before, after, bound, want string }{
		{"routine advances minor", "1.2.3", "1.3.0", "routine", ""},
		{"zero cannot advance minor", "0.2.3", "0.3.0", "routine", "bound"},
		{"zero patch", "0.2.3", "0.2.4", "routine", ""},
		{"removed", "1.2.3", "", "routine", "package set"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := []Listing{{Ecosystem: "npm", Package: "example", Version: tc.before, Source: "registry", Attributes: map[string]any{}}}
			var after []Listing
			if tc.after != "" {
				after = append(after, before[0])
				after[0].Version = tc.after
			}
			_, err := CompareUpdate(before, after, tc.bound, nil)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("%v, want %s", err, tc.want)
			}
		})
	}
}

func TestDuplicateInstallationsKeepTheirOwnAttributes(t *testing.T) {
	before := []Listing{
		{Ecosystem: "npm", Package: "example", Version: "1.0.0", Source: "registry", Attributes: map[string]any{"dev": true}},
		{Ecosystem: "npm", Package: "example", Version: "1.1.0", Source: "registry", Attributes: map[string]any{"dev": false}},
	}
	after := []Listing{
		{Ecosystem: "npm", Package: "example", Version: "1.1.1", Source: "registry", Attributes: map[string]any{"dev": false}},
		{Ecosystem: "npm", Package: "example", Version: "1.2.0", Source: "registry", Attributes: map[string]any{"dev": true}},
	}
	moves, err := CompareUpdate(before, after, "routine", nil)
	if err != nil || len(moves) != 2 {
		t.Fatalf("independent installations: %+v %v", moves, err)
	}
}
