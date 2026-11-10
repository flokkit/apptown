package fly

// The Machines API's words, as much of them as AppTown reads and writes
// (https://docs.fly.io/api/machines/openapi.json).

type flyApp struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	MachineCount int    `json:"machine_count"`
	Organization struct {
		Slug string `json:"slug"`
	} `json:"organization"`
}

type orgMachine struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	AppName string `json:"app_name"`
	State   string `json:"state"`
	Region  string `json:"region"`
	Config  struct {
		Metadata map[string]string `json:"metadata"`
	} `json:"config"`
}

// machine is a Machine as Fly answers it. Of its config AppTown reads only
// the metadata: Fly answers some fields in another form than it takes them
// (a service's autostop is a string in a request and a boolean in an
// answer), so the request type is never used to read one.
type machine struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	State      string         `json:"state"`
	Region     string         `json:"region"`
	HostStatus string         `json:"host_status"`
	Version    string         `json:"version"`
	Config     answeredConfig `json:"config"`
	Checks     []checkStatus  `json:"checks"`
	Events     []event        `json:"events"`
}

type answeredConfig struct {
	Metadata map[string]string `json:"metadata"`
}

type checkStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Output string `json:"output"`
}

type event struct {
	Type      string         `json:"type"`
	Status    string         `json:"status"`
	Source    string         `json:"source"`
	Timestamp int64          `json:"timestamp"`
	Request   map[string]any `json:"request"`
}

// exit is a Machine's exit as an event reports it.
type exit struct {
	Code   int  `json:"exit_code"`
	OOM    bool `json:"oom_killed"`
	Signal int  `json:"signal"`
}

type machineConfig struct {
	Image    string                  `json:"image"`
	Guest    guest                   `json:"guest"`
	Env      map[string]string       `json:"env,omitempty"`
	Init     *machineInit            `json:"init,omitempty"`
	Services []service               `json:"services,omitempty"`
	Checks   map[string]machineCheck `json:"checks,omitempty"`
	Restart  restart                 `json:"restart"`
	Metadata map[string]string       `json:"metadata,omitempty"`
	DNS      *dnsConfig              `json:"dns,omitempty"`
	// AutoDestroy is never set: a one-off Machine is destroyed by AppTown
	// after its exit has been read.
	AutoDestroy bool `json:"auto_destroy,omitempty"`
}

type guest struct {
	CPUKind  string `json:"cpu_kind"`
	CPUs     int    `json:"cpus"`
	MemoryMB int    `json:"memory_mb"`
}

type machineInit struct {
	Cmd []string `json:"cmd,omitempty"`
}

type restart struct {
	Policy     string `json:"policy"`
	MaxRetries int    `json:"max_retries,omitempty"`
}

type dnsConfig struct {
	Searches []string `json:"searches,omitempty"`
}

type service struct {
	Protocol           string         `json:"protocol"`
	InternalPort       int            `json:"internal_port"`
	Ports              []port         `json:"ports"`
	Autostart          bool           `json:"autostart"`
	Autostop           string         `json:"autostop"`
	MinMachinesRunning int            `json:"min_machines_running"`
	Checks             []serviceCheck `json:"checks,omitempty"`
}

type port struct {
	Port       int      `json:"port"`
	Handlers   []string `json:"handlers"`
	ForceHTTPS bool     `json:"force_https,omitempty"`
}

type serviceCheck struct {
	Type        string `json:"type"`
	Port        int    `json:"port,omitempty"`
	Path        string `json:"path,omitempty"`
	Method      string `json:"method,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	Interval    string `json:"interval"`
	Timeout     string `json:"timeout"`
	GracePeriod string `json:"grace_period"`
}

type machineCheck struct {
	serviceCheck
	Kind string `json:"kind,omitempty"`
}

type createMachine struct {
	Name              string        `json:"name"`
	Region            string        `json:"region"`
	Config            machineConfig `json:"config"`
	MinSecretsVersion int64         `json:"min_secrets_version,omitempty"`
}

type ipAssignment struct {
	IP     string `json:"ip"`
	Shared bool   `json:"shared"`
	Type   string `json:"type,omitempty"`
}
