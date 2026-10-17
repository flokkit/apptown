package conformance_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/forge/conformance"
)

func TestDeclarationDefaultsStayConsistentAcrossValidationAndUse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name             string
		declaration      conformance.CheckDeclaration
		evaluator, scope string
	}{
		{"shell", conformance.CheckDeclaration{Sh: "true"}, "sh", conformance.ScopeTree},
		{"rubric", conformance.CheckDeclaration{Rubric: "check changes"}, "llm", conformance.ScopeDiff},
		{"scanner", conformance.CheckDeclaration{Tool: "semgrep"}, "tool", conformance.ScopeTree},
		{"probe", conformance.CheckDeclaration{Probe: "readiness"}, "runtime", conformance.ScopeTree},
		{"publication precedence", conformance.CheckDeclaration{Sh: "true", Rubric: "context", Tool: "semgrep", Probe: "readiness"}, "sh", conformance.ScopeTree},
		{"explicit selection", conformance.CheckDeclaration{Evaluator: "tool", Sh: "true", Tool: "semgrep"}, "tool", conformance.ScopeTree},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.declaration
			c.Name = "check"
			if got := check.Evaluator(c); got != tc.evaluator {
				t.Fatalf("before validation: %s", got)
			}
			before := conformance.DefinitionOf("block", 3, c)
			checks := []conformance.CheckDeclaration{c}
			if err := check.Validate(checks); err != nil {
				t.Fatal(err)
			}
			if checks[0].Evaluator != tc.evaluator || checks[0].Scope != tc.scope {
				t.Fatalf("canonical declaration: %+v", checks[0])
			}
			after := conformance.DefinitionOf("block", 3, checks[0])
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("validation changed the described meaning: before=%+v after=%+v", before, after)
			}
			for _, form := range []conformance.CheckDeclaration{c, checks[0]} {
				script, shell := check.PostureScript(form)
				if shell != (tc.evaluator == "sh") || (shell && script != "true") {
					t.Fatalf("shell admission: %q %v", script, shell)
				}
				if check.IsRuntime(form) != (tc.evaluator == "runtime") {
					t.Fatalf("runtime admission: %+v", form)
				}
				if check.Scope(form) != tc.scope {
					t.Fatalf("scope: %+v", form)
				}
			}
		})
	}
}

func TestCheckDefinitionPreservesPublicVocabulary(t *testing.T) {
	t.Parallel()
	definition := conformance.DefinitionOf("block", 2, conformance.CheckDeclaration{Name: "check", Sh: "true"})
	body, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"judgedWhere":"at landing and in every review"`, `"evaluator":"sh"`, `"scope":"tree"`, `"severity":"note"`, `"key":"block@2·check"`} {
		if !strings.Contains(string(body), field) {
			t.Fatalf("missing %s in %s", field, body)
		}
	}
}
