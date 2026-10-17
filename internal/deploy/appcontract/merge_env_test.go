package appcontract

import (
	"maps"
	"testing"
)

func TestMergeEnvOverrideWinsAndInputsStayAsWritten(t *testing.T) {
	base := map[string]string{"A": "base", "B": "base"}
	override := map[string]string{"B": "override", "C": "override"}
	got := MergeEnv(base, override)
	if want := map[string]string{"A": "base", "B": "override", "C": "override"}; !maps.Equal(got, want) {
		t.Fatalf("MergeEnv = %v, want %v", got, want)
	}
	got["A"] = "changed"
	if base["A"] != "base" || len(base) != 2 || len(override) != 2 {
		t.Fatalf("an input changed: base %v, override %v", base, override)
	}
	if got := MergeEnv(nil, nil); got == nil || len(got) != 0 {
		t.Fatalf("MergeEnv(nil, nil) = %#v, want an empty map", got)
	}
}
