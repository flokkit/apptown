package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/ids"
)

// fastModel resolves every slot to one model.
type fastModel struct{}

func (fastModel) Resolve(context.Context, ids.ID, string) (string, string, string, error) {
	return "test/fast", "fast", "standard", nil
}

// interviewer answers every question with answer, or fails with err.
type interviewer struct {
	answer string
	err    error
}

func (i interviewer) Ask(context.Context, modelcall.Call) (string, *modelcall.Usage, error) {
	return i.answer, nil, i.err
}

// The interview decides only when the interviewer named offered packs that
// can be taken together: no model, a failed call, an unreadable answer, a
// pack that is not offered, packs that disagree,
// or an answer that is neither a choice nor questions is an error with no
// pack in it, so the person chooses; no stock pack is chosen for them, and
// no reason is invented for a choice.
func TestPackInterviewDecidesOnlyWhatTheInterviewerChose(t *testing.T) {
	t.Parallel()
	f := newPackPullFixture(t)
	interview := func(h PackAssistance, body string) (int, map[string]any) {
		t.Helper()
		w := httptest.NewRecorder()
		h.PackInterview(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, _ := interview(NewPackAssistance(f.ways, testOffer{f.ways}, fastModel{}, interviewer{}), `{"description":"a game"}`); code != 422 {
		t.Fatalf("empty library: %d", code)
	}
	for name, spec := range map[string]string{"alpha": `{}`, "go-small": `{}`, "goish": `{"toolchain":"go"}`, "nodeish": `{"toolchain":"node"}`} {
		if _, err := f.ways.PublishPack(t.Context(), name, name, spec, ""); err != nil {
			t.Fatal(err)
		}
	}
	for name, c := range map[string]struct {
		h    PackAssistance
		body string
		code int
	}{
		"no model":                {NewPackAssistance(f.ways, testOffer{f.ways}, noBriefModel{}, interviewer{}), `{"description":"a game"}`, 422},
		"an unread model":         {NewPackAssistance(f.ways, testOffer{f.ways}, noBriefModel{err: errors.New("slots unreadable")}, interviewer{}), `{"description":"a game"}`, 500},
		"no description":          {NewPackAssistance(f.ways, testOffer{f.ways}, fastModel{}, interviewer{}), `{"description":" "}`, 400},
		"a failed call":           {NewPackAssistance(f.ways, testOffer{f.ways}, fastModel{}, interviewer{err: errors.New("provider down")}), `{"description":"a game"}`, 502},
		"an unreadable reply":     {NewPackAssistance(f.ways, testOffer{f.ways}, fastModel{}, interviewer{answer: "go-small, probably"}), `{"description":"a game"}`, 502},
		"an unoffered pack":       {NewPackAssistance(f.ways, testOffer{f.ways}, fastModel{}, interviewer{answer: `{"decided":true,"packs":["alpha","rails"],"why":"it has a server"}`}), `{"description":"a game"}`, 502},
		"packs that disagree":     {NewPackAssistance(f.ways, testOffer{f.ways}, fastModel{}, interviewer{answer: `{"decided":true,"packs":["goish","nodeish"]}`}), `{"description":"a game"}`, 502},
		"no choice, no questions": {NewPackAssistance(f.ways, testOffer{f.ways}, fastModel{}, interviewer{answer: `{"decided":false}`}), `{"description":"a game"}`, 502},
		"questions after answers": {NewPackAssistance(f.ways, testOffer{f.ways}, fastModel{}, interviewer{answer: `{"decided":false,"questions":["Is it multiplayer?"]}`}), `{"description":"a game","answers":[{"question":"Saves?","answer":"no"}]}`, 502},
		"a malformed request":     {NewPackAssistance(f.ways, testOffer{f.ways}, fastModel{}, interviewer{}), `{`, 400},
	} {
		if code, out := interview(c.h, c.body); code != c.code || out["packs"] != nil || out["decided"] != nil {
			t.Fatalf("%s: %d %v, want %d and no pack", name, code, out, c.code)
		}
	}
	// A valid choice is the interviewer's, with its own reason or none.
	h := NewPackAssistance(f.ways, testOffer{f.ways}, fastModel{}, interviewer{answer: "```json\n{\"decided\":true,\"packs\":[\"alpha\",\"go-small\"],\"icon\":\"🎮\"}\n```"})
	if code, out := interview(h, `{"description":"a game"}`); code != 200 || fmt.Sprint(out["packs"]) != "[alpha go-small]" || out["why"] != "" || out["icon"] != "🎮" || out["decided"] != true {
		t.Fatalf("a decision: %d %v", code, out)
	}
	h = NewPackAssistance(f.ways, testOffer{f.ways}, fastModel{}, interviewer{answer: `{"decided":false,"questions":["Do people play together?"]}`})
	if code, out := interview(h, `{"description":"a game"}`); code != 200 || out["decided"] != false || out["packs"] != nil {
		t.Fatalf("questions: %d %v", code, out)
	}
}

// testOffer stands in for the lifecycle's creation rule: offered packs
// that wow.Combine finds agreeing. The rule itself is tested where it lives.
type testOffer struct{ ways *wow.Service }

func (o testOffer) OfferedPacks(ctx context.Context, names []string) ([]*wow.Pack, error) {
	offered, err := o.ways.OfferedPacks(ctx)
	if err != nil {
		return nil, err
	}
	var out []*wow.Pack
	var taken []wow.Pack
	for _, name := range names {
		i := slices.IndexFunc(offered, func(p wow.Pack) bool { return p.Name == name })
		if i < 0 {
			return nil, fmt.Errorf("%s is not offered", name)
		}
		out, taken = append(out, &offered[i]), append(taken, offered[i])
	}
	if problems := wow.Combine(taken, nil).Problems; len(problems) != 0 {
		return nil, errors.New(strings.Join(problems, "; "))
	}
	return out, nil
}
