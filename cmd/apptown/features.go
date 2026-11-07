package main

import (
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/identity"
)

func cmdFeatures(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	if (args[0] == "list" && len(args) != 1) || (args[0] != "list" && (len(args) != 2 || (args[0] != "on" && args[0] != "off"))) {
		return usagef("usage: apptown features [list|on <area>|off <area>]")
	}
	var features []identity.Feature
	if err := api.get("/features", &features); err != nil {
		return err
	}
	if args[0] != "list" {
		for _, f := range features {
			if f.Area == args[1] {
				raw, err := api.request("PUT", "/features/"+f.Area, map[string]bool{"expected": f.Enabled, "enabled": args[0] == "on"})
				if err != nil {
					return err
				}
				_ = raw
				fmt.Printf("%s: %s\n", f.Name, args[0])
				return nil
			}
		}
		return usagef("unknown feature area: %s", args[1])
	}
	for _, f := range features {
		state := "off"
		if f.Enabled {
			state = "on"
		}
		fmt.Printf("%-22s %-10s %-3s %s\n", f.Area, f.Tier, state, f.Covers)
	}
	return nil
}
