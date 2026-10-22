package main

import (
	"fmt"
	"strconv"
	"strings"

	"flokkit.io/forge/internal/ids"
)

// evalScorecard is what `apptown eval show` reads (httpapi EvalScorecard).
type evalScorecard struct {
	ID           ids.ID `json:"id"`
	Number       int64  `json:"number"`
	StartedBy    string `json:"startedBy"`
	Subject      string `json:"subject"`
	State        string `json:"state"`
	Reason       string `json:"reason"`
	BudgetMicros int64  `json:"budgetMicros"`
	CostMicros   int64  `json:"costMicros"`
	Incomplete   int64  `json:"incomplete"`
	Arms         []struct {
		ID             ids.ID `json:"id"`
		SourceChangeID ids.ID `json:"sourceChangeId"`
		Arm            string `json:"arm"`
		Profile        string `json:"profile"`
		State          string `json:"state"`
		Reason         string `json:"reason"`
		ProjectID      ids.ID `json:"projectId"`
		ChangeID       ids.ID `json:"changeId"`
		Score          struct {
			ReadyToLand bool   `json:"readyToLand"`
			Landed      bool   `json:"landed"`
			Deploy      string `json:"deploy"`
			Checks      []struct {
				Step, Status string
			} `json:"checks"`
			RequestMet  string `json:"requestMet"`
			Reworks     int    `json:"reworks"`
			WorkSeconds int64  `json:"workSeconds"`
			Compared    *struct {
				Rows []struct {
					Block         string   `json:"block"`
					Check         string   `json:"check"`
					New           []string `json:"new"`
					Fixed         []string `json:"fixed"`
					NotComparable string   `json:"notComparable"`
				} `json:"rows"`
			} `json:"compared"`
			CompareError  string `json:"compareError"`
			NotComparable bool   `json:"notComparable"`
		} `json:"score"`
	} `json:"arms"`
	ArmCosts map[string]struct {
		CostMicros int64 `json:"costMicros"`
		Incomplete int64 `json:"incomplete"`
	} `json:"armCosts"`
	Complaints []struct {
		ID    ids.ID `json:"id"`
		About string `json:"about"`
		ArmID ids.ID `json:"armId"`
		Text  string `json:"text"`
	} `json:"complaints"`
}

func dollars(micros int64, incomplete int64) string {
	s := fmt.Sprintf("$%.2f", float64(micros)/1e6)
	if incomplete > 0 {
		s = "at least " + s
	}
	return s
}

// cmdEval: start an eval of a model profile or a pack version, show one,
// continue a held one, or delete one.
func cmdEval(args []string) error {
	if len(args) == 0 {
		return usagef("usage: apptown eval start|show|continue|delete …")
	}
	switch args[0] {
	case "start":
		return cmdEvalStart(args[1:])
	case "show":
		return cmdEvalShow(args[1:])
	case "continue":
		pos := parseArgs(newFlags("eval continue"), args[1:])
		if err := numberedArg(pos, "eval"); err != nil {
			return err
		}
		if _, err := api.request("POST", "/evals/"+pos[0]+"/continue", map[string]any{}); err != nil {
			return err
		}
		fmt.Printf("eval %s continues; its cost stays \"at least\" what is shown\n", pos[0])
		return nil
	case "delete":
		pos := parseArgs(newFlags("eval delete"), args[1:])
		if err := numberedArg(pos, "eval"); err != nil {
			return err
		}
		fmt.Printf("Deleting eval %s stops its arms, takes its copies' deployments down and deletes its copies.\n", pos[0])
		fmt.Println(dim("  What it cost and what was complained about in it are kept."))
		if ans := ask(fmt.Sprintf("type %q to delete it: ", pos[0])); ans != pos[0] {
			return fmt.Errorf("nothing deleted: what was typed is not %q", pos[0])
		}
		if _, err := api.request("DELETE", "/evals/"+pos[0], nil); err != nil {
			return err
		}
		fmt.Printf("eval %s deleted\n", pos[0])
		return nil
	}
	return usagef("usage: apptown eval start|show|continue|delete …")
}

