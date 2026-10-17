package deploy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flokkit.io/forge/internal/dockerlabel"
	"strconv"
)

// DockerIdentity names what a Docker deployment's container was admitted
// for: the deployment, the generation and the artifact hash. The label keys
// are external resource identifiers; existing containers retain them.
type DockerIdentity struct {
	Deployment HostLabel `json:"deployment"`
	Generation int64     `json:"generation"`
	Artifact   string    `json:"artifact"`
}

// HostLabel is the name a deployment carries on its host (the
// forge.environment label, boot proofs): the old integer for a deployment
// made before the identity cutover, which its containers keep, and its UUID
// for one made after. It reads a proof written with either a number or a
// string.
type HostLabel string

func (l *HostLabel) UnmarshalJSON(b []byte) error {
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		*l = HostLabel(n.String())
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*l = HostLabel(s)
	return nil
}

func (id DockerIdentity) Labels() map[string]string {
	return map[string]string{dockerlabel.Deployment: string(id.Deployment), dockerlabel.Generation: strconv.FormatInt(id.Generation, 10), dockerlabel.Artifact: id.Artifact}
}

func (id DockerIdentity) Checkpoint(name string) map[string]any {
	return map[string]any{"container": name, "docker": id}
}

// ConfigHash hashes a JSON document in its one canonical spelling, so a hash
// made over Go's sorted, compact form matches jsonb's spacing and key order.
// Anything that is not JSON hashes as is.
func ConfigHash(configJSON string) string {
	var v any
	if json.Unmarshal([]byte(configJSON), &v) == nil {
		if b, err := json.Marshal(v); err == nil {
			configJSON = string(b)
		}
	}
	sum := sha256.Sum256([]byte(configJSON))
	return hex.EncodeToString(sum[:])[:16]
}
