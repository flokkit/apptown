package fly

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// Machine metadata AppTown gives every Machine it makes. The installation's
// id is the mark (design/subsystems/deploy-targets.md, "What a target
// holds"): an app is this installation's when any of its Machines carries
// it. The deployment and generation say which deployment and release a
// Machine belongs to, so recovery, observation and take-down never go by a
// name.
const (
	metaInstance   = "apptown_instance"
	metaDeployment = "apptown_deployment"
	metaGeneration = "apptown_generation"
	// metaRole is part (an app part) or init (an init step's run).
	metaRole = "apptown_role"
	// metaGroup is Fly's process group: other Machines of the app reach a
	// part as <part>.process.<app>.internal.
	metaGroup = "fly_process_group"
)

// installationPart is the installation's part of every name AppTown gives
// on Fly: the first 8 hex digits of its id's SHA-256. Fly app names are
// global across every Fly customer, so two installations sharing an org,
// or two orgs, never reach for the same name.
func installationPart(installation string) string {
	sum := sha256.Sum256([]byte(installation))
	return hex.EncodeToString(sum[:])[:8]
}

// appName is a deployment's Fly app: at-<installation>-<key>, where key is
// deploy.HostKey (unique within an installation). 24 characters. A name is
// never evidence of ownership; the recorded app id is.
func appName(installation, key string) string {
	return "at-" + installationPart(installation) + "-" + key
}

// appURL is where Fly's proxy answers an app.
func appURL(app string) string { return "https://" + app + "." + appDomain }

func machineName(part string, generation int64) string {
	return part + "-g" + strconv.FormatInt(generation, 10)
}
