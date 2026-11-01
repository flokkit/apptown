package deployment

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/stock"
)

// A release asking for a need the catalogue no longer holds (dynamodb,
// withdrawn) is refused at admission on every target, before it is
// accepted, in the catalogue's sentence.
func TestReleaseAdmissionRefusesAWithdrawnKind(t *testing.T) {
	s, in, _, sources := requestFixture(t)
	kinds := dependency.NewCatalog(s.db, nil)
	if _, _, err := kinds.SyncStock(t.Context(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	s.env.needs = kinds
	sources.contract = &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true}}, Needs: []string{"dynamodb"}}
	for _, kind := range []string{KindDockerHost, KindPortainer} {
		why, err := s.env.RefusesContract(t.Context(), &Target{Name: "host", Kind: kind}, sources.contract)
		if err != nil || why != "this app needs dynamodb, which is not in the dependency catalog" {
			t.Errorf("%s contract admission accepted withdrawn dynamodb", kind)
		}
	}
	if got, err := s.RequestRelease(t.Context(), in); err == nil {
		t.Fatalf("withdrawn kind admitted as release %+v", got)
	}
}
