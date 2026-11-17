package dataprotection

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dataprotection/internal/protectionsql"
	"github.com/flokkit/apptown/internal/ids"
)

func TestProtectionBatchPreservesSerializedIdentityAndInvalidatesEveryInput(t *testing.T) {
	for _, input := range []string{"checks owed", "reading basis", "target revision", "backup store"} {
		t.Run(input, func(t *testing.T) {
			w := newOffhostWorld(t, "identity", answeringPort(t))
			ctx := t.Context()
			dep, err := w.s.Deployments.ByName(ctx, w.pid, "main")
			if err != nil {
				t.Fatal(err)
			}
			target, err := w.s.Targets.ByID(ctx, dep.TargetID)
			if err != nil {
				t.Fatal(err)
			}
			if dep.LastApplied == nil {
				t.Fatal("fixture lacks serving-attempt evidence")
			}
			// A historical row uses the serialized contract from before batching.
			// There is no owner operation for importing a historical observation.
			raw, err := json.Marshal(struct {
				ChecksOwedAt   time.Time
				Deployment     any
				TargetRevision int64
				Store          *BackupStore
			}{dep.ChecksOwedAt, dep.ReadingBasis(), target.SettingsRevision, nil})
			if err != nil {
				t.Fatal(err)
			}
			identity := fmt.Sprintf("%x", sha256.Sum256(raw))
			now := w.s.now()
			saved := ProtectionSnapshot{Identity: identity, ObservedAt: now, AttemptedAt: now, OffHost: true, Items: []ProtectionItemObservation{{Key: "data", Need: "postgres", Copy: CopyFresh}}}
			payload, err := json.Marshal(saved)
			if err != nil {
				t.Fatal(err)
			}
			if err := protectionsql.New(w.s.db.SQL()).StoreReading(ctx, protectionsql.StoreReadingParams{ID: ids.New(now), Subject: dep.ID.String(), Kind: "protection", Identity: identity, Payload: payload}); err != nil {
				t.Fatal(err)
			}
			observer := NewProtectionObserver(w.s.Service, nil)
			queries, stop := w.s.db.CountStatements("-- name: BackupStore :one")
			snapshots, err := observer.Snapshots(ctx, []ids.ID{dep.ID})
			stop()
			got := snapshots[dep.ID]
			if err != nil || got.Unknown || got.Stale || got.Identity != identity || !reflect.DeepEqual(got.Items, saved.Items) || !got.ObservedAt.Equal(now) || queries() != 1 {
				t.Fatalf("unchanged reading: %+v %v (%d store reads)", got, err, queries())
			}
			switch input {
			case "checks owed":
				err = w.s.Deployments.OweChecks(ctx, dep.ID)
			case "reading basis":
				_, err = w.s.Deployments.ReserveHostPort(ctx, w.pid, dep.Name, "changed-container", 43001)
			case "target revision":
				target.Ingress = map[string]any{"domain": "changed.example"}
				_, err = w.s.Targets.Put(ctx, target)
			case "backup store":
				w.attach()
			}
			if err != nil {
				t.Fatal(err)
			}
			snapshots, err = observer.Snapshots(ctx, []ids.ID{dep.ID})
			got = snapshots[dep.ID]
			if err != nil || !got.Unknown || !got.Stale || len(got.Items) != 0 || !got.ObservedAt.Equal(now) {
				t.Fatalf("changed %s stayed current: %+v %v", input, got, err)
			}
		})
	}
}
