package remediation

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
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
