package portainer

import (
	"context"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Adapter unit tests receive the snapshot their resolving domain owner verified.
// Live verification and wrong-destination requests have separate scope tests.
func scopeFixtureScope(base string) *Scope {
	return &Scope{Version: 1, APIBase: base, EndpointID: 3, DaemonID: "fixture-daemon"}
}
func scopeFixtureCompose(body string) string {
	return "x-apptown-deployment: " + idstest.N(7).String() + "\n" + body
}

// admitDisk admits every release: an endpoint with room on its disk.
func admitDisk(context.Context) error { return nil }
