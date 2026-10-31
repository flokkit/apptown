package check

import (
	"reflect"
	"testing"
)

func TestToolApplicabilityMatchesTheSamePart(t *testing.T) {
	t.Parallel()
	src := Source{Parts: []Part{
		{Name: "api", Folder: ".", Kinds: []string{KindBuild, KindContainer}},
		{Name: "site", Folder: ".", Kinds: []string{KindStatic}},
	}}
	for _, tc := range []struct {
		check, scanner []string
		want           []Place
	}{
		{[]string{"repository"}, []string{"build"}, []Place{{Folder: ".", Parts: []string{"api"}}}},
		{[]string{"part"}, []string{"build"}, []Place{{Folder: ".", Parts: []string{"api"}}}},
		{[]string{"static"}, []string{"build"}, nil},
		{[]string{"build"}, nil, []Place{{Folder: ".", Parts: []string{"api"}}}},
	} {
		got, why, err := ToolPlaces(tc.check, tc.scanner, src)
		if err != nil || !reflect.DeepEqual(got, tc.want) || (len(got) == 0 && why == "") {
			t.Errorf("%v limited by %v: %v %q %v", tc.check, tc.scanner, got, why, err)
		}
	}
}
