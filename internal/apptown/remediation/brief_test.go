package remediation

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestRemediationBriefCarriesTheRubric(t *testing.T) {
	t.Parallel()
	sc := conformance.DeclaredCheck{Block: "lang/selection/django-fullstack", Version: 8, Check: conformance.CheckDeclaration{AppliesTo: []string{"repository"},
		Name: "admin-exposed-deliberately", Expects: "shipping the admin is a decision",
		Rubric: "(1) collectstatic with whitenoise AND (2) a provisioned superuser AND (3) the password is supplied"}}
	b := Brief(sc.Block, sc.Version, sc, "Admin is routed but lacks whitenoise. [settings.py:27]")
	if !strings.Contains(b, `marked "preexisting" by apptown`) {
		t.Fatal("remediation steering must name the current apptown check command")
	}
	for _, want := range []string{"HOW IT IS JUDGED", "(3) the password is supplied", "Fix the WHOLE of what the check asks", "lacks whitenoise"} {
		if !strings.Contains(b, want) {
			t.Fatalf("brief lacks %q:\n%s", want, b)
		}
	}
	sh := conformance.DeclaredCheck{Block: "b", Version: 1, Check: conformance.CheckDeclaration{AppliesTo: []string{"repository"}, Name: "x", Expects: "y", Sh: "grep -q collectstatic Dockerfile"}}
	if b := Brief("b", 1, sh, "-"); !strings.Contains(b, "the check's own script") || !strings.Contains(b, "grep -q collectstatic") {
		t.Fatalf("a shell check's script is the judging text:\n%s", b)
	}
}

func TestGeneratedRemediationTitlesAreBoundedAndKeepAcceptanceIdentity(t *testing.T) {
	t.Parallel()
	for _, declared := range []bool{true, false} {
		sc := conformance.DeclaredCheck{Block: strings.Repeat("pack", 80), Version: 2, Check: conformance.CheckDeclaration{Name: strings.Repeat("check", 80)}}
		if declared {
			sc.Check.Title = strings.Repeat("界", 150)
		}
		title := ChangeTitle(sc)
		if title == "" || !utf8.ValidString(title) || utf8.RuneCountInString(title) > 100 {
			t.Fatalf("generated title: %q", title)
		}
		f, starts, item, def := attemptFixture(t, false)
		def.Title = title
		def.Acceptance = &workflow.Acceptance{Checks: []workflow.AcceptanceCheck{AcceptanceCheck(sc)}}
		run, err := starts.Start(t.Context(), item, def, "worker")
		if err != nil || run == nil {
			t.Fatalf("generated remediation admission: %+v %v", run, err)
		}
		changes := f.changes(t)
		if len(changes) != 1 || changes[0].Title != title {
			t.Fatalf("admitted titles: %+v", changes)
		}
		acc := AcceptanceCheck(sc)
		if acc.Block != sc.Block || acc.Check != sc.Check.Name || acc.Title != sc.Check.Title {
			t.Fatalf("acceptance identity: %+v", acc)
		}
	}
}
