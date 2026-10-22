package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/flokkit/apptown/internal/ids"
)

// cmdAdopt / cmdRetire: integration workflows with the attach/detach step folded in.
func cmdAdopt(args []string) error {
	fs := newFlags("adopt")
	project := fs.String("p", "", "project slug")
	connector := fs.String("via", "", "connector name (default: the capability's first)")
	yes := fs.Bool("y", false, "auto-approve gates and landing")
	pos := parseArgs(fs, args)
	if len(pos) < 1 {
		return usagef("usage: apptown adopt <capability> [-p project] [--via connector]")
	}
	capability := pos[0]
	slug, err := resolveProject(*project)
	if err != nil {
		return err
	}
	// Attach first (idempotent enough: 409/conflicts surface as errors).
	var att struct {
		Provenance string `json:"provenance"`
		Connector  string `json:"connector"`
	}
	reqBody := map[string]string{"capability": capability}
	if *connector != "" {
		reqBody["connector"] = *connector
	}
	if err := api.post("/projects/"+slug+"/integrations", reqBody, &att); err != nil {
		// Already attached is fine — the workflow still wires the code.
		fmt.Println(dim("attach: " + err.Error() + " (continuing — the workflow wires the code)"))
	} else {
		fmt.Printf("%s %s attached (%s%s)\n", green("✓"), capability, att.Provenance,
			map[bool]string{true: " via " + att.Connector, false: ""}[att.Connector != ""])
	}
	body := fmt.Sprintf("Adopt the %q integration: wire this app into the newly attached integration, following the integration's guidance.", capability)
	return openAndFollow(slug, "adopt-integration", "adopt "+capability, body, *yes, false)
}

func cmdRetire(args []string) error {
	fs := newFlags("retire")
	project := fs.String("p", "", "project slug")
	yes := fs.Bool("y", false, "auto-approve gates and landing")
	pos := parseArgs(fs, args)
	if len(pos) < 1 {
		return usagef("usage: apptown retire <capability> [-p project]")
	}
	capability := pos[0]
	slug, err := resolveProject(*project)
	if err != nil {
		return err
	}
	body := fmt.Sprintf("Retire the %q integration: remove all of its wiring from this product. The service is being retired org-side; after this lands its env vars will stop being injected.", capability)
	if err := openAndFollow(slug, "retire-integration", "retire "+capability, body, *yes, false); err != nil {
		return err
	}
	ans := "y"
	if !*yes {
		ans = ask("detach the integration now (stop passing its settings and guidance)? [y/N] ")
	}
	if strings.EqualFold(ans, "y") {
		if _, derr := api.request("DELETE", "/projects/"+slug+"/integrations/"+capability, nil); derr != nil {
			return derr
		}
		fmt.Println(green("✓") + " detached")
	}
	return nil
}

// cmdIntegrations: what this project is connected to, from the app's side —
// wired in or not, and (errors) when the provider last saw an event. This is
// how an agent proves its wiring reached the provider (design: test hooks).
func cmdIntegrations(args []string) error {
	fs := newFlags("integrations")
	proj := fs.String("p", os.Getenv("APPTOWN_PROJECT"), "project slug (default: $APPTOWN_PROJECT, set in agent steps)")
	parseArgs(fs, args)
	if *proj == "" {
		return usagef("no project: pass -p <slug> (or run inside an agent step, where $APPTOWN_PROJECT is set)")
	}
	var out struct {
		Attachments []struct {
			Capability string `json:"capability"`
			Title      string `json:"title"`
			Connector  string `json:"connector"`
			Wirable    bool   `json:"wirable"`
			LastEvent  string `json:"lastEvent"`
			Wire       *struct {
				ChangeID ids.ID `json:"changeId"`
				State    string `json:"state"`
			} `json:"wire"`
			Resource map[string]any `json:"resource"`
		} `json:"attachments"`
	}
	if err := api.get("/projects/"+*proj+"/integrations", &out); err != nil {
		return err
	}
	if len(out.Attachments) == 0 {
		fmt.Println("nothing attached")
		return nil
	}
	for _, a := range out.Attachments {
		state := ""
		switch {
		case !a.Wirable:
			state = "used by AppTown's deploy"
		case a.Wire == nil:
			state = "not wired in yet"
		case a.Wire.State == "shipped":
			state = "wired in"
		default:
			state = fmt.Sprintf("wiring · change %s %s", a.Wire.ChangeID, a.Wire.State)
		}
		fmt.Printf("%-22s %-14s %s", a.Title, a.Capability, state)
		if a.Capability == "errors" {
			if a.LastEvent != "" {
				fmt.Printf(" · last event seen %s", a.LastEvent)
			} else {
				fmt.Printf(" · no events seen at the provider yet")
			}
		}
		fmt.Println()
		if u, ok := a.Resource["unresolved"].([]any); ok && len(u) > 0 {
			for _, x := range u {
				if m, ok := x.(map[string]any); ok {
					fmt.Printf("    ⚠ %v ×%v (last %v)\n", m["title"], m["count"], m["lastSeen"])
				}
			}
		}
	}
	return nil
}
