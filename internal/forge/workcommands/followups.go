package workcommands

import (
	"context"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow"
)

// FollowUps applies already-admitted durable effects. River owns delivery;
// Workflow owns transition/intent transactions and current effect observations.
type FollowUps struct {
	DB         *database.DB
	Work       *workflow.Service
	Landings   AcceptedLandings
	Uplifts    UpliftSettlement
	Resources  Resources
	Narratives interface {
		Refresh(context.Context, ids.ID) error
	}
	// Chats ends a closed change's chats before its folder is removed, and
	// tells an open change's chats what they would otherwise act wrongly
	// without.
	Chats interface {
		EndChangeChats(ctx context.Context, projectID, changeID ids.ID) error
		TellChangeChats(ctx context.Context, projectID, changeID ids.ID, msg string) error
	}
}

func engineCaller(ctx context.Context) context.Context {
	principal := access.Principal{Kind: "instrument", Name: "system", Instrument: "forge", Via: "engine"}
	return access.WithCaller(ctx, access.Caller{Principal: principal, Verified: true, Receipt: principal.Name})
}
