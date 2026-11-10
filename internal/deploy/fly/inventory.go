package fly

import (
	"context"
	"fmt"
	"sort"

	"github.com/flokkit/apptown/internal/deploy"
)

// Inventory answers what the target's org holds: its apps, each marked as
// this installation's only when one of its Machines carries the
// installation's id. Every other app (another installation's, one a person
// made) is reported only to confirm a record, so it is never called a
// leftover and never offered for removal. Built for a target, it can look,
// never deploy.
type Inventory struct {
	Client       func(context.Context) (*Client, Config, error)
	Installation string
}

var _ deploy.Inventorier = Inventory{}

func (i Inventory) Inventory(ctx context.Context) ([]deploy.Resource, error) {
	c, cfg, err := i.Client(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := c.appsIn(ctx, cfg.Org)
	if err != nil {
		return nil, fmt.Errorf("listing the org's apps: %w", err)
	}
	ms, err := c.orgMachines(ctx, cfg.Org)
	if err != nil {
		return nil, fmt.Errorf("listing the org's Machines: %w", err)
	}
	marked := map[string]bool{}
	machines := map[string]int{}
	for _, m := range ms {
		machines[m.AppName]++
		if i.Installation != "" && m.Config.Metadata[metaInstance] == i.Installation {
			marked[m.AppName] = true
		}
	}
	sort.Slice(apps, func(a, b int) bool { return apps[a].Name < apps[b].Name })
	out := make([]deploy.Resource, 0, len(apps))
	for _, a := range apps {
		r := deploy.Resource{Kind: "app", Name: a.Name, ID: a.ID, Status: a.Status, Region: cfg.Region, URL: appURL(a.Name),
			Detail: fmt.Sprintf("%d Machines", machines[a.Name])}
		if !marked[a.Name] {
			r.Tags = []string{deploy.TagConfirmsOnly}
		}
		out = append(out, r)
	}
	return out, nil
}
