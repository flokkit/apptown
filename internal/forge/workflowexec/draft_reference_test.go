package workflowexec

import (
	"regexp"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/schema"
)

// Every change interpolation the drafter teaches is accepted by the parser
// that validates the draft: a model that follows the reference never writes
// a workflow the save refuses.
//
// Kills: teaching ${{workItem.*}} after the save started refusing it, so a
// drafting model spent its correction attempt on the reference's own example.
func TestTheDraftersInterpolationsPassTheSave(t *testing.T) {
	t.Parallel()
	examples := regexp.MustCompile(`\$\{\{\s*[a-zA-Z]+\.[a-z]+\s*\}\}`).FindAllString(assistReference, -1)
	if len(examples) == 0 {
		t.Fatal("the reference teaches no interpolation")
	}
	for _, e := range examples {
		doc := strings.Join([]string{
			"apiVersion: apptown.dev/v1",
			"kind: Workflow",
			"metadata: {name: drafted}",
			"spec:",
			"  kind: lifecycle",
			"  stages:",
			"    - name: work",
			"      steps:",
			"        - name: do",
			"          uses: agent/pi",
			"          with: {prompt: \"" + e + "\"}",
		}, "\n")
		if _, err := schema.ParseWorkflow([]byte(doc)); err != nil {
			t.Errorf("%s: %v", e, err)
		}
	}
}
