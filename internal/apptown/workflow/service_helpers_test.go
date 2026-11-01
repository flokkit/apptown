package workflow

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/database"
)

// Runtime selects the runtime parts exercised by a focused service test.
type Runtime struct {
	Advancement *advancer
	Automatic   []string
	Abandonment abandonRecorder
	Starter     *starter
}

// newService uses the production assembly, then supplies only the runtime
// parts the test exercises. Focused tests do not dispatch background work.
func newService(db *database.DB, authority operationAuthority, runtime Runtime) *Service {
	s := newServiceWithAuthority(db, Dependencies{Repositories: &scm.RepositoryLocks{}, Automatic: runtime.Automatic, Abandonment: runtime.Abandonment}, authority)
	s.starter, s.advancer = runtime.Starter, runtime.Advancement
	s.dispatcher.stop()
	return s
}

func newServiceWithAuthority(db *database.DB, deps Dependencies, authority operationAuthority) *Service {
	if deps.Repositories == nil {
		deps.Repositories = &scm.RepositoryLocks{}
	}
	s := New(context.Background(), db, deps)
	s.authority = authority
	return s
}
