package wow

// Every published workflow, block and pack version records where it came
// from (came_from): the stock a release shipped, named by the update it
// came in, or authored on this AppTown. Stock older than the record is
// "AppTown before <build>" (dbschema migration 168).

// CameFromHere is a version authored on this AppTown.
const CameFromHere = "authored here"

// stockCameFrom is a stock version's came_from: "AppTown <the shipped
// update's name>", which is the version of the build that made it.
func stockCameFrom(shipped string) string {
	if shipped == "" {
		return "AppTown (a build that did not name itself)"
	}
	return "AppTown " + shipped
}

// cameFrom is a pack version's came_from for a publication of origin.
func (s *Packs) cameFrom(origin string) string {
	if origin == "stock" {
		return stockCameFrom(s.shipped)
	}
	return CameFromHere
}
