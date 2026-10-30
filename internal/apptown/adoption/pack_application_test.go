package adoption

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestPackInstallationKeepsExistingPoliciesAndSealsExactCandidate(t *testing.T) {
	t.Parallel()
	original := []Policy{{Name: "existing", Rule: "scanFailOnCeiling", With: map[string]any{"max": "high"}}}
	c, err := preparePackInstallation(&Candidate{ProjectID: idstest.N(7), ExpectedRevision: 2, Packs: []PackPick{{Pack: "small", Version: 3}}, Receipt: Receipt{Extensions: []string{"already"}}}, original, wow.PackSetup{Policies: []wow.LibraryPolicy{{Name: "new", Rule: "scanFailOnCeiling", With: map[string]any{"max": "low"}, Origin: "pack/small"}}, Extensions: []string{"new", "already"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Installation.Policies) != 2 || len(c.Receipt.Policies) != 2 || !slices.Equal(c.Installation.Extensions, []string{"already", "new"}) {
		t.Fatalf("installation lost prior selections: %+v", c)
	}
	if len(original) != 1 || original[0].Name != "existing" {
		t.Fatal("preparation mutated prior policy list")
	}
	if err := validatePackApplication(*c); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Candidate
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if err := validatePackApplication(roundTrip); !errors.Is(err, ErrPackApplication) {
		t.Fatalf("transport candidate acquired pack authority: %v", err)
	}
}

func TestPackApplicationSealRejectsMutation(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Candidate){
		"project":                func(c *Candidate) { c.ProjectID = idstest.N(999999) },
		"expected":               func(c *Candidate) { c.ExpectedRevision++ },
		"pack":                   func(c *Candidate) { c.Packs[0].Version++ },
		"receipt":                func(c *Candidate) { c.Receipt.Packs = []string{"different"} },
		"requirements":           func(c *Candidate) { c.RequiredCapabilities = nil },
		"installation policy":    func(c *Candidate) { c.Installation.Policies[0].With["max"] = "critical" },
		"installation extension": func(c *Candidate) { c.Installation.Extensions = append(c.Installation.Extensions, "arbitrary") },
		"digest":                 func(c *Candidate) { c.Digest = "different" },
		"problems":               func(c *Candidate) { c.Problems = []string{"problem"} },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, err := preparePackInstallation(&Candidate{ProjectID: idstest.N(7), ExpectedRevision: 2, Packs: []PackPick{{Pack: "small", Version: 3}}, RequiredCapabilities: []string{"scanner"}}, nil, wow.PackSetup{Policies: []wow.LibraryPolicy{{Name: "bounded", Rule: "scanFailOnCeiling", With: map[string]any{"max": "low"}}}})
			if err != nil {
				t.Fatal(err)
			}
			mutate(c)
			if err := validatePackApplication(*c); !errors.Is(err, ErrPackApplication) {
				t.Fatalf("mutation kept pack authority: %v", err)
			}
		})
	}
	if err := validatePackApplication(Candidate{}); !errors.Is(err, ErrPackApplication) {
		t.Fatalf("unprepared candidate acquired pack authority: %v", err)
	}
}
