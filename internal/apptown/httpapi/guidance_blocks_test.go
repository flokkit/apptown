package httpapi

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/wow"
)

func TestBlockWithoutChecksHasNoDecodedDeclarations(t *testing.T) {
	t.Parallel()
	ways := wow.New(databaseSuite.New(t), wow.Dependencies{})
	block, err := ways.CreateBlock(t.Context(), "empty/block", "", "", "alice")
	if err != nil {
		t.Fatal(err)
	}
	checks, err := (GuidanceBlocks{ways: ways}).blockVersionChecks(t.Context(), block.ID, 1)
	if err != nil || checks != nil {
		t.Fatalf("empty checks should be nil, got %+v err=%v", checks, err)
	}
}
