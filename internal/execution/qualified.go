package execution

import (
	_ "embed"
	"encoding/json"
	"strings"
)

// Qualified worker profiles (design/operations/workers.md, "Private Docker
// on the worker"). A profile is qualified by running
// the qualification suite on a separate Linux worker and recording what was
// measured and what passed; the facts it ran on are listed here as data,
// each with its record. A worker whose measured facts match one is
// qualified by that record, and by nothing else: a profile that matches
// none, or whose facts could not be measured, is unqualified.

//go:embed qualified_profiles.json
var qualifiedProfilesJSON []byte

// QualifiedProfile is the facts one qualification ran on, and its record:
// what a worker's profile must match to rest on it. The kernel is not among
// them (it changes with the host's updates, and a request is still refused
// on a kernel other than the one it was admitted on); the Docker version is
// matched by its major version; the private daemon by its image's
// repository digest and the version its dockerd reports.
type QualifiedProfile struct {
	Record        string `json:"record"`
	Name          string `json:"name"`
	OS            string `json:"os"`
	Architecture  string `json:"architecture"`
	Cgroup        string `json:"cgroup"`
	StorageDriver string `json:"storageDriver"`
	DockerMajor   string `json:"dockerMajor"`
	DindDigest    string `json:"dindDigest"`
	DindVersion   string `json:"dindVersion"`
}

// QualifiedProfiles are the qualified fact sets, from
// qualified_profiles.json. One that cannot be read is a build error, found
// by the package's tests.
func QualifiedProfiles() ([]QualifiedProfile, error) {
	var out []QualifiedProfile
	err := json.Unmarshal(qualifiedProfilesJSON, &out)
	return out, err
}

// Qualification is the record of the qualified profile p matches, or ""
// when it matches none (or the list cannot be read). Beside the measured
// facts, p must carry what qualification assumed: the operator's exclusive
// declaration, and a privileged container that ran.
func (p WorkerProfile) Qualification() string {
	qualified, err := QualifiedProfiles()
	if err != nil {
		return ""
	}
	return p.qualificationIn(qualified)
}

func (p WorkerProfile) qualificationIn(qualified []QualifiedProfile) string {
	if !p.Exclusive || p.Privileged != "yes" {
		return ""
	}
	major, _, _ := strings.Cut(p.Docker, ".")
	for _, q := range qualified {
		if q.Record != "" && q.Name == p.Name && q.OS == p.OS && q.Architecture == p.Architecture && q.Cgroup == p.Cgroup &&
			q.StorageDriver == p.StorageDriver && q.DockerMajor == major && q.DindDigest == p.DindDigest && q.DindVersion == p.DindVersion {
			return q.Record
		}
	}
	return ""
}
