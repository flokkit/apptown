package docker

import (
	"context"
	"os"
)

// Own external tests exercise private behavior without expanding production APIs.
var ScopeFromFile = (Scope).fromFile
var ScopeRemoveStack = (Scope).removeOwnedStack

// Test setup delegates to the operations used by production.
func (dk Scope) fromFile(ctx context.Context, path string, args ...string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return dk.Command(ctx, Command{Args: args, Stdin: f})
}
