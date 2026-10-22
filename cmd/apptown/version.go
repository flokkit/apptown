package main

import (
	"fmt"

	"github.com/flokkit/apptown/internal/version"
)

// cmdVersion prints this CLI's version, then AppTown's as its answer
// names it. An AppTown that cannot be reached is said, not an error: the CLI's
// own version is still the answer to "what is this".
func cmdVersion(args []string) error {
	fs := newFlags("version")
	asJSON := fs.Bool("json", false, "print both versions as JSON")
	parseArgs(fs, args)
	v := Versions{CLI: CLIVersion{Version: version.String(), Protocol: version.CLIProtocol}, AppTown: AppTownVersion{URL: api.base}}
	if !*asJSON {
		fmt.Printf("apptown CLI %s (CLI protocol %d)\n", v.CLI.Version, v.CLI.Protocol)
	}
	res, err := api.send("GET", "/setup", nil)
	if err != nil {
		if *asJSON {
			return printJSON(v)
		}
		fmt.Printf("AppTown at %s: not reached (%v)\n", api.base, err)
		return nil
	}
	defer res.Body.Close()
	v.AppTown.Reached, v.AppTown.Version = true, res.Header.Get(version.Header)
	if *asJSON {
		if err := printJSON(v); err != nil {
			return err
		}
	} else {
		stated := v.AppTown.Version
		if stated == "" {
			stated = "no version stated"
		}
		fmt.Printf("AppTown at %s: %s\n", api.base, stated)
	}
	if res.StatusCode == 426 {
		_, err := api.answer(res, "GET", "/setup")
		return err
	}
	return nil
}
