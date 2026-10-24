package integration

import (
	"testing"
)

func TestErrorsConnectorKeepsBindingOrder(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	service := NewService(db, Dependencies{})
	bindings := New(db)
	const raw = `{"url":"https://errors.invalid/","tokenFrom":["ERRORS_TOKEN"],"unknown":9007199254740993}`
	for _, name := range []string{"z-first", "a-second"} {
		if _, err := bindings.Upsert(t.Context(), name, CapErrors, "test", raw, false); err != nil {
			t.Fatal(err)
		}
	}
	if names, err := service.Bindings.Names(t.Context(), CapErrors); err != nil || len(names) != 2 || names[0] != "a-second" || names[1] != "z-first" {
		t.Fatalf("connector choices must be alphabetical: %v", names)
	}
	if got, err := service.errorsBindingNamed(t.Context(), ""); err != nil || got == nil || got.Name != "z-first" || got.URL != "https://errors.invalid" || len(got.TokenFrom) != 1 {
		t.Fatalf("errors configuration: %+v %v", got, err)
	}
}

// The errors connector is selected once: a first connector whose
// configuration cannot be used is an error, and the next one is never
// chosen in its place, with its own endpoint and credential.
//
// Kills: a failed listing read as no connector; an undecodable first
// connector skipped for the second.
func TestAnUnusableErrorsConnectorIsNotReplacedByTheNext(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	service := NewService(db, Dependencies{})
	if got, err := service.errorsBindingNamed(t.Context(), ""); err != nil || got != nil {
		t.Fatalf("no connector: %+v %v", got, err)
	}
	// A listing that fails is an error, not "no connector".
	restore := db.FailNextStatement("FROM integrations ORDER BY capability, name")
	got, err := service.errorsBindingNamed(t.Context(), "")
	restore()
	if err == nil || got != nil {
		t.Fatalf("a failed listing: %+v %v", got, err)
	}
	if _, err := service.Bindings.Upsert(t.Context(), "first", CapErrors, "test", `{"url":7}`, false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Bindings.Upsert(t.Context(), "second", CapErrors, "test", `{"url":"https://other.invalid","tokenFrom":["OTHER_TOKEN"]}`, false); err != nil {
		t.Fatal(err)
	}
	if got, err := service.errorsBindingNamed(t.Context(), ""); err == nil || got != nil {
		t.Fatalf("an unusable first connector: %+v %v", got, err)
	}
}
