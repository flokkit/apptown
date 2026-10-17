package identity

import (
	"context"
	"testing"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestRemoveProjectInKeepsOtherProjects(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	service := New(db, Config{})
	tokens := map[ids.ID]string{}
	for _, pid := range []ids.ID{idstest.N(1), idstest.N(2)} {
		tok, err := service.RuntimePullToken(context.Background(), pid)
		if err != nil {
			t.Fatal(err)
		}
		tokens[pid] = tok
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error { return RemoveProjectIn(context.Background(), tx, idstest.N(1)) }); err != nil {
		t.Fatal(err)
	}
	if _, found, err := service.ProjectOfPullToken(context.Background(), tokens[idstest.N(1)]); err != nil || found {
		t.Fatalf("removed project's token survived: %v %v", found, err)
	}
	if pid, found, err := service.ProjectOfPullToken(context.Background(), tokens[idstest.N(2)]); err != nil || !found || pid != idstest.N(2) {
		t.Fatalf("other project's token: %d %v %v", pid, found, err)
	}
}
