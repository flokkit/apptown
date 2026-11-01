package wow

// Every published workflow, block and pack version records where it came
// from: the source that published it and who signed what came in. Stock
// comes from the built-in source; what is authored on this AppTown is its
// own source and has no signer. Stock older than the record is "shipped
// with AppTown before <build>, unsigned" (dbschema migration 168).

// The sources this release knows. More are a later release's work.
const (
	// sourceFlokkit is the built-in source: the stock every AppTown has.
	sourceFlokkit = "flokkit"
	// sourceHere is this AppTown: what is authored in its library.
	sourceHere = "here"
)

// Origin is where a version came from.
type Origin struct {
	Source string `json:"source"`
	// Signer is the identity that signed what came in, or how it came
	// unsigned; "" for what is authored here.
	Signer string `json:"signer"`
}

// authoredHere is a version authored on this AppTown.
var authoredHere = Origin{Source: sourceHere}

// shippedOrigin is a stock version's origin: the built-in source, shipped
// with the release named shipped (the AppTown version that built it). In
// this release a shipped update is not signed.
func shippedOrigin(shipped string) Origin {
	if shipped == "" {
		shipped = "a build that did not name itself"
	}
	return Origin{Source: sourceFlokkit, Signer: "shipped with AppTown " + shipped + ", unsigned"}
}

// origin is a pack version's origin for a publication of origin.
func (s *packs) origin(origin string) Origin {
	if origin == "stock" {
		return shippedOrigin(s.shipped)
	}
	return authoredHere
}
