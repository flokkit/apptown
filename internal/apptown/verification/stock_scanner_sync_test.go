package verification

import (
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/stock"
)

// A stock scanner's binding is its manifest as declared: the scan step reads
// exactly what stock/scanners.yaml says, every field. The sync once copied a
// hand-made list that left out historyCommand, so no landing on a real
// AppTown ever scanned the commits it published, only their final tree, and
// a secret committed then deleted landed in main's history.
func TestStockScannerBindingsCarryEveryDeclaredField(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	SyncStockScanners(t.Context(), integration.NewService(db, integration.Dependencies{}), stock.FS)
	scanners := NewScanners(ScannerDependencies{DB: db})
	manifests := loadScannerManifests(stock.FS)
	if len(manifests) == 0 {
		t.Fatal("no stock scanners")
	}
	history := false
	for _, want := range manifests {
		_, got, _, _, err := scanners.binding(t.Context(), want.Name)
		if err != nil {
			t.Fatalf("%s: %v", want.Name, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the binding the scan reads differs from its manifest:\n got %+v\nwant %+v", want.Name, got, want)
		}
		history = history || (want.Kind == "secret" && len(got.HistoryCommand) > 0)
	}
	if !history {
		t.Fatal("the stock secret scanner's binding has no history command: landings would scan only their final tree")
	}
}
