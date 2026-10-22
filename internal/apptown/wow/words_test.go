package wow

import (
	"testing"
)

func TestBlockTierWords(t *testing.T) {
	if TierOrdinary.String() != "" || TierPlatform.String() != "platform" {
		t.Fatal("the stored words: empty for ordinary, platform for AppTown's own contract")
	}
	var zero BlockTier
	if zero.String() != "invalid-tier" {
		t.Fatal("the zero tier is not a stored word")
	}
}
