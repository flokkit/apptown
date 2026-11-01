package wow

// blockTier is a guidance block's reach: an ordinary block a project
// imports, or a platform block that rides on every repo-backed project.
type blockTier uint8

const (
	tierOrdinary blockTier = iota + 1
	TierPlatform
)

// The stored words: ” for ordinary, 'platform' for AppTown's own contract.
func (t blockTier) String() string {
	switch t {
	case tierOrdinary:
		return ""
	case TierPlatform:
		return "platform"
	default:
		return "invalid-tier"
	}
}
