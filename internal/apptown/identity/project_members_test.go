package identity

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A membership grant is required, but its informational event must not turn a
// successful grant into a reported failure. Revocation has a separate atomicity
// guarantee, covered by the existing removal tests.
func TestProjectMemberWriteFailureSemantics(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, marker string
		wantErr      bool
		grants       int
	}{
		{"grant required", "INSERT INTO grants", true, 0},
		{"event best effort", "INSERT INTO audit_log", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := testDB(t)
			s := New(db, Config{})
			if _, err := s.EnsureUser(context.Background(), "new-member"); err != nil {
				t.Fatal(err)
			}
			restore := db.FailNextStatement(tc.marker)
			defer restore()
			err := s.AddProjectMember(context.Background(), idstest.N(42), "new-member", access.RoleProjectViewer, "owner")
			if tc.wantErr != (err != nil) {
				t.Fatalf("error: %v", err)
			}
			// Every grant row, revoked or not: a failed grant must leave no
			// history behind, which a live-only read would not see.
			count := 0
			for _, g := range recordedGrants(t, db, "new-member", "") {
				if g.ScopeType == string(access.ScopeProject) && g.ScopeID == idstest.N(42).String() {
					count++
				}
			}
			if count != tc.grants {
				t.Fatalf("grant count %d, want %d", count, tc.grants)
			}
			if receipts := ledger(t, db, "project.member-added"); len(receipts) != 0 {
				t.Fatalf("failed event persisted: %+v", receipts)
			}
		})
	}
}
