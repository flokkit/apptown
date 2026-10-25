package instancebackup

import (
	"fmt"
	"regexp"
)

// supportedRestoreMajor is the built-in restore engine's compatibility policy,
// separate from the versions observed when an archive is made.
const supportedRestoreMajor = 16

// DatabaseProvenance records observed engine and dump-client versions. It is
// absent in legacy archives; it does not certify cross-major compatibility.
type DatabaseProvenance struct {
	Engine            string `json:"engine"`
	ServerVersionNum  int    `json:"serverVersionNum"`
	DumpClientVersion string `json:"dumpClientVersion"`
}

var dumpClientVersion = regexp.MustCompile(`^pg_dump \(PostgreSQL\) [0-9]{1,2}\.[0-9]{1,3}(\.[0-9]{1,3})?([ -][ -~]+)?$`)

func (p DatabaseProvenance) validate() error {
	if p.Engine != "postgres" || p.ServerVersionNum < 100000 || p.ServerVersionNum >= 1000000 || len(p.DumpClientVersion) > 200 || !dumpClientVersion.MatchString(p.DumpClientVersion) {
		return fmt.Errorf("the archive's database provenance is incomplete or malformed")
	}
	return nil
}
