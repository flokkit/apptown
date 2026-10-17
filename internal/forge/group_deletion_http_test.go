package forge

import (
	"context"
	"testing"

	"flokkit.io/forge/internal/forge/identity"
)

func TestGroupDeletionHTTPProtectsAuthorityAndSystemGroups(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	if w := authzCall(h, "boss", "POST", "/api/v1/groups", `{"name":"temporary","members":["owner"]}`); w.Code != 200 {
		t.Fatalf("create group: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "outsider", "DELETE", "/api/v1/groups/temporary", ""); w.Code != 403 {
		t.Fatalf("unauthorized delete: %d %s", w.Code, w.Body.String())
	}
	if kind, err := a.Access.GroupKind(context.Background(), "temporary"); err != nil || kind == "" {
		t.Fatalf("denial deleted group: %q %v", kind, err)
	}
	if w := authzCall(h, "boss", "DELETE", "/api/v1/groups/"+identity.GroupAllUsers, ""); w.Code != 409 {
		t.Fatalf("system group: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", "DELETE", "/api/v1/groups/temporary", ""); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", "DELETE", "/api/v1/groups/temporary", ""); w.Code != 404 {
		t.Fatalf("missing group: %d %s", w.Code, w.Body.String())
	}
	if kind, _ := a.Access.GroupKind(context.Background(), "temporary"); kind != "" {
		t.Fatal("successful deletion left group")
	}
}
