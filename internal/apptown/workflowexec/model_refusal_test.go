package workflowexec

import (
	"testing"

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
	project := ids.Nil // no project of its own: the standard profile, as for any project with no slots of its own
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
		if got, err := none(t.Context(), project, c.spec, c.inputs); err != nil || (got != "") != c.refused {
			t.Errorf("%s, no instance default: refusal %q (%v)", c.name, got, err)
		}
		if got, err := withDefault(t.Context(), project, c.spec, c.inputs); err != nil || got != "" {
			t.Errorf("%s, with an instance default: refused %q (%v)", c.name, got, err)
		}
	}
}

// A model selection that cannot be read is an admission error, never
// "no AI model is set up": the person is not told to add a key on a
// database fault.
func TestAnUnreadModelSelectionIsNotNoModel(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	refusal := ModelRefusal(selectionWithDefault(t, db, "anthropic/claude-sonnet"))
	spec := &schema.Spec{Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "apply-change", Uses: AgentKind}}}}}
	defer db.FailNextStatement("FROM model_slots")()
	if got, err := refusal(t.Context(), ids.Nil, spec, "{}"); err == nil {
		t.Fatalf("an unread selection answered %q", got)
	}
}
