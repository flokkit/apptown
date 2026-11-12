package integration

import (
	"encoding/json"
	"net/mail"
	"slices"
	"strings"
)

func notifierSpecs() []ConnectorSpec {
	zero, last := 0, 23
	hour := ConnectorField{Key: "digestHour", Label: "Daily digest hour (UTC)", Type: "number", Placeholder: "8", Min: &zero, Max: &last}
	presets := []ConnectorPreset{}
	for _, f := range []struct{ name, title string }{{"json", "JSON"}, {"slack", "Slack"}, {"discord", "Discord"}, {"teams", "Microsoft Teams"}, {"ntfy", "ntfy"}} {
		presets = append(presets, ConnectorPreset{Name: f.name, Title: f.title, Icon: "feedback", Suggest: f.name, Config: map[string]any{"format": f.name, "digestHour": 8}})
	}
	return []ConnectorSpec{
		{Capability: CapNotifier, Provider: "webhook", Title: "Webhook", Icon: "feedback", Multi: true, Description: "Send attention items to a chat channel or webhook.", Presets: presets, Fields: []ConnectorField{
			{Key: "urlFrom", Label: "Webhook address secret", Type: "secretRef"},
			{Key: "format", Label: "Format", Type: "select", Options: "json,slack,discord,teams,ntfy"}, hour}},
		{Capability: CapNotifier, Provider: "smtp", Title: "Email (SMTP)", Icon: "feedback", Multi: true, Description: "Send attention items to the addresses you choose.", Fields: []ConnectorField{
			{Key: "host", Label: "SMTP host", Type: "string"}, {Key: "port", Label: "Port", Type: "number", Placeholder: "587"},
			{Key: "security", Label: "Security", Type: "select", Options: "starttls,tls,none"},
			{Key: "username", Label: "Username", Type: "string"}, {Key: "passwordFrom", Label: "Password secret", Type: "secretRef"},
			{Key: "from", Label: "From address", Type: "string"}, {Key: "to", Label: "To addresses (comma-separated)", Type: "string"}, hour}},
	}
}

// validateNotifierConfig accepts references only; raw addresses and passwords
// cannot enter the binding configuration returned by integration views.
func validateNotifierConfig(provider, raw string) error {
	spec := FindConnector(CapNotifier, provider)
	if spec == nil {
		return ErrInvalidConfig
	}
	var cfg map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &cfg) != nil {
		return ErrInvalidConfig
	}
	fields := map[string]ConnectorField{}
	for _, f := range spec.Fields {
		fields[f.Key] = f
	}
	for key, value := range cfg {
		f, ok := fields[key]
		if !ok {
			return ErrInvalidConfig
		}
		switch f.Type {
		case "secretRef":
			// The save path writes every secretRef field, so an unused one
			// (no SMTP login) arrives as null or []; a used one names one secret.
			var refs []string
			if json.Unmarshal(value, &refs) != nil || len(refs) > 1 || len(refs) == 1 && strings.TrimSpace(refs[0]) == "" {
				return ErrInvalidConfig
			}
		case "number":
			var n int
			if json.Unmarshal(value, &n) != nil {
				return ErrInvalidConfig
			}
			if f.Min != nil && n < *f.Min || f.Max != nil && n > *f.Max {
				return ErrInvalidConfig
			}
			if key == "port" && (n < 1 || n > 65535) {
				return ErrInvalidConfig
			}
		default:
			var s string
			if json.Unmarshal(value, &s) != nil {
				return ErrInvalidConfig
			}
			if f.Type == "select" && !slices.Contains(strings.Split(f.Options, ","), s) {
				return ErrInvalidConfig
			}
		}
	}
	if provider == "webhook" && !namesSecret(cfg["urlFrom"]) {
		return ErrInvalidConfig
	}
	if provider == "smtp" {
		for _, key := range []string{"host", "from", "to"} {
			var s string
			if json.Unmarshal(cfg[key], &s) != nil || strings.TrimSpace(s) == "" {
				return ErrInvalidConfig
			}
		}
		// A login needs its password; without a login none is read.
		var username string
		if raw, ok := cfg["username"]; ok && json.Unmarshal(raw, &username) != nil {
			return ErrInvalidConfig
		}
		if strings.TrimSpace(username) != "" && !namesSecret(cfg["passwordFrom"]) {
			return ErrInvalidConfig
		}
		var from, to string
		if err := json.Unmarshal(cfg["from"], &from); err != nil {
			return ErrInvalidConfig
		}
		if err := json.Unmarshal(cfg["to"], &to); err != nil {
			return ErrInvalidConfig
		}
		if _, err := mail.ParseAddress(from); err != nil {
			return ErrInvalidConfig
		}
		if _, err := mail.ParseAddressList(to); err != nil {
			return ErrInvalidConfig
		}
	}
	return nil
}

// namesSecret reports whether a validated secretRef value names one secret.
func namesSecret(raw json.RawMessage) bool {
	var refs []string
	return raw != nil && json.Unmarshal(raw, &refs) == nil && len(refs) == 1
}
