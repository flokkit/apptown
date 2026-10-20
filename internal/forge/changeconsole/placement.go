package changeconsole

import (
	"context"
	"fmt"
	"strings"

	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow"
)

// Placement opens chats on the worker (conversation.WorkerPlacement): the
// registries its builds use must be reachable from the worker, and the
// change's folder is published before the worker is sent it. It is nil
// without a worker, and then every chat opens on AppTown.
func (c *Service) Placement() conversation.WorkerPlacement {
	if c.saves.remote == nil {
		return nil
	}
	return placement{c}
}

type placement struct{ c *Service }

// Ready asks only whether each registry answers at all from the worker:
// not whether its packages install there. Every registry needs a positive
// answer; one the worker leaves out is unreachable.
func (w placement) Ready(ctx context.Context) error {
	bound, err := integration.RegistryBindings(ctx, w.c.db)
	if err != nil {
		return fmt.Errorf("reading the registries the chat's builds use: %w", err)
	}
	var urls []string
	names := map[string]string{}
	for _, b := range bound {
		urls = append(urls, b.URL)
		names[b.URL] = b.Name
	}
	if len(urls) == 0 {
		return nil
	}
	answers, err := w.c.saves.remote.Sessions.Reach(ctx, urls)
	if err != nil {
		return fmt.Errorf("asking the worker which registries it can reach: %w", err)
	}
	reached := map[string]bool{}
	for _, a := range answers {
		reached[a.URL] = reached[a.URL] || a.Reachable
	}
	var unreachable []string
	for _, u := range urls {
		if !reached[u] {
			why := "no answer"
			for _, a := range answers {
				if a.URL == u && a.Error != "" {
					why = a.Error
				}
			}
			unreachable = append(unreachable, fmt.Sprintf("%s (%s: %s)", names[u], u, why))
		}
	}
	if len(unreachable) > 0 {
		return fmt.Errorf("it cannot reach the registry %s", strings.Join(unreachable, ", "))
	}
	return nil
}

func (w placement) Publish(ctx context.Context, p *project.Project, ch *workflow.Change, holder string) error {
	_, err := w.c.folders.PublishChangeFolder(ctx, p, ch, holder)
	return err
}
