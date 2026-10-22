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
	if got := service.errorsBindingNamed(t.Context(), ""); got == nil || got.Name != "z-first" || got.URL != "https://errors.invalid" || len(got.TokenFrom) != 1 {
		t.Fatalf("errors configuration: %+v", got)
	}
}
