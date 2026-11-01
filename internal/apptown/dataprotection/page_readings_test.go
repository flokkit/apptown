package dataprotection

import (
	"context"
	"github.com/flokkit/apptown/internal/ids"
	"testing"
)

// Observation is an explicit step in tests of protection decisions. Public
// reads remain the assertion door, as they are for pages after a scheduled job.
func (s *Service) protectionServiceForTest() *Service { return s }

func collectProtectionForTest(t *testing.T, owner interface{ protectionServiceForTest() *Service }, project ids.ID, name string) {
	s := owner.protectionServiceForTest()
	t.Helper()
	dep, err := s.deployments.ByName(t.Context(), project, name)
	if err != nil {
		t.Fatal(err)
	}
	o := NewProtectionObserver(s, nil)
	_, identity, err := o.resolve(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A failed assessment is recorded and is the state these tests inspect.
	_ = o.run(context.Background(), dep.ID, identity)
}
