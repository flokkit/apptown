package adoption

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/adoption/internal/adoptionsql"
)

// RefreshAdoptedWording follows the library's words on unmodified
// adoptions: the rule a project adopted stays pinned forever, but the
// sentence describing it tracks the library. A copy whose rule or
// parameters differ keeps its own words.
func (s *policyStore) RefreshAdoptedWording(ctx context.Context) error {
	_, err := adoptionsql.New(s.db.SQL()).RefreshAdoptedPolicyWords(ctx)
	return err
}
