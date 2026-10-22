package scm

import (
	"context"
	"fmt"
	"sync"

	"github.com/flokkit/apptown/internal/ids"
)

// RepositoryLocks is a value supplied by assembly to every writer of the same
// repository namespace. It has no package-global state and no retained idle keys.
// A zero value is ready to use. Do not copy it after first use.
type RepositoryLocks struct {
	mu      sync.Mutex
	entries map[ids.ID]*repositoryLock
}
type repositoryLock struct {
	token chan struct{}
	users int
}

func (l *RepositoryLocks) Acquire(ctx context.Context, projectID ids.ID) (func(), error) {
	if l == nil || projectID.IsZero() {
		return nil, fmt.Errorf("repository serialization requires its project")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	if l.entries == nil {
		l.entries = make(map[ids.ID]*repositoryLock)
	}
	entry := l.entries[projectID]
	if entry == nil {
		entry = &repositoryLock{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		l.entries[projectID] = entry
	}
	entry.users++
	l.mu.Unlock()
	forget := func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		entry.users--
		if entry.users == 0 {
			delete(l.entries, projectID)
		}
	}
	select {
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	case <-entry.token:
		if err := ctx.Err(); err != nil {
			entry.token <- struct{}{}
			forget()
			return nil, err
		}
	}
	var once sync.Once
	return func() { once.Do(func() { entry.token <- struct{}{}; forget() }) }, nil
}
