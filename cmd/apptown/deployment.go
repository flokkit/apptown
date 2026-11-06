package main

import "fmt"

func cmdDeployment(args []string) error {
	if len(args) > 0 && (args[0] == "start" || args[0] == "stop" || args[0] == "restart") {
		if len(args) != 3 {
			return usagef("usage: apptown deployment %s <project> <deployment>", args[0])
		}
		if err := api.post("/projects/"+args[1]+"/deployments/"+args[2]+"/"+args[0], map[string]any{}, nil); err != nil {
			return err
		}
		fmt.Printf("Deployment %s/%s: %s completed\n", args[1], args[2], args[0])
		return nil
	}
	if len(args) > 0 && args[0] == "set-feed" {
		return cmdSetFeed(args[1:])
	}
	if len(args) == 0 || args[0] != "confirm-feed" {
		return usagef("usage: apptown deployment start|stop|restart <project> <deployment> | confirm-feed <project> <deployment> | --all")
	}
	return cmdConfirmFeed(args[1:])
}

func cmdConfirmFeed(args []string) error {
	fs := newFlags("deployment confirm-feed")
	all := fs.Bool("all", false, "confirm every unconfirmed follows-main deployment you may authorize")
	args = parseArgs(fs, args)
	if *all {
		if len(args) != 0 {
			return usagef("usage: apptown deployment confirm-feed --all")
		}
		var out struct{ Confirmed, Refused []string }
		if err := api.post("/deployments/confirm-feed", map[string]any{}, &out); err != nil {
			return err
		}
		for _, dep := range out.Confirmed {
			fmt.Printf("Confirmed follows main: %s\n", dep)
		}
		for _, dep := range out.Refused {
			fmt.Printf("Not confirmed: %s\n", dep)
		}
		if len(out.Refused) > 0 {
			return fmt.Errorf("%d deployments could not be confirmed", len(out.Refused))
		}
		return nil
	}
	if len(args) != 2 {
		return usagef("usage: apptown deployment confirm-feed <project> <deployment> | --all")
	}
	var deps []struct {
		Name         string `json:"name"`
		ApprovalRole string `json:"approvalRole"`
		FeedRevision int64  `json:"feedRevision"`
	}
	if err := api.get("/projects/"+args[0]+"/deployments", &deps); err != nil {
		return err
	}
	for _, dep := range deps {
		if dep.Name == args[1] {
			if err := api.post("/projects/"+args[0]+"/deployments/"+args[1]+"/confirm-feed", map[string]any{"revision": dep.FeedRevision}, nil); err != nil {
				return err
			}
			fmt.Printf("Confirmed follows main: %s/%s\n", args[0], args[1])
			return nil
		}
	}
	return fmt.Errorf("no such deployment: %s/%s", args[0], args[1])
}

func cmdSetFeed(args []string) error {
	fs := newFlags("deployment set-feed")
	approvalRole := fs.String("approval-role", "", "role required to approve each release; none removes approval")
	from := fs.String("from", "", "deployment whose last successful release is promoted")
	args = parseArgs(fs, args)
	if len(args) != 3 {
		return usagef("usage: apptown deployment set-feed <project> <deployment> main|manual|promotion [--from <deployment>]")
	}
	if args[2] != "main" && args[2] != "manual" && args[2] != "promotion" {
		return usagef("choose main, manual or promotion")
	}
	if (args[2] == "promotion") != (*from != "") {
		return usagef("promotion needs --from <deployment>; other feeds have no source")
	}
	var deps []struct {
		Name         string `json:"name"`
		ApprovalRole string `json:"approvalRole"`
		FeedRevision int64  `json:"feedRevision"`
	}
	if err := api.get("/projects/"+args[0]+"/deployments", &deps); err != nil {
		return err
	}
	for _, dep := range deps {
		if dep.Name != args[1] {
			continue
		}
		role := dep.ApprovalRole
		if *approvalRole != "" {
			role = *approvalRole
		}
		if role == "none" {
			role = ""
		}
		if _, err := api.request("PUT", "/projects/"+args[0]+"/deployments/"+args[1]+"/feed", map[string]any{"revision": dep.FeedRevision, "feed": args[2], "from": *from, "approvalRole": role}); err != nil {
			return err
		}
		fmt.Printf("Updated deployment feed: %s/%s\n", args[0], args[1])
		return nil
	}
	return fmt.Errorf("no such deployment: %s/%s", args[0], args[1])
}
