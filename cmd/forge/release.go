package main

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// cmdRelease releases what landed on main. Named, it goes to that
// deployment through its deploy route (a landing-fed deployment is refused
// by name: every landing already deploys it); unnamed, to the intranet
// (design/subsystems/lan-release.md): image from the committed tree →
// registry → Portainer stack behind Traefik. The gates ran at landing; this
// is distribution. Either way the CLI follows the release's log until it
// settles.
func cmdRelease(args []string) error {
	fs := newFlags("release")
	domain := fs.String("domain", "", "intranet only: an extra hostname to serve on (kept for later releases; DNS is managed by the platform)")
	asJSON := fs.Bool("json", false, "print the release as JSON once the forge accepts it (no following)")
	acceptUncopied := fs.Bool("accept-uncopied-data", false, "release though the forge cannot copy the data the deployment holds now (the release records your choice): a change this release makes to that data has no copy to go back to")
	acceptUnchecked := fs.Bool("accept-unchecked-copy", false, "a deployment's first release that starts from a copy: fill it though an item of the copy was not shown to come back by its last check (the release records your choice)")
	args = parseArgs(fs, args)
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: forge release <project-slug> <deployment>, or forge release <project-slug> [-domain host] for the intranet (either with --json)")
	}
	slug := args[0]
	var number int64
	var where, url, state string
	if len(args) == 2 {
		if *domain != "" {
			return fmt.Errorf("-domain is the intranet's; a named deployment keeps its own address")
		}
		where = args[1]
		var out struct {
			Number int64  `json:"number"`
			State  string `json:"state"`
		}
		body := map[string]any{}
		if *acceptUncopied {
			body["acceptUncopiedData"] = true
		}
		if *acceptUnchecked {
			body["acceptUncheckedCopy"] = true
		}
		if err := api.post("/projects/"+slug+"/deployments/"+where+"/deploy", body, &out); err != nil {
			return err
		}
		number, state = out.Number, out.State
	} else {
		where = "the intranet"
		var out struct {
			Number int64  `json:"number"`
			URL    string `json:"url"`
			State  string `json:"state"`
		}
		if *acceptUnchecked {
			return fmt.Errorf("--accept-unchecked-copy is for a deployment that starts from a copy; the intranet does not")
		}
		body := map[string]any{"domain": *domain}
		if *acceptUncopied {
			body["acceptUncopiedData"] = true
		}
		if err := api.post("/projects/"+slug+"/release", body, &out); err != nil {
			return err
		}
		number, url, state = out.Number, out.URL, out.State
	}
	if *asJSON {
		r := ReleaseRequested{Project: slug, Number: number, URL: url, State: state}
		if len(args) == 2 {
			r.Deployment = where
		}
		return printJSON(r)
	}
	fmt.Printf("%s release r%d to %s started; following its log:\n", bold("▶"), number, where)
	switch state, err := followRelease(slug, number, 2*time.Second); {
	case err != nil:
		return err
	case state == "released":
		if url != "" {
			fmt.Printf("%s live: %s\n", bold("✓"), url)
		} else {
			fmt.Printf("%s released to %s\n", bold("✓"), where)
		}
		return nil
	default:
		return fmt.Errorf("the release failed — the log above shows where it stopped")
	}
}

// followRelease prints a release's log as it is written until the release
// settles, and answers how it settled: released or failed. A poll that
// fails is ridden out (the forge restarts for a deploy and answers 502 for a
// few seconds); an answer that will not change (no such release) is given at
// once.
func followRelease(slug string, number int64, interval time.Duration) (string, error) {
	seen, misses := 0, 0
	for {
		var d struct {
			Release struct {
				State string `json:"state"`
			} `json:"release"`
			Events []struct {
				At   string `json:"at"`
				Text string `json:"text"`
			} `json:"events"`
		}
		if err := api.get(fmt.Sprintf("/projects/%s/releases/%d", slug, number), &d); err != nil {
			var refused *httpError
			if misses++; (errors.As(err, &refused) && refused.final()) || misses >= 10 {
				return "", err
			}
			fmt.Fprintf(os.Stderr, "… %v (retrying)\n", err)
			pause(interval)
			continue
		}
		misses = 0
		for ; seen < len(d.Events); seen++ {
			at := d.Events[seen].At
			if len(at) >= 19 {
				at = at[11:19]
			}
			fmt.Printf("  %s  %s\n", at, d.Events[seen].Text)
		}
		switch d.Release.State {
		case "released", "failed":
			return d.Release.State, nil
		}
		pause(interval)
	}
}
