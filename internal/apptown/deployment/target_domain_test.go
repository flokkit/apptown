package deployment

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeTargetDomain(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", ""}, {"  ", ""}, {" *.Apps.Example ", "Apps.Example"}, {"apps.example.", "apps.example."}, {"local", "local"}, {"xn--bcher-kva.example", "xn--bcher-kva.example"},
		{strings.Repeat("a", 63) + ".example", strings.Repeat("a", 63) + ".example"},
		{strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61), strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)},
	} {
		got, err := normalizeTargetDomain(tc.input)
		if err != nil || got != tc.want {
			t.Errorf("%q: got %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
	for _, input := range []string{"apps.example?x=1", "apps.example?", "apps.example#x", "apps.example#", "person@apps.example", "https://apps.example", "apps.example/path", "apps.example:443", "127.0.0.1", "[::1]", "::1", "*.", "*.*.example", "apps..example", ".example", "apps.example..", "-apps.example", "apps-.example", "apps_example", "apps\texample", "apps\nexample", "bücher.example", strings.Repeat("a", 64) + ".example", strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 62)} {
		if _, err := normalizeTargetDomain(input); !errors.Is(err, errTargetDomain) {
			t.Errorf("invalid %q: %v", input, err)
		}
	}
}
