package deployment

// dockerSourceIdentity is the part of an admitted Docker source this package
// checks: the request version and the revision it names. The rest of the
// selection belongs to the driver that stages it.
type dockerSourceIdentity struct {
	Version  int    `json:"version"`
	Revision string `json:"revision"`
}

// RecoveryJob is the background operation that observes an uncertain attempt.
const RecoveryJob = "deployment-recovery"
