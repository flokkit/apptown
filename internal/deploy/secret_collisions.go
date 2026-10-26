package deploy

import (
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// RefuseNeedSecretCollisions keeps one authority for each consumer variable.
// Equal values do not establish common ownership: either source may rotate.
// The selected plan determines consumers; ordinary environment placeholders
// remain lower precedence and are not scoped secret declarations.
func RefuseNeedSecretCollisions(plan *appcontract.Plan, needNames []string, secrets ConsumerSecrets) error {
	if plan == nil || secrets == nil || len(needNames) == 0 {
		return nil
	}
	consumers := make([]string, 0, len(plan.Parts)+len(plan.Init))
	for _, part := range plan.Parts {
		consumers = append(consumers, part.Name)
	}
	for _, init := range plan.Init {
		consumers = append(consumers, "init-"+init.Name)
	}
	slices.Sort(consumers)
	names := slices.Clone(needNames)
	slices.Sort(names)
	names = slices.Compact(names)
	for _, consumer := range consumers {
		delivered := secrets.EnvFor(consumer)
		var collisions []string
		for _, name := range names {
			if _, ok := delivered[name]; ok {
				collisions = append(collisions, name)
			}
		}
		if len(collisions) > 0 {
			return fmt.Errorf("consumer %q has scoped secrets also supplied by its needs: %s; remove the redundant secret or use a distinct application variable", consumer, strings.Join(collisions, ", "))
		}
	}
	return nil
}
