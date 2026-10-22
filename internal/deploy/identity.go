package deploy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/ids"
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
// apptown.deployment label, boot proofs): the old integer for a deployment
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

// HostKey is a deployment's key on a host or a provider: the last 12 hex
// digits of its id, the random tail of a UUIDv7. A unique index on
// deployments makes it unique within an AppTown (migration 162), so every
// name made from it is too.
func HostKey(id ids.ID) string { return hex.EncodeToString(id[10:]) }

// IdentityLabels are a container's identity labels (deployment,
// generation, artifact) by their current keys. A container made before 1.0
// carries them under forge.* (dockerlabel.Before1); one that carries
// neither key reads "" for it.
//
// REMOVE-AFTER resource move: once every deployment has been moved, its
// containers carry only the current keys and this is labels itself.
func IdentityLabels(labels map[string]string) map[string]string {
	out := map[string]string{}
	for _, k := range []string{dockerlabel.Deployment, dockerlabel.Generation, dockerlabel.Artifact} {
		v, ok := labels[k]
		if !ok {
			v = labels[dockerlabel.Before1(k)]
		}
		out[k] = v
	}
	return out
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
