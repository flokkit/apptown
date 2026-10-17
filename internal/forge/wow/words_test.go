package wow

import (
	"testing"
)

func TestCatalogStateOf(t *testing.T) {
	if CatalogStateOf(1, 0) != CatalogDraft || CatalogStateOf(3, 3) != Catalogued || CatalogStateOf(4, 3) != CatalogDraft {
		t.Fatal("catalogued only when the latest version is the promoted one")
	}
	if Catalogued.String() != "catalogued" || CatalogDraft.String() != "draft" {
		t.Fatal("the page's words")
	}
}

func TestBlockTierWords(t *testing.T) {
	if TierOrdinary.String() != "" || TierPlatform.String() != "platform" {
		t.Fatal("the stored words: empty for ordinary, platform for the forge's own contract")
	}
	var zero BlockTier
	if zero.String() != "invalid-tier" {
		t.Fatal("the zero tier is not a stored word")
	}
}
