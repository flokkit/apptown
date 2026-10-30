package adoption

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// bodyCatalog serves block bodies by id and version.
type bodyCatalog struct {
	runtimeCatalog
	bodies map[ids.ID]map[int]string
}

func (c *bodyCatalog) Block(_ context.Context, ref wow.BlockRef) (wow.BlockObservation, error) {
	if body, ok := c.bodies[ref.ID][ref.Version]; ok {
		return wow.BlockObservation{Version: ref.Version, Body: body}, nil
	}
	return wow.BlockObservation{}, sql.ErrNoRows
}

func receiptOf(blocks ...ReceiptBlock) Source {
	return Source{ProjectID: idstest.N(3), Revision: &RecordedRevision{ProjectID: idstest.N(3), Revision: 1, Receipt: Receipt{Guidance: blocks}}}
}

// The rules an agent reads up front leave a skill's how-to out and name it
// in one line; the skill comes from the same pinned version, so an old
// receipt keeps the old how-to when a newer version exists.
func TestCompiledGuidanceKeepsTheHowToOutOfTheRules(t *testing.T) {
	t.Parallel()
	image, plain := idstest.N(7), idstest.N(8)
	cat := &bodyCatalog{bodies: map[ids.ID]map[int]string{
		image: {
			1: "Run as non-root.\n<!-- skill: How to write the Dockerfile. -->\nuseradd v1",
			2: "Run as non-root.\n<!-- skill: How to write the Dockerfile. -->\nuseradd v2",
		},
		plain: {1: "Say it plainly."},
	}}
	s := New(nil, Dependencies{Ways: cat})
	resolved, err := s.Resolve(t.Context(), receiptOf(
		ReceiptBlock{Name: "build/container-image", BlockID: image, Version: 1},
		ReceiptBlock{Name: "prose/plain", BlockID: plain, Version: 1}))
	if err != nil {
		t.Fatal(err)
	}
	want := "# build/container-image v1\n\nRun as non-root.\n\nSkill `build-container-image` has the how-to: How to write the Dockerfile." +
		"\n\n---\n\n# prose/plain v1\n\nSay it plainly."
	if resolved.Text != want {
		t.Fatalf("rules:\n%s", resolved.Text)
	}
	if len(resolved.Skills) != 1 || resolved.Skills[0] != (wow.Skill{Name: "build-container-image", Description: "How to write the Dockerfile.", Body: "useradd v1"}) {
		t.Fatalf("skills: %+v", resolved.Skills)
	}
	// The hash is over the whole pinned bodies, as before skills existed.
	whole := "# build/container-image v1\n\n" + cat.bodies[image][1] + "\n\n---\n\n# prose/plain v1\n\nSay it plainly."
	if sum := sha256.Sum256([]byte(whole)); resolved.Hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash is not the whole bodies' hash")
	}
	// A change to the how-to alone keeps the rules and moves the hash.
	newer, err := s.Resolve(t.Context(), receiptOf(
		ReceiptBlock{Name: "build/container-image", BlockID: image, Version: 2},
		ReceiptBlock{Name: "prose/plain", BlockID: plain, Version: 1}))
	if err != nil || strings.Replace(newer.Text, "v2", "v1", 1) != resolved.Text || newer.Hash == resolved.Hash || newer.Skills[0].Body != "useradd v2" {
		t.Fatalf("a how-to change: %q %v", newer.Text, err)
	}
}

// A project without a skill reads its guidance exactly as before: the
// joined bodies, hashed.
func TestCompiledGuidanceWithoutSkillsIsUnchanged(t *testing.T) {
	t.Parallel()
	cat := &bodyCatalog{bodies: map[ids.ID]map[int]string{idstest.N(8): {1: " Say it plainly.\n"}}}
	resolved, err := New(nil, Dependencies{Ways: cat}).Resolve(t.Context(), receiptOf(ReceiptBlock{Name: "prose/plain", BlockID: idstest.N(8), Version: 1}))
	sum := sha256.Sum256([]byte("# prose/plain v1\n\nSay it plainly."))
	if err != nil || resolved.Text != "# prose/plain v1\n\nSay it plainly." || resolved.Hash != hex.EncodeToString(sum[:]) || resolved.Skills != nil {
		t.Fatalf("%+v %v", resolved, err)
	}
}

// Two items whose names give one skill name would write one file, so one
// how-to would stand in for the other: compiling refuses, naming both.
func TestCompiledGuidanceRefusesTwoSkillsWithOneName(t *testing.T) {
	t.Parallel()
	cat := &bodyCatalog{bodies: map[ids.ID]map[int]string{
		idstest.N(7): {1: "a\n<!-- skill: one -->\nx"},
		idstest.N(8): {1: "b\n<!-- skill: two -->\ny"},
	}}
	_, err := New(nil, Dependencies{Ways: cat}).Resolve(t.Context(), receiptOf(
		ReceiptBlock{Name: "build/container-image", BlockID: idstest.N(7), Version: 1},
		ReceiptBlock{Name: "build-container/image", BlockID: idstest.N(8), Version: 1}))
	if err == nil || !strings.Contains(err.Error(), "build/container-image and build-container/image") {
		t.Fatalf("colliding skills: %v", err)
	}
}
