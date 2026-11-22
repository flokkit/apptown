package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/ids"
)

// Janitor commands read scans and manage choices.
func cmdJanitor(args []string) error {
	if len(args) == 0 {
		return usagef("usage: apptown janitor <choices|set|history|assess|attempts|run|majors> [options]")
	}
	if args[0] == "run" {
		fs := newFlags("janitor run")
		slug := fs.String("p", "", "project slug")
		queue := fs.String("queue", "", "queue name (omitted: all)")
		again := fs.Bool("again", false, "close the queue's open change and try again")
		rest := parseArgs(fs, args[1:])
		if *slug == "" || len(rest) != 0 || (*again && *queue == "") {
			return usagef("usage: apptown janitor run -p <project> [-queue name] [-again]")
		}
		path := "/projects/" + url.PathEscape(*slug)
		req := changeactions.JanitorRun{Queue: *queue, Again: *again}
		if *again {
			var view httpapi.ProjectScanView
			if err := api.get(path+"/scans", &view); err != nil {
				return err
			}
			found := false
			for _, q := range view.Queues {
				if q.Queue == *queue {
					req.ChangeID = q.ChangeID
					found = true
				}
			}
			if !found {
				return fmt.Errorf("queue %q is not adopted by this project", *queue)
			}
		}
		var out httpapi.JanitorRunView
		if err := api.post(path+"/janitor/run", req, &out); err != nil {
			return err
		}
		return printJSON(out)
	}
	if args[0] == "majors" {
		return janitorMajors(args[1:])
	}
	if args[0] == "attempts" {
		fs := newFlags("janitor attempts")
		var slug string
		fs.StringVar(&slug, "p", "", "project slug")
		queue := fs.String("queue", "", "queue name")
		rest := parseArgs(fs, args[1:])
		if slug == "" || len(rest) != 0 {
			return usagef("usage: apptown janitor attempts -p <slug> [--queue name]")
		}
		var out json.RawMessage
		if err := api.get("/projects/"+url.PathEscape(slug)+"/janitor/attempts?queue="+url.QueryEscape(*queue), &out); err != nil {
			return err
		}
		return printJSON(out)
	}
	verb := args[0]
	fs := newFlags("janitor " + verb)
	dependencies := fs.String("dependencies", "", "off, routine or aggressive")
	security := fs.Bool("security-fixes", false, "keep security fixes updated")
	images := fs.Bool("base-images", false, "keep base images updated")
	toolchain := fs.Bool("toolchain-patches", false, "toolchain patch releases within the same minor")
	packs := fs.Bool("packs", false, "keep packs updated")
	revision := fs.Int64("revision", -1, "expected current revision (0 for first save)")
	var runID string
	fs.StringVar(&runID, "run", "", "exact run UUID")
	rest := parseArgs(fs, args[1:])
	if len(rest) != 0 {
		return usagef("unexpected arguments for janitor %s", verb)
	}
	var out json.RawMessage
	switch verb {
	case "choices":
		if err := api.get("/janitor/choices", &out); err != nil {
			return err
		}
	case "history":
		if err := api.get("/janitor/choices/history", &out); err != nil {
			return err
		}
	case "assess":
		if runID == "" {
			return usagef("usage: apptown janitor assess --run RUN")
		}
		if err := api.post("/janitor/assess", map[string]string{"run": runID}, &out); err != nil {
			return err
		}
	case "set":
		if *revision < 0 || (*dependencies != "off" && *dependencies != "routine" && *dependencies != "aggressive") {
			return usagef("usage: apptown janitor set --dependencies LEVEL [--security-fixes] [--base-images] [--toolchain-patches] [--packs] --revision N")
		}
		body := map[string]any{"choices": map[string]any{"dependencies": *dependencies, "securityFixes": *security, "baseImages": *images, "toolchainPatches": *toolchain, "packs": *packs}, "expectedRevision": *revision}
		raw, err := api.request("PUT", "/janitor/choices", body)
		if err != nil {
			return err
		}
		out = raw
	default:
		return usagef("unknown janitor command %q", verb)
	}
	return printJSON(out)
}

// janitorMajors asks AppTown, for a Janitor run's agent, which direct
// dependencies are due to move to their newest major.
func janitorMajors(args []string) error {
	fs := newFlags("janitor majors")
	runFlag := os.Getenv("APPTOWN_RUN_ID")
	fs.StringVar(&runFlag, "run", runFlag, "run (default: this run, APPTOWN_RUN_ID)")
	asJSON := fs.Bool("json", false, "print the decisions as JSON")
	purls := parseArgs(fs, args)
	id, err := ids.Parse(runFlag)
	if err != nil || len(purls) == 0 {
		return usagef("usage: apptown janitor majors [-run RUN] pkg:npm/react@18.3.1 [pkg:pypi/django@4.2.16 pkg:docker/library/php@8.3.35 …]")
	}
	q := url.Values{"purl": purls}
	var out []verification.MajorMove
	if err := api.get("/runs/"+id.String()+"/major-moves?"+q.Encode(), &out); err != nil {
		return err
	}
	if *asJSON {
		return printJSON(out)
	}
	for _, m := range out {
		fmt.Println(m.Text())
	}
	return nil
}
