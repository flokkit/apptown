package wow

import (
	"encoding/json"
)

// CatalogState is derived, never stored (§catalog): a catalog change is
// catalogued when its latest version is the promoted one, else it is a
// draft, because a new version always needs fresh evaluation receipts.
type CatalogState uint8

const (
	CatalogDraft CatalogState = iota + 1
	Catalogued
)

func (s CatalogState) String() string {
	switch s {
	case CatalogDraft:
		return "draft"
	case Catalogued:
		return "catalogued"
	default:
		return "invalid-catalog-state"
	}
}

// CatalogStateOf is the one rule.
func CatalogStateOf(latest, promoted int) CatalogState {
	if promoted != 0 && promoted == latest {
		return Catalogued
	}
	return CatalogDraft
}

func (s CatalogState) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// BlockTier is a guidance block's reach: an ordinary block a project
// imports, or a platform block that rides on every repo-backed project.
type BlockTier uint8

const (
	TierOrdinary BlockTier = iota + 1
	TierPlatform
)

// The stored words: ” for ordinary, 'platform' for the forge's own contract.
func (t BlockTier) String() string {
	switch t {
	case TierOrdinary:
		return ""
	case TierPlatform:
		return "platform"
	default:
		return "invalid-tier"
	}
}
