package adoption

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
)

func validationCandidate(t *testing.T) (*database.DB, *Service, Candidate) {
	t.Helper()
	db := testDB(t)
	testAccounts(t, db, "alice")
	pid := adoptionTestProject(t, db, "validate", "Validate")
	if err := db.InTx(context.Background(), func(tx *database.Tx) error { return InitializeIn(t.Context(), tx, pid, 0, "") }); err != nil {
		t.Fatal(err)
	}
	ways := wow.New(db, wow.Dependencies{})
	created, err := ways.CreateBlock(t.Context(), "held-guide", "held body", "", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	block := created.ID
	yaml := fmt.Sprintf(laneYAML, "held-flow", "held publication", "")
	if _, version, err := ways.PublishWorkflow(t.Context(), yaml, "fixture"); err != nil || version != 1 {
		t.Fatalf("publish workflow v1: %d %v", version, err)
	}
	if version, err := ways.PublishPack(t.Context(), "held-pack", "", `{"guidance":["held-guide"],"workflows":["held-flow"]}`, ""); err != nil || version != 1 {
		t.Fatalf("publish pack: %d %v", version, err)
	}
	if err := ways.BumpBlock(t.Context(), block, "intermediate body", "", "fixture", "held-guide"); err != nil {
		t.Fatal(err)
	}
	if err := ways.BumpBlock(t.Context(), block, "newer body", "", "fixture", "held-guide"); err != nil {
		t.Fatal(err)
	}
	for version := 2; version <= 3; version++ {
		if _, got, err := ways.PublishWorkflow(t.Context(), yaml+fmt.Sprintf("\n# newer %d\n", version), "fixture"); err != nil || got != version {
			t.Fatalf("publish workflow v%d: %d %v", version, got, err)
		}
	}
	c := Candidate{ProjectID: pid, Receipt: EmptyReceipt(), Packs: []PackPick{{Pack: "held-pack", Version: 1}}, Installation: Installation{Policies: []Policy{{Name: "required", Rule: "requireCapabilityBound", With: map[string]any{"capability": "registry"}}}, Extensions: []string{"addon"}}}
	c.Receipt.Packs = []string{"held-pack"}
	c.Receipt.Guidance = []ReceiptBlock{{BlockID: block, Name: "held-guide", Version: 1}}
	c.Receipt.Workflows = []ReceiptWorkflow{{Name: "held-flow", Version: 1, YAMLHash: hashText(yaml)}}
	policy, err := policyReceipt(c.Installation.Policies[0])
	if err != nil {
		t.Fatal(err)
	}
	c.Receipt.Policies = []ReceiptPolicy{policy}
	c.Receipt.Extensions = []string{"addon"}
	c.Digest = Digest(c.Receipt)
	return db, New(db, Dependencies{Ways: wow.New(db, wow.Dependencies{})}), c
}

func TestCandidateValidationUsesFrozenPublicationsAndRejectsTampering(t *testing.T) {
	t.Parallel()
	db, service, prepared := validationCandidate(t)
	if err := service.validateCandidateIn(context.Background(), db, prepared.ProjectID, prepared); err != nil {
		t.Fatalf("valid held versions rejected for newer catalog: %v", err)
	}
	cases := map[string]func(*Candidate){
		"apptownd name":        func(c *Candidate) { c.Receipt.Guidance[0].Name = "another-name" },
		"missing version":      func(c *Candidate) { c.Receipt.Guidance[0].Version = 2 },
		"unpublished pack pin": func(c *Candidate) { c.Receipt.Guidance[0].Version = 3 },
		"duplicate identity": func(c *Candidate) {
			b := c.Receipt.Guidance[0]
			b.Position = 1
			c.Receipt.Guidance = append(c.Receipt.Guidance, b)
		},
		"position":                  func(c *Candidate) { c.Receipt.Guidance[0].Position = 9 },
		"missing required block":    func(c *Candidate) { c.Receipt.Guidance = nil },
		"disabled required block":   func(c *Candidate) { c.Receipt.Guidance[0].Disabled = true },
		"workflow hash":             func(c *Candidate) { c.Receipt.Workflows[0].YAMLHash = "invented" },
		"workflow system":           func(c *Candidate) { c.Receipt.Workflows[0].System = true },
		"missing required workflow": func(c *Candidate) { c.Receipt.Workflows = nil },
		"pack name":                 func(c *Candidate) { c.Receipt.Packs = []string{"other"} },
		"pack left out":             func(c *Candidate) { c.Receipt.Packs = nil },
		"duplicate pack":            func(c *Candidate) { c.Packs = append(c.Packs, c.Packs[0]) },
		"policy hash":               func(c *Candidate) { c.Receipt.Policies[0].ConfigHash = "invented" },
		"policy rule":               func(c *Candidate) { c.Receipt.Policies[0].Rule = "scanFailOnCeiling" },
		"missing extension":         func(c *Candidate) { c.Receipt.Extensions = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := prepared
			c.Receipt = cloneReceipt(prepared.Receipt)
			c.Packs = slices.Clone(prepared.Packs)
			mutate(&c)
			c.Digest = Digest(c.Receipt)
			if err := service.validateCandidateIn(context.Background(), db, c.ProjectID, c); err == nil {
				t.Fatal("apptownd candidate accepted despite a self-consistent digest")
			}
		})
	}
	prepared.Digest = "invented"
	if err := service.validateCandidateIn(context.Background(), db, prepared.ProjectID, prepared); err == nil {
		t.Fatal("mismatched digest accepted")
	}
}
