package conversation

import (
	"context"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/ids"
)

// Context attributes an already authenticated console to its owning person.
func Context(ctx context.Context, s *console.Session) context.Context {
	pr := Principal(s)
	return access.WithCaller(ctx, access.Caller{Principal: pr, Verified: true, Receipt: pr.Name})
}
func Principal(s *console.Session) access.Principal {
	// The person is the opener's principal, whatever their name is now.
	opener, _ := ids.Parse(s.Labels[OpenerLabel])
	return access.Principal{ID: opener, Kind: "instrument", Name: s.Actor + " via console/" + s.ID, Person: s.Actor,
		Instrument: "console", Via: "console", ProjectID: Of(s).ProjectID, ConsoleID: s.ID}
}
