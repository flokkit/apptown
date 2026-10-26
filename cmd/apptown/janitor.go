package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
)

// Janitor commands manage consent only. Preview and explicit confirmation are
// separate requests so a script cannot silently agree to newly changed meaning.
func cmdJanitor(args []string) error {
	if len(args) == 0 {
		return usagef("usage: apptown janitor <list|preview|enable|disable|history|assess> [options]")
	}
	if args[0] == "publication" {
		return cmdJanitorPublication(args[1:])
	}
	verb := args[0]
	fs := newFlags("janitor " + verb)
	file := fs.String("file", "", "JSON standing-rule definition to preview or confirm")
	confirm := fs.String("confirm", "", "exact meaning digest returned by preview")
	rule := fs.String("rule", "", "existing rule UUID when revising")
	revision := fs.Int64("revision", 0, "expected current revision for revision or disable")
	project := fs.String("project", "", "exact project UUID to list (omitted: administrative view)")
	var runID string
	fs.StringVar(&runID, "run", "", "exact run UUID to inspect without accepting or landing")
	args = parseArgs(fs, args[1:])
	var out json.RawMessage
	switch verb {
	case "list":
		if len(args) != 0 {
			return usagef("usage: apptown janitor list [--project UUID]")
		}
		path := "/standing-approvals"
		if *project != "" {
			path += "?projectId=" + url.QueryEscape(*project)
		}
		if err := api.get(path, &out); err != nil {
			return err
		}
	case "assess":
		if len(args) != 1 || runID == "" {
			return usagef("usage: apptown janitor assess <rule UUID> --run UUID")
		}
		if err := api.post("/standing-approvals/"+url.PathEscape(args[0])+"/assess", map[string]string{"runId": runID}, &out); err != nil {
			return err
		}
	case "history":
		if len(args) != 1 {
			return usagef("usage: apptown janitor history <rule UUID>")
		}
		if err := api.get("/standing-approvals/"+url.PathEscape(args[0])+"/history", &out); err != nil {
			return err
		}
	case "disable":
		if len(args) != 1 || *revision <= 0 {
			return usagef("usage: apptown janitor disable <rule UUID> --revision N")
		}
		if err := api.post("/standing-approvals/"+url.PathEscape(args[0])+"/disable", map[string]any{"expectedRevision": *revision}, &out); err != nil {
			return err
		}
	case "preview", "enable":
		if len(args) != 0 || *file == "" {
			return usagef("usage: apptown janitor %s --file definition.json [--confirm digest] [--rule UUID --revision N]", verb)
		}
		body, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		var definition map[string]json.RawMessage
		if err = json.Unmarshal(body, &definition); err != nil || definition == nil {
			return fmt.Errorf("the standing-rule definition must be a JSON object")
		}
		if verb == "preview" {
			if err = api.post("/standing-approvals/preview", definition, &out); err != nil {
				return err
			}
		} else {
			if *confirm == "" {
				return usagef("preview the definition and supply its exact --confirm digest; no standing consent was saved")
			}
			if (*rule == "") != (*revision == 0) {
				return usagef("revising consent needs both --rule and --revision")
			}
			command := map[string]any{"definition": definition, "shownDigest": *confirm, "expectedRevision": *revision}
			if *rule != "" {
				command["ruleId"] = *rule
			}
			if err = api.post("/standing-approvals", command, &out); err != nil {
				return err
			}
		}
	default:
		return usagef("unknown janitor command %q", verb)
	}
	return printJSON(out)
}

// Publication commands deliberately retain a caller-owned request UUID. Retrying
// a lost response after revocation returns the revoked receipt, not new consent.
func cmdJanitorPublication(args []string) error {
	if len(args) == 0 {
		return usagef("usage: apptown janitor publication <preview|classify|history|revoke> [options]")
	}
	verb := args[0]
	fs := newFlags("janitor publication " + verb)
	file := fs.String("file", "", "exact publication transition JSON")
	confirm := fs.String("confirm", "", "exact digest of the complete before/after preview")
	request := fs.String("request", "", "stable request UUID for this human classification act")
	args = parseArgs(fs, args[1:])
	var out json.RawMessage
	switch verb {
	case "preview", "classify":
		if len(args) != 0 || *file == "" {
			return usagef("usage: apptown janitor publication %s --file transition.json [--confirm digest --request UUID]", verb)
		}
		body, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		var transition map[string]json.RawMessage
		if err = json.Unmarshal(body, &transition); err != nil || transition == nil {
			return usagef("publication transition must be a JSON object")
		}
		if verb == "preview" {
			if err = api.post("/routine-publications/preview", transition, &out); err != nil {
				return err
			}
		} else {
			if *confirm == "" || *request == "" {
				return usagef("inspect the complete before/after preview and supply --confirm and a stable --request UUID; no consent was saved")
			}
			if err = api.post("/routine-publications", map[string]any{"transition": transition, "shownDigest": *confirm, "requestId": *request}, &out); err != nil {
				return err
			}
		}
	case "history":
		if len(args) != 2 {
			return usagef("usage: apptown janitor publication history <block|pack> <publication UUID>")
		}
		if err := api.get("/routine-publications/"+url.PathEscape(args[0])+"/"+url.PathEscape(args[1])+"/history", &out); err != nil {
			return err
		}
	case "revoke":
		if len(args) != 1 {
			return usagef("usage: apptown janitor publication revoke <classification UUID>")
		}
		if err := api.post("/routine-publications/"+url.PathEscape(args[0])+"/revoke", map[string]any{}, &out); err != nil {
			return err
		}
	default:
		return usagef("unknown publication command %q", verb)
	}
	return printJSON(out)
}
