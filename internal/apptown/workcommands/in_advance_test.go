package workcommands

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
)

// lanesOf answers a project's selected workflows from YAML bodies by name.
type lanesOf map[string]string

func (l lanesOf) ResolveWorkflow(_ context.Context, _ *project.Project, name string) (*adoption.ResolvedWorkflow, error) {
	body, ok := l[name]
	if !ok {
		return nil, errors.New("not selected")
	}
	doc, err := schema.ParseWorkflow([]byte(body))
	if err != nil {
		return nil, err
	}
	return &adoption.ResolvedWorkflow{Name: name, Version: 3, Doc: doc}, nil
}

func approvalWorkflow(name, role, step, inAdvance string) string {
	extra := ""
	if inAdvance != "" {
		extra = fmt.Sprintf("\n            inAdvance: %q", inAdvance)
	}
	return fmt.Sprintf(`apiVersion: apptown.dev/v1
kind: Workflow
metadata:
  name: %s
spec:
  kind: pipeline
  role: %s
  stages:
    - name: work
      steps:
        - name: %s
          uses: approval
          with:
            means: "I looked."%s
`, name, role, step, extra)
}

// The question names the lane an adoption runs and each approval's
// sentence for a yes in advance, from the project's own workflows: two
// projects whose landings say different things are asked different things.
// It offers none where an approval says nothing of one, and a yes counts
// only for exactly the sentences asked, in order.
func TestTheInAdvanceQuestionIsEachProjectsOwn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := &project.Project{Slug: "alpha"}
	quick := approvalWorkflow("quick-change", "working", "accept", "I accept it in advance")
	for _, tc := range []struct {
		name       string
		lanes      lanesOf
		workflow   string
		shown      []string
		notOffered string
	}{
		{"quick-change and a landing", lanesOf{"quick-change": quick, "land": approvalWorkflow("land", "landing", "approve-landing", "I answer for it in advance")}, "quick-change", []string{"I accept it in advance", "I answer for it in advance"}, ""},
		{"another project's landing", lanesOf{"quick-change": quick, "land": approvalWorkflow("land", "landing", "approve-landing", "Ship it if it is clean")}, "quick-change", []string{"I accept it in advance", "Ship it if it is clean"}, ""},

		{"the acceptance says nothing", lanesOf{"quick-change": approvalWorkflow("quick-change", "working", "accept", ""), "land": approvalWorkflow("land", "landing", "approve-landing", "I answer for it in advance")}, "quick-change", nil, "quick-change v3's accept approval does not say"},
		{"the landing says nothing", lanesOf{"quick-change": quick, "land": approvalWorkflow("land", "landing", "approve-landing", "")}, "quick-change", nil, "approve-landing approval does not say"},
	} {
		working, err := tc.lanes.ResolveWorkflow(ctx, p, "quick-change")
		if err != nil {
			t.Fatal(err)
		}
		landing, err := tc.lanes.ResolveWorkflow(ctx, p, "land")
		if err != nil {
			t.Fatal(err)
		}
		q := inAdvanceQuestion(working, landing)
		if q.Workflow != tc.workflow || !slices.Equal(q.Shown(), tc.shown) || (tc.notOffered == "") != (q.NotOffered == "") || !strings.Contains(q.NotOffered, tc.notOffered) {
			t.Fatalf("%s: %+v", tc.name, q)
		}
		given, err := givenInAdvance(p.Slug, q, tc.shown)
		if tc.notOffered != "" {
			if err == nil || !strings.Contains(err.Error(), "no yes in advance on alpha") {
				t.Fatalf("%s: a yes where none is offered: %+v %v", tc.name, given, err)
			}
			continue
		}
		if err != nil || given.Workflow != tc.workflow || !slices.Equal(given.Shown, tc.shown) {
			t.Fatalf("%s: the yes to what was asked: %+v %v", tc.name, given, err)
		}
		for _, other := range [][]string{nil, tc.shown[:1], {tc.shown[1], tc.shown[0]}} {
			if _, err := givenInAdvance(p.Slug, q, other); !errors.Is(err, errInAdvanceQuestionChanged) {
				t.Fatalf("%s: a yes to %q counted: %v", tc.name, other, err)
			}
		}
	}
}

func TestAnAdoptionWithoutAProcedureSaysWhatToInclude(t *testing.T) {
	_, _, err := adoptionProcedure(t.Context(), nil, adoption.Candidate{})
	if err == nil || err.Error() != "this adoption brings no adoption procedure; include the platform pack" {
		t.Fatalf("missing adoption procedure: %v", err)
	}
}
