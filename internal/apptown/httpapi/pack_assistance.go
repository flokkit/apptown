package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/ids"
)

type PackAssistance struct {
	ways       *wow.Service
	offer      PackOffer
	selection  ModelSelection
	completion modelcall.Completer
}

// PackOffer is the owner's answer to "may a new project take these packs
// together" (projectlifecycle.Service.OfferedPacks): the rule creation
// applies, so the interview never suggests what creation would refuse.
type PackOffer interface {
	OfferedPacks(ctx context.Context, names []string) ([]*wow.Pack, error)
}

func NewPackAssistance(ways *wow.Service, offer PackOffer, selection ModelSelection, completion modelcall.Completer) PackAssistance {
	return PackAssistance{ways: ways, offer: offer, selection: selection, completion: completion}
}

// PackInterview: POST /packs/interview {description, answers?} — the
// new-project wizard's short interview. Round one: the
// fast model either decides the packs outright or asks AT MOST three
// yes/no questions that genuinely discriminate between the available
// setups (multiplayer? shared saves? uploads?) — phrased for someone who
// is not a developer, never naming technology. Round two (answers present): it MUST decide.
// When the interview cannot decide (no model, a failed call, an answer that
// is not a valid choice) it answers an error and decides nothing: the
// person chooses the way of working.
func (h PackAssistance) PackInterview(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Description string `json:"description"`
		Answers     []struct {
			Question string `json:"question"`
			Answer   string `json:"answer"` // "yes" | "no" | "not sure"
		} `json:"answers"`
	}) {
		packs, err := h.ways.OfferedPacks(r.Context())
		if err != nil {
			WriteFault(w, err)
			return
		}
		if len(packs) == 0 {
			WriteError(w, 422, "no packs are turned on — turn one on under Ways of working")
			return
		}
		if strings.TrimSpace(in.Description) == "" {
			WriteError(w, 400, "describe the project first")
			return
		}
		pattern, _, _, err := h.selection.Resolve(r.Context(), ids.Nil, "fast")
		if err != nil {
			WriteFault(w, err)
			return
		}
		if pattern == "" || h.completion == nil {
			WriteError(w, 422, "no model is set up to suggest a way of working; choose one yourself")
			return
		}

		var opts strings.Builder
		for _, p := range packs {
			fmt.Fprintf(&opts, "- %s: %s\n", p.Name, p.Description)
		}
		var qa strings.Builder
		for _, ans := range in.Answers {
			fmt.Fprintf(&qa, "Q: %s\nA: %s\n", ans.Question, ans.Answer)
		}
		mustDecide := len(in.Answers) > 0
		system := `You help set up a new software project by picking its way of working: one
or more of these packs, taken together. Each pack's description says what
it is taken with; pick a set the descriptions say belong together, and a
pack that stands alone by itself:
` + opts.String() + `
From the person's description` + map[bool]string{true: " and their answers", false: ""}[mustDecide] + `, either DECIDE, or ask the FEWEST
yes/no questions (1-3) that truly distinguish between these setups —
things like: do other people play/use it at the same time? should saves
work across different devices? do people upload files or photos? does it
need to remember things when the browser closes? The person can only
answer "yes", "no" or "not sure", so every question must be one a plain
yes or no answers: never offer two choices ("A, or B?"). Questions must be
answerable by someone who is not a developer and must NEVER mention
technology names.
` + map[bool]string{true: "Answers are now provided: you MUST decide (treat 'not sure' as no).", false: "If the description already settles it, decide without questions."}[mustDecide] + `
Also pick the project's FACE: one single emoji that captures what the
project IS (a recipe app might be 🍳, a space game 🚀) — specific beats
generic, never a tool or tech emoji.
Respond with ONLY one JSON object, either:
{"decided": true, "packs": ["<name from the list>", ...], "why": "<one plain sentence anyone understands, no technology names>", "icon": "<one emoji>"}
or
{"decided": false, "questions": ["<question 1>", "<question 2>"]}`

		user := "Project: " + in.Description
		if qa.Len() > 0 {
			user += "\n\nTheir answers:\n" + qa.String()
		}
		out, _, err := h.completion.Ask(r.Context(), modelcall.Call{Purpose: "pack-assistance", Model: pattern, System: system, User: user})
		if err != nil {
			WriteError(w, 502, "the interviewer could not be asked (%v); choose a way of working yourself", err)
			return
		}
		clean := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(out), "```json"), "```"), "```"))
		var v struct {
			Decided   bool     `json:"decided"`
			Packs     []string `json:"packs"`
			Why       string   `json:"why"`
			Icon      string   `json:"icon"`
			Questions []string `json:"questions"`
		}
		if json.Unmarshal([]byte(clean), &v) != nil {
			WriteError(w, 502, "the interviewer's answer could not be read; choose a way of working yourself")
			return
		}
		if v.Decided || mustDecide {
			// Only packs the interviewer named, that this AppTown offers and
			// that can be taken together, are a decision; its reason is the
			// interviewer's own, or none.
			if len(v.Packs) == 0 {
				WriteError(w, 502, "the interviewer named no pack; choose a way of working yourself")
				return
			}
			if _, err := h.offer.OfferedPacks(r.Context(), v.Packs); err != nil {
				WriteError(w, 502, "the interviewer chose %s, which a new project cannot take (%v); choose a way of working yourself", strings.Join(v.Packs, " + "), err)
				return
			}
			WriteJSON(w, 200, PackInterviewPick{Decided: true, Packs: v.Packs, Why: strings.TrimSpace(v.Why), Icon: strings.TrimSpace(v.Icon)})
			return
		}
		if len(v.Questions) == 0 {
			WriteError(w, 502, "the interviewer neither chose a way of working nor asked a question; choose one yourself")
			return
		}
		if len(v.Questions) > 3 {
			v.Questions = v.Questions[:3]
		}
		WriteJSON(w, 200, PackInterviewQuestions{Decided: false, Questions: v.Questions})
	})
}

// PackInterviewPick is the setup the interview chose, and why in plain words. Icon is
// the project's suggested face, empty when the interviewer offered none.
type PackInterviewPick struct {
	Decided bool     `json:"decided"`
	Packs   []string `json:"packs"`
	Why     string   `json:"why"`
	Icon    string   `json:"icon"`
}

// PackInterviewQuestions are the questions the interview still needs answered.
type PackInterviewQuestions struct {
	Decided   bool     `json:"decided"`
	Questions []string `json:"questions"`
}
