package workflowexec

import (
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/ids"
)

// A workflow is refused for want of a model only when one of its model
// steps would get none, asked as the step asks: a step's own model, the
// workflow's default or a catalog pin is enough without an instance
// default, and a workflow with no model step needs none.
//
// Kills: refusing on the empty instance default alone, which would refuse a
// workflow whose steps name their models.
func TestOnlyAModelStepWithNoModelIsRefused(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	none := ModelRefusal(model.NewSelection(model.New(db)))
	withDefault := ModelRefusal(selectionWithDefault(t, databaseSuite.New(t), "anthropic/claude-sonnet"))
	spec := func(defaults string, steps ...schema.Step) *schema.Spec {
		return &schema.Spec{Defaults: schema.Defaults{Model: defaults}, Stages: []schema.Stage{{Name: "work", Steps: steps}}}
	}
	agent := schema.Step{Name: "apply-change", Uses: AgentKind}
	review := schema.Step{Name: "review", Uses: ReviewKind}
	named := func(s schema.Step, m string) schema.Step { s.With = map[string]any{"model": m}; return s }
	project := ids.New(time.Now()) // no such project: the standard profile, as for any project with no slots of its own
	for _, c := range []struct {
		name    string
		spec    *schema.Spec
		inputs  string
		refused bool
	}{
		{"an agent with no model anywhere", spec("", agent), "{}", true},
		{"a review with no model anywhere", spec("", review), "{}", true},
		{"no model step", spec("", schema.Step{Name: "accept", Uses: "approval"}), "{}", false},
		{"the step names its model", spec("", named(agent, "openrouter/x"), named(review, "openrouter/y")), "{}", false},
		{"the workflow's default", spec("openrouter/x", agent), "{}", false},
		{"a catalog pin", spec("", agent), `{"catalog":{"model":"openrouter/x"}}`, false},
	} {
		if got := none(t.Context(), project, c.spec, c.inputs); (got != "") != c.refused {
			t.Errorf("%s, no instance default: refusal %q", c.name, got)
		}
		if got := withDefault(t.Context(), project, c.spec, c.inputs); got != "" {
			t.Errorf("%s, with an instance default: refused %q", c.name, got)
		}
	}
}
