package wow

// BlockTier is a guidance block's reach: an ordinary block a project
// imports, or a platform block that rides on every repo-backed project.
type BlockTier uint8

const (
	TierOrdinary BlockTier = iota + 1
	TierPlatform
)

// The stored words: ” for ordinary, 'platform' for AppTown's own contract.
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
