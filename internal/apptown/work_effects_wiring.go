package apptown

import "github.com/flokkit/apptown/internal/apptown/workcommands"

func workResourcesFor(a *App) workcommands.Resources {
	return workcommands.Resources{DB: a.DB, Previews: a.Preview, DataDir: a.cfg.dataDir}
}
func workflowFollowUps(a *App) workcommands.FollowUps {
	return workcommands.FollowUps{
		DB: a.DB, Work: a.Work,
		Landings: workcommands.AcceptedLandings{DB: a.DB, Work: a.Work, Lanes: a.lifecycle},
		Uplifts:  upliftSettlementFor(a), Resources: workResourcesFor(a), Narratives: a.narratives,
		Chats: a.changeConsole,
	}
}
