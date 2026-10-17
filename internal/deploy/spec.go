package deploy

import "flokkit.io/forge/internal/deploy/appcontract"

// ServiceSpec is a container run beside an app: a sidecar for a step, a
// need's emulator, a service a contract declares: started on the step's
// network before the step runs, reachable by Name, torn down after.
type ServiceSpec struct {
	Name  string            `json:"name"`
	Image string            `json:"image"`
	Env   map[string]string `json:"env"`
	// Command overrides the image's default (a dependency emulator like MinIO
	// needs `server /data`). Sidecars from a contract leave it empty.
	Command []string `json:"command,omitempty"`
	// Entrypoint replaces the image's entrypoint, when set: a need's start
	// script (the s3 emulator's makes its bucket and account first). Docker
	// takes one executable for it; the rest go before Command (RunArgs).
	Entrypoint []string `json:"entrypoint,omitempty"`
	// ConfigAtStart says the service keeps none of what it is started with
	// in its data: everything it is configured with is set again at every
	// start. A standing one whose image, entrypoint, command or environment
	// changed is remade on its own volume, whatever its data class, as a
	// lifted size limit is.
	ConfigAtStart bool `json:"configAtStart,omitempty"`
	// KeepsImage says the service's data is in its image's own format: a
	// standing one whose image moved is not remade under ConfigAtStart but
	// refused unless its data is synthetic, as any data service is. A
	// Postgres of another major version cannot read the old files.
	KeepsImage bool `json:"keepsImage,omitempty"`
	// Health is a shell healthcheck command (exit 0 = healthy). When set, the
	// runtime applies it as docker --health-cmd and can gate startup on it.
	Health string `json:"health,omitempty"`
	// Public/host-port handling is caller-driven; these mark a non-public app
	// part started on the stack (Port for its PORT env, not a host mapping).
	Port int `json:"port,omitempty"`
	// Resources is the most a non-public app part or a need's service may
	// use; nothing declared is no limit.
	Resources appcontract.Resources `json:"resources,omitzero"`
	// Data is where a need's emulator keeps its data (its catalogue kind's
	// local data path), empty for a sidecar or an app part. A throwaway
	// start mounts a disposable volume there: an emulator whose image does
	// not make that folder itself (S3's) exits at once without it.
	Data string `json:"data,omitempty"`
	// Credentials are the values the forge generated for this service (a
	// deployment's or a preview's need credentials), in its Env and in what
	// the app is given: a boot redacts them from every failure it returns
	// (docker.Host.Boot). Carried with a prepared boot to where it runs.
	Credentials []string `json:"credentials,omitempty"`
}

// EntrypointFlag is the docker run flag that sets the service's entrypoint,
// or nothing when it keeps its image's.
func (s ServiceSpec) EntrypointFlag() []string {
	if len(s.Entrypoint) == 0 {
		return nil
	}
	return []string{"--entrypoint", s.Entrypoint[0]}
}

// RunArgs are what follow the image on docker run: the rest of the
// entrypoint, then the command.
func (s ServiceSpec) RunArgs() []string {
	var out []string
	if len(s.Entrypoint) > 1 {
		out = append(out, s.Entrypoint[1:]...)
	}
	return append(out, s.Command...)
}
