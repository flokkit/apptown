package adoption

import (
	"context"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// PrepareAssessed refuses anything other than the candidate that was assessed.
func (s *Service) PrepareAssessed(ctx context.Context, pid ids.ID, req Request, revision int64, digest string) (*Candidate, error) {
	c, err := s.Prepare(ctx, pid, req)
	if err != nil {
		return nil, err
	}
	if digest == "" || c.ExpectedRevision != revision || c.Digest != digest {
		return nil, fault.New(fault.Conflict, "what this project would adopt has changed since it was assessed — run the what-if again")
	}
	return c, nil
}
