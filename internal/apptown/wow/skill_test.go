package wow

import (
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/stock"
)

// A block's body splits at its one skill marker into the rules every agent
// reads and the how-to it loads on demand; a body with no marker is exactly
// as written.
func TestSplitSkillSeparatesRulesFromTheHowTo(t *testing.T) {
	t.Parallel()
	body := "Run as non-root.\n\n<!-- skill: How to write the Dockerfile: users, stages. -->\n\n# Writing it\n\nuseradd app\n"
	rules, skill, err := SplitSkill("build/container-image", body)
	if err != nil || skill == nil {
		t.Fatalf("split: %+v %v", skill, err)
	}
	if rules != "Run as non-root." || strings.Contains(rules, "useradd") {
		t.Fatalf("rules carry only the always-read part: %q", rules)
	}
	if skill.Name != "build-container-image" || skill.Description != "How to write the Dockerfile: users, stages." || skill.Body != "# Writing it\n\nuseradd app" {
		t.Fatalf("skill: %+v", skill)
	}
	plain := "Say it plainly.\n\n<!-- a comment, not a skill -->\n"
	if rules, skill, err := SplitSkill("prose/plain", plain); err != nil || skill != nil || rules != plain {
		t.Fatalf("a body without a marker is unchanged: %q %+v %v", rules, skill, err)
	}
}

func TestSplitSkillRefusesWhatCannotBeASkill(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct{ block, body string }{
		"two markers":       {"a/b", "r\n<!-- skill: one -->\nx\n<!-- skill: two -->\ny"},
		"empty description": {"a/b", "r\n<!-- skill:  -->\nx"},
		"no how-to":         {"a/b", "r\n<!-- skill: does things -->\n\n"},
		"double hyphen":     {"build-/image", "r\n<!-- skill: does things -->\nx"},
		"trailing hyphen":   {"build/image-", "r\n<!-- skill: does things -->\nx"},
		"too long":          {"a/" + strings.Repeat("b", 63), "r\n<!-- skill: does things -->\nx"},
		"long description":  {"a/b", "r\n<!-- skill: " + strings.Repeat("d", 1025) + " -->\nx"},
		"unclosed":          {"a/b", "r\n<!-- skill: does things\nx"},
	} {
		if _, _, err := SplitSkill(c.block, c.body); !errors.Is(err, errSkill) {
			t.Errorf("%s: refused with ErrSkill, got %v", name, err)
		}
	}
}

// Every door that writes a block body refuses a malformed skill: shipped
// stock, a new block, an edit and a bump.
func TestBlockWritesRefuseAMalformedSkill(t *testing.T) {
	t.Parallel()
	bad := "rules\n<!-- skill: -->\nhow"
	if _, err := parseStockGuidance("---\nname: a/b\n---\n" + bad); !errors.Is(err, errSkill) {
		t.Fatalf("stock: %v", err)
	}
	db := testDB(t)
	s := New(db, Dependencies{})
	if _, err := s.CreateBlock(t.Context(), "house/bad", bad, "", "alice"); !errors.Is(err, errSkill) {
		t.Fatalf("create: %v", err)
	}
	b, err := s.CreateBlock(t.Context(), "house/good", "rules\n<!-- skill: does things -->\nhow", "", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditBlock(t.Context(), b.ID, BlockPatch{BodyMD: &bad}, "alice"); !errors.Is(err, errSkill) {
		t.Fatalf("edit: %v", err)
	}
	if err := s.BumpBlock(t.Context(), b.ID, bad, "", "alice", b.Name); !errors.Is(err, errSkill) {
		t.Fatalf("bump: %v", err)
	}
	if latest, _ := s.library.byID(t.Context(), b.ID); latest.LatestVersion != 1 {
		t.Fatalf("a refused write records no version: v%d", latest.LatestVersion)
	}
}

// The shipped items that carry a skill split cleanly, and keep their
// checks: a skipped skill cannot let a broken result through.
func TestStockSkillsSplit(t *testing.T) {
	t.Parallel()
	blocks, skipped := ReadStockGuidance(stock.FS, nil)
	if skipped != 0 {
		t.Fatalf("skipped %d stock blocks", skipped)
	}
	with := map[string]bool{}
	for _, b := range blocks {
		_, skill, err := SplitSkill(b.Name, b.Body)
		if err != nil {
			t.Fatal(err)
		}
		if b.Name == "data/networked-database" {
			// Migration limits govern every task: a migration that needs
			// more than the owner passes tests and fails when deployed.
			rules, _, _ := SplitSkill(b.Name, b.Body)
			for _, term := range []string{"CREATE ROLE", "CREATE DATABASE", "untrusted extension"} {
				if !strings.Contains(rules, term) {
					t.Errorf("%s's always-read rules omit %q", b.Name, term)
				}
			}
		}
		if skill != nil {
			with[b.Name] = true
			if b.Name == "build/container-image" && b.ChecksJSON == "" {
				t.Errorf("%s lost its checks", b.Name)
			}
		}
	}
	for _, name := range []string{"build/container-image", "data/networked-database"} {
		if !with[name] {
			t.Errorf("%s carries a skill", name)
		}
	}
}
