package main

import (
	"fmt"
	"os"
	"strings"

	"flokkit.io/forge/internal/ids"
)

// cmdComplain files a complaint about the platform.
// Attribution rides along automatically: APPTOWN_RUN_ID/APPTOWN_STEP in a run
// container, the session token in a console.
func cmdComplain(args []string) error {
	fs := newFlags("complain")
	why := fs.String("why", "", "why it slowed you down")
	wish := fs.String("wish", "", "what you wish had been true")
	about := fs.String("about", "", "what it is about: apptown (default), way-of-working or project")
	asJSON := fs.Bool("json", false, "print the filed complaint as JSON")
	// Text and flags in any order (agents write text first, naturally).
	text := strings.TrimSpace(strings.Join(parseArgs(fs, args), " "))
	if text == "" {
		return usagef(`usage: apptown complain "what happened" [-why "why it slowed you down"] [-wish "what you wish had been true"] [-about apptown|way-of-working|project]`)
	}
	// Set in run containers; absent or malformed elsewhere, which files the
	// complaint without a run (Nil, sent as null).
	runID, _ := ids.Parse(os.Getenv("APPTOWN_RUN_ID"))
	var out struct {
		ID ids.ID `json:"id"`
	}
	if err := api.post("/complaints", map[string]any{"text": text, "why": *why, "wish": *wish,
		"runId": runID, "step": os.Getenv("APPTOWN_STEP"), "about": *about}, &out); err != nil {
		return err
	}
	if *asJSON {
		return printJSON(ComplaintFiled{ID: out.ID})
	}
	fmt.Printf("complaint %s filed\n", out.ID)
	return nil
}

// cmdComplaints lists the complaints box — the read half (the write
// half is apptown complain). Noticed missing when reading complaints
// meant reaching for curl.
func cmdComplaints(args []string) error {
	// apptown complaints answer <id> -m "the resolution" — put the answer on
	// the record (marks it seen; the reply shows to every future reader).
	if len(args) > 0 && args[0] == "answer" {
		fs := newFlags("complaints answer")
		msg := fs.String("m", "", "the answer — what was done about it")
		pos := parseArgs(fs, args[1:])
		if len(pos) < 1 || *msg == "" {
			return usagef(`usage: apptown complaints answer <id> -m "<what was done>"`)
		}
		if err := numberedArg(pos, "complaint"); err != nil {
			return err
		}
		id := pos[0]
		if _, err := api.request("POST", "/complaints/"+id+"/answer", map[string]string{"answer": *msg}); err != nil {
			return err
		}
		fmt.Printf("✓ complaint #%s answered\n", id)
		return nil
	}
	var out struct {
		Complaints []struct {
			ID         ids.ID `json:"id"`
			Number     int64  `json:"number"`
			At         string `json:"at"`
			Source     string `json:"source"`
			Actor      string `json:"actor"`
			Project    string `json:"project"`
			ChangeID   ids.ID `json:"changeId"`
			Text       string `json:"text"`
			Why        string `json:"why"`
			Wish       string `json:"wish"`
			Seen       bool   `json:"seen"`
			Answer     string `json:"answer"`
			AnsweredBy string `json:"answeredBy"`
			AnsweredAt string `json:"answeredAt"`
		} `json:"complaints"`
	}
	if err := api.get("/complaints", &out); err != nil {
		return err
	}
	for _, c := range out.Complaints {
		seen := " "
		if !c.Seen {
			seen = "*"
		}
		fmt.Printf("%s %d  %s  %s/%s", seen, c.Number, c.At[:10], c.Source, c.Actor)
		if c.Project != "" {
			fmt.Printf("  %s", c.Project)
		}
		if !c.ChangeID.IsZero() {
			fmt.Printf(" change %d", c.ChangeID)
		}
		fmt.Printf("\n    what: %s\n", c.Text)
		if c.Why != "" {
			fmt.Printf("    why:  %s\n", c.Why)
		}
		if c.Wish != "" {
			fmt.Printf("    wish: %s\n", c.Wish)
		}
		if c.Answer != "" {
			fmt.Printf("    ↳ %s (%s, %s)\n", c.Answer, c.AnsweredBy, c.AnsweredAt[:10])
		}
	}
	return nil
}