func cmdEvalStart(args []string) error {
	fs := newFlags("eval start")
	changes := fs.String("changes", "", "the landed Changes to re-run, comma-separated idList")
	profile := fs.String("model", "", "the model profile to compare with what the projects run now")
	packVersion := fs.String("pack", "", "the pack version to compare with what the projects run now, as name@version")
	budget := fs.Float64("budget", 0, "the most the eval may spend, in dollars")
	yes := fs.Bool("y", false, "start without asking (the meaning is still shown)")
	parseArgs(fs, args)
	var idList []ids.ID
	for _, s := range strings.Split(*changes, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		id, err := changeRef(s, "")
		if err != nil {
			return fmt.Errorf("--changes: %w", err)
		}
		idList = append(idList, id)
	}
	usage := usagef("usage: apptown eval start --changes shop#12,shop#15 (--model <profile> | --pack <name@version>) --budget <dollars>")
	if len(idList) == 0 || (*profile == "") == (*packVersion == "") || *budget <= 0 {
		return usage
	}
	body := map[string]any{"changes": idList, "budgetDollars": *budget}
	subject := "profile " + bold(*profile)
	if *packVersion != "" {
		name, v, ok := strings.Cut(*packVersion, "@")
		version, err := strconv.ParseInt(v, 10, 64)
		if !ok || name == "" || err != nil || version < 1 {
			return usage
		}
		body["pack"], body["version"] = name, version
		subject = "pack version " + bold(*packVersion)
	} else {
		body["profile"] = *profile
	}
	var form struct {
		Meaning string `json:"meaning"`
	}
	if err := api.get("/evals/start", &form); err != nil {
		return err
	}
	fmt.Printf("Each Change is re-run twice in throwaway copies: as its project runs now, and with %s.\n", subject)
	fmt.Printf("They land and deploy with no one approving them, on the eval domain only, up to $%.2f.\n\n", *budget)
	fmt.Printf("Starting means: %s\n", bold(form.Meaning))
	if !*yes {
		if ans := ask("type \"start\" to start it: "); ans != "start" {
			return fmt.Errorf("nothing started")
		}
	}
	var out struct {
		ID ids.ID `json:"id"`
	}
	body["shown"] = form.Meaning
	if err := api.post("/evals", body, &out); err != nil {
		return err
	}
	fmt.Printf("eval started; apptown eval show %s\n", out.ID)
	return nil
}

func cmdEvalShow(args []string) error {
	pos := parseArgs(newFlags("eval show"), args)
	if err := numberedArg(pos, "eval"); err != nil {
		return err
	}
	var e evalScorecard
	if err := api.get("/evals/"+pos[0], &e); err != nil {
		return err
	}
	fmt.Printf("eval %d — %s vs what projects run now — %s", e.Number, bold(e.Subject), e.State)
	if e.Reason != "" {
		fmt.Printf(": %s", e.Reason)
	}
	fmt.Printf("\n  started by %s · spent %s of $%.2f\n", e.StartedBy, dollars(e.CostMicros, e.Incomplete), float64(e.BudgetMicros)/1e6)
	if e.State == "held" {
		fmt.Printf("  %s\n", dim(fmt.Sprintf("held: apptown eval continue %d to start the next arm", e.Number)))
	}
	for _, a := range e.Arms {
		cost := e.ArmCosts[a.ID.String()]
		fmt.Printf("\n  Change %s · %s (%s) · %s", a.SourceChangeID, a.Arm, a.Profile, a.State)
		if a.Reason != "" {
			fmt.Printf(": %s", a.Reason)
		}
		fmt.Println()
		if a.ProjectID.IsZero() {
			continue
		}
		sc := a.Score
		fmt.Printf("    ready to land %v · landed %v · deploy %s · request met %s · reworks %d · %ds of work · %s\n",
			sc.ReadyToLand, sc.Landed, orDash(sc.Deploy), orDash(sc.RequestMet), sc.Reworks, sc.WorkSeconds, dollars(cost.CostMicros, cost.Incomplete))
		for _, c := range sc.Checks {
			fmt.Printf("    landing gate %s: %s\n", c.Step, c.Status)
		}
		switch {
		case sc.Compared != nil:
			for _, r := range sc.Compared.Rows {
				switch {
				case r.NotComparable != "":
					fmt.Printf("    %s · %s: %s\n", r.Block, r.Check, dim("not comparable: "+r.NotComparable))
				case len(r.New)+len(r.Fixed) > 0:
					fmt.Printf("    %s · %s: %d new, %d fixed\n", r.Block, r.Check, len(r.New), len(r.Fixed))
				}
			}
		case sc.CompareError != "":
			fmt.Printf("    %s\n", dim("not compared: "+sc.CompareError))
		}
		if a.State != "scored" && a.State != "not-run" {
			fmt.Printf("    %s\n", dim(fmt.Sprintf("copy: project %s, Change %s (left until the eval is deleted)", a.ProjectID, a.ChangeID)))
		}
	}
	if len(e.Complaints) > 0 {
		fmt.Println("\n  complaints filed in the eval:")
		for _, c := range e.Complaints {
			fmt.Printf("    %s about %s (arm %s): %s\n", c.ID, c.About, c.ArmID, c.Text)
		}
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
