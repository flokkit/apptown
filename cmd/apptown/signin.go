package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"github.com/flokkit/apptown/internal/apptown/identity"
)

// Sign-in administration shares the browser's owner operations. Configuration
// carries a secret reference only; secret bytes use the existing secret command.
func cmdSignIn(args []string) error {
	if len(args) == 0 {
		return usagef("usage: apptown signin <show|configure|test|enable|disable|bindings|bind|revoke> [options]")
	}
	verb := args[0]
	fs := newFlags("signin " + verb)
	file := fs.String("file", "", "provider configuration JSON (secret name only)")
	revision := fs.Int64("revision", 0, "expected provider revision")
	provider := fs.String("provider", "", "immutable provider UUID")
	person := fs.String("principal", "", "local principal UUID")
	subject := fs.String("subject", "", "exact provider subject")
	args = parseArgs(fs, args[1:])
	var out json.RawMessage
	switch verb {
	case "show", "bindings":
		if len(args) != 0 {
			return usagef("signin %s takes no positional arguments", verb)
		}
		path := "/auth/external/provider"
		if verb == "bindings" {
			path = "/auth/external/bindings?all=true"
		}
		if e := api.get(path, &out); e != nil {
			return e
		}
	case "test":
		if len(args) != 0 {
			return usagef("signin test takes no arguments")
		}
		if e := api.post("/auth/external/test", map[string]any{}, &out); e != nil {
			return e
		}
	case "configure":
		if *file == "" || len(args) != 0 {
			return usagef("usage: apptown signin configure --file provider.json")
		}
		raw, e := os.ReadFile(*file)
		if e != nil {
			return e
		}
		var in identity.ExternalProviderChange
		if e := json.Unmarshal(raw, &in); e != nil {
			return fmt.Errorf("invalid provider configuration JSON")
		}
		raw, e = api.request("PUT", "/auth/external/provider", in)
		if e != nil {
			return e
		}
		out = raw
	case "enable", "disable":
		if *revision <= 0 || len(args) != 0 {
			return usagef("usage: apptown signin %s --revision N", verb)
		}
		if e := api.post("/auth/external/enabled", map[string]any{"revision": *revision, "enabled": verb == "enable"}, &out); e != nil {
			return e
		}
	case "bind":
		if *provider == "" || *person == "" || *subject == "" || len(args) != 0 {
			return usagef("usage: apptown signin bind --provider UUID --principal UUID --subject SUBJECT")
		}
		if _, e := api.request("POST", "/auth/external/bindings", map[string]string{"providerId": *provider, "principalId": *person, "subject": *subject}); e != nil {
			return e
		}
		out = json.RawMessage(`{"bound":true}`)
	case "revoke":
		if len(args) != 1 {
			return usagef("usage: apptown signin revoke <binding UUID>")
		}
		if _, e := api.request("DELETE", "/auth/external/bindings/"+url.PathEscape(args[0]), nil); e != nil {
			return e
		}
		out = json.RawMessage(`{"revoked":true}`)
	default:
		return usagef("unknown signin command %q", verb)
	}
	return printJSON(out)
}
