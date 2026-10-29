package numbered

import "testing"

func TestParseReadsSlugHashPositiveNumber(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]bool{"shop#3": true, "shop#0": false, "shop#-1": false, "#3": false, "shop": false, "shop#x": false} {
		if _, _, ok := Parse(in); ok != want {
			t.Errorf("Parse(%q) ok=%v, want %v", in, ok, want)
		}
	}
	if slug, n, _ := Parse("shop#12"); slug != "shop" || n != 12 {
		t.Fatalf("Parse(shop#12) = %q %d", slug, n)
	}
}
