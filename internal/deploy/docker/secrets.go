package docker

// SecretReceipt is what a boot records about one delivered secret: names,
// stores, versions, never values.
type SecretReceipt struct {
	Name      string   `json:"name"`
	Store     string   `json:"store"`
	Version   string   `json:"version"`
	Consumers []string `json:"consumers"`
	Generated bool     `json:"generated,omitempty"`
}
