package wow

import "testing"

// Publications are offered immediately, without a what-if or an enabled janitor.
func TestNewestPublicationIsOfferedImmediately(t *testing.T) {
	t.Parallel()
	packs := NewPacks(testDB(t))
	for _, origin := range []string{"", "stock"} {
		name := "team"
		if origin != "" {
			name = "shipped"
		}
		for i, spec := range []string{`{}`, `{"toolchain":"go"}`} {
			v, err := packs.Publish(t.Context(), name, "Team", spec, origin)
			if err != nil || v != i+1 {
				t.Fatalf("publish: %d %v", v, err)
			}
			h, err := packs.history(t.Context(), name)
			if err != nil || h.Recommended != int64(v) || h.Latest != int64(v) {
				t.Fatalf("history: %+v %v", h, err)
			}
			offered, err := packs.offeredContentsOf(t.Context(), name)
			if err != nil || offered.Version != int64(v) {
				t.Fatalf("offered: %+v %v", offered, err)
			}
			if v == 2 && offered.Toolchain != "go" {
				t.Fatalf("old contents: %+v", offered)
			}
		}
	}
}
