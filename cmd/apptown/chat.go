package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

// cmdPreview boots or reuses a change's preview and prints its address:
// the change named, or inside a change chat THIS chat's change, with the
// address the chat's container can reach. Outside a chat it once refused,
// though AppTown previews any change on request (sweep 7).
func cmdPreview(args []string) error {
	if len(args) > 0 {
		if err := changeArg(args, ""); err != nil {
			return err
		}
		var out struct {
			URL    string `json:"url"`
			Commit string `json:"commit"`
		}
		fmt.Printf("booting change #%s's preview (or reusing a running one)…\n", args[0])
		if err := api.post("/changes/"+args[0]+"/preview", map[string]any{}, &out); err != nil {
			return err
		}
		at := ""
		if out.Commit != "" {
			at = " at " + out.Commit
		}
		fmt.Printf("preview of change #%s is up%s: %s\n", args[0], at, out.URL)
		return nil
	}
	sid, tok := os.Getenv("APPTOWN_SESSION_ID"), os.Getenv("APPTOWN_SESSION_TOKEN")
	if sid == "" || tok == "" {
		return usagef("usage: apptown preview <change-id> (inside a change chat, bare apptown preview previews that chat's change)")
	}
	var out struct {
		URL          string `json:"url"`
		ContainerURL string `json:"containerUrl"`
	}
	if err := api.post("/console/"+sid+"/preview", map[string]any{}, &out); err != nil {
		return err
	}
	fmt.Printf("preview is up.\n  from this container: %s\n  for people:          %s\n", out.ContainerURL, out.URL)
	return nil
}

// cmdWaitChat: block until the helper hands the turn back — the chat
// counterpart of apptown wait. The signal is the server's own turn
// detector (the one that gates the boot rehearsal), not a guess.
func cmdWaitChat(args []string) error {
	if len(args) < 1 {
		return usagef("usage: apptown wait-chat <session-id>")
	}
	for {
		var out struct {
			Turn  string `json:"turn"`
			State string `json:"state"`
		}
		if err := api.get("/console/"+args[0]+"/messages?from=999999", &out); err != nil {
			return err
		}
		if out.Turn == "handed-back" {
			fmt.Println("the helper handed the turn back")
			return nil
		}
		pause(10 * time.Second)
	}
}

// cmdChat: start a chat session (a change chat when -c is given) and
// print its session id — the handle for say/transcript.
func cmdChat(args []string) error {
	fs := newFlags("chat")
	project := fs.String("p", "", "project slug")
	change := fs.String("c", "", "the change: its id, <project>#<number>, or its number in the project (omit for a read-only project chat)")
	step := idFlag(fs, "step", "a stopped step to take to chat: its run stops (a landing ends, and the change goes back to work), and the chat continues from it")
	parseArgs(fs, args)
	if *project == "" || (!step.IsZero() && *change == "") {
		return usagef("usage: apptown chat -p <project> [-c <change> [-step <step-id>]]")
	}
	changeID := new(ids.ID)
	if *change != "" {
		id, err := changeRef(*change, *project)
		if err != nil {
			return err
		}
		*changeID = id
	}
	var out struct {
		Session struct {
			ID       string `json:"id"`
			ChangeID ids.ID `json:"changeId"`
		} `json:"session"`
	}
	if err := api.post("/projects/"+*project+"/console", map[string]any{"changeId": *changeID, "continueStep": *step}, &out); err != nil {
		return err
	}
	kind := "project chat (read-only)"
	if !out.Session.ChangeID.IsZero() {
		kind = fmt.Sprintf("change chat on %s", out.Session.ChangeID)
	}
	fmt.Printf("session %s — %s\nnext: apptown say %s \"...\" ; apptown transcript %s\n", out.Session.ID, kind, out.Session.ID, out.Session.ID)
	return nil
}

// cmdSay: deliver one message to a chat session's helper.
func cmdSay(args []string) error {
	if len(args) < 2 {
		return usagef("usage: apptown say <session-id> \"message\"")
	}
	if err := api.post("/console/"+args[0]+"/say", map[string]any{"text": strings.Join(args[1:], " ")}, nil); err != nil {
		return err
	}
	fmt.Println("delivered")
	return nil
}

// cmdInterrupt: press Escape in the helper's terminal — abort its current
// turn (a stuck tool) without touching the conversation or the workspace.
func cmdInterrupt(args []string) error {
	fs := newFlags("interrupt")
	step := fs.Bool("step", false, "the id is a running agent or review step's, not a chat's")
	args = parseArgs(fs, args)
	if len(args) < 1 {
		return usagef("usage: apptown interrupt <chat-id>, or apptown interrupt -step <step-id>")
	}
	// Chats and steps both have UUIDs: the caller says which it means. A
	// running agent or review step is stopped through the product and re-run
	// with run-step.
	if *step {
		if err := idArg(args, "step id"); err != nil {
			return err
		}
		var out struct {
			Stopped []string `json:"stopped"`
		}
		if err := api.post("/steps/"+args[0]+"/interrupt", map[string]any{}, &out); err != nil {
			return err
		}
		fmt.Printf("stopped step %s (%d container(s)) — apptown run-step %s to run it again\n", args[0], len(out.Stopped), args[0])
		return nil
	}
	if err := api.post("/console/"+args[0]+"/interrupt", map[string]any{}, nil); err != nil {
		return err
	}
	fmt.Println("stopped — tell it what you want instead")
	return nil
}

// cmdTranscript: print a chat session's messages from an offset.
func cmdTranscript(args []string) error {
	fs := newFlags("transcript")
	from := fs.Int("from", 0, "message offset to start at")
	args = parseArgs(fs, args)
	if len(args) < 1 {
		return usagef("usage: apptown transcript <session-id> [-from N]")
	}
	var out struct {
		Messages []struct {
			Role  string `json:"role"`
			Text  string `json:"text"`
			Tools []struct {
				Name   string `json:"name"`
				Detail string `json:"detail"`
			} `json:"tools"`
		} `json:"messages"`
		State string `json:"state"`
		Turn  string `json:"turn"`
	}
	if err := api.get(fmt.Sprintf("/console/%s/messages?from=%d", args[0], *from), &out); err != nil {
		return err
	}
	for i, m := range out.Messages {
		fmt.Printf("--- [%d] %s ---\n", *from+i, m.Role)
		for _, t := range m.Tools {
			fmt.Printf("  · %s %s\n", t.Name, t.Detail)
		}
		if txt := strings.TrimSpace(m.Text); txt != "" {
			fmt.Println(txt)
		}
	}
	fmt.Printf("(state: %s, turn: %s, next offset %d)\n", out.State, out.Turn, *from+len(out.Messages))
	return nil
}
