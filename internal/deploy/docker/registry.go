package docker

import (
	"context"
	"strings"
)

// LoginRegistry authenticates only images from this registry. Credentials are
// supplied per operation and reach Docker on stdin, never a command argument.
// A failed login retains the existing best-effort behavior: the pull reports it.
func LoginRegistry(ctx context.Context, dk Scope, host, username, token, image string) {
	if host == "" || username == "" || token == "" || !strings.HasPrefix(image, host+"/") {
		return
	}
	_, _ = dk.Command(ctx, Command{Args: []string{"login", host, "-u", username, "--password-stdin"}, Stdin: strings.NewReader(token)})
}
