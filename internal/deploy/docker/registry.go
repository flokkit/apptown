package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/strs"
)

// LoginRegistry authenticates only images from this registry. Credentials are
// supplied per operation and reach Docker on stdin, never a command argument.
// A login that fails is an error, and the caller pulls nothing: a pull
// after it could succeed with another credential Docker holds, or none.
func LoginRegistry(ctx context.Context, dk Scope, host, username, token, image string) error {
	if host == "" || username == "" || token == "" || !strings.HasPrefix(image, host+"/") {
		return nil
	}
	if out, err := dk.Command(ctx, Command{Args: []string{"login", host, "-u", username, "--password-stdin"}, Stdin: strings.NewReader(token)}); err != nil {
		return fmt.Errorf("logging in to %s: %w: %s", host, err, strs.Truncate(strings.TrimSpace(string(out)), 300))
	}
	return nil
}
