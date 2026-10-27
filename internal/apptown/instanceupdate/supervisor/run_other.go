//go:build !linux

package supervisor

import (
	"context"
	"errors"
)

func RunDaemon(context.Context, string, Host) error {
	return errors.New("managed startup requires Linux")
}
func RunContainer(context.Context, string, Host) error {
	return errors.New("managed startup requires Linux")
}
