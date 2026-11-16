package main

import (
	"fmt"
	"net/url"

	"github.com/flokkit/apptown/internal/apptown/imagelines"
	"github.com/flokkit/apptown/internal/imageline"
)

func cmdImage(args []string) error {
	if len(args) < 2 || args[0] != "newest" {
		return fmt.Errorf("usage: apptown image newest <ref> --scope <line|patch|minor|major>")
	}
	fs := newFlags("image newest")
	scope := fs.String("scope", string(imageline.Line), fmt.Sprintf("%s, %s, %s or %s", imageline.Line, imageline.Patch, imageline.Minor, imageline.Major))
	asJSON := fs.Bool("json", false, "print the registry reading as JSON")
	parseArgs(fs, args[2:])
	var answer imagelines.Reading
	if err := api.get("/image-lines?ref="+url.QueryEscape(args[1])+"&scope="+url.QueryEscape(*scope), &answer); err != nil {
		return err
	}
	if *asJSON {
		return printJSON(answer)
	}
	evidence := answer.Evidence()
	if answer.Stale {
		evidence = "stale: " + evidence
	}
	fmt.Printf("%s\nregistry: %s\n%s\n", answer.Image(), answer.Registry, evidence)
	return nil
}
