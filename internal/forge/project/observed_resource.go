package project

import (
	"context"
	"log"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
)

// ObservedResource refreshes the owner group, as existing HTTP checks do. Keep
// lookup-failure behavior distinct from the stricter domain authority API.
func ObservedResource(ctx context.Context, projects *Projects, p *Project) access.Resource {
	if p == nil {
		return access.Resource{Type: "instance"}
	}
	group := ""
	current, err := projects.ByID(ctx, p.ID)
	if err == nil {
		group = current.OwnerGroup
	} else if _, err := database.Lookup(err); err != nil {
		log.Printf("read failed: %v", err)
	}
	return access.Resource{Type: "project", ID: p.ID.String(), ProjectID: p.ID, ProjectName: p.Name, Group: group}
}
