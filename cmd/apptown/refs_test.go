package main

import (
	"strings"
	"testing"
)

// A change is named the same way everywhere a command takes one: check
// takes its friendly form, or its number inside the project.
func TestCheckTakesAChangesFriendlyForm(t *testing.T) {
	w := newCheckWorld(t, nil)
	t.Setenv("APPTOWN_PROJECT", "shop")
	for _, ref := range []string{"shop#7", "7"} {
		*w.bodies = nil
		out, err := run(t, "check", "-c", ref, "--published")
		if err != nil || len(*w.bodies) != 1 || !strings.Contains(out, "gate clear for commit "+w.published) {
			t.Fatalf("check -c %s: %v %v\n%s", ref, err, *w.bodies, out)
		}
	}
}

// chat opens the change its friendly form or project number names.
func TestChatTakesAChangesFriendlyForm(t *testing.T) {
	seen := fakeForge(t, func(method, path string) (int, string) {
		switch path {
		case "/changes/shop#12":
			return 200, `{"projectSlug":"shop","change":{"id":"00000000-000c-7000-8000-000000000000","number":12}}`
		case "/projects/shop/console":
			return 201, `{"session":{"id":"00000000-00aa-7000-8000-000000000000","changeId":"00000000-000c-7000-8000-000000000000"}}`
		}
		return 404, `{"error":"no"}`
	})
	for _, ref := range []string{"shop#12", "12"} {
		if _, err := run(t, "chat", "-p", "shop", "-c", ref); err != nil {
			t.Fatalf("chat -c %s: %v", ref, err)
		}
		last := (*seen)[len(*seen)-1]
		if last.Path != "/projects/shop/console" || last.Body["changeId"] != "00000000-000c-7000-8000-000000000000" {
			t.Fatalf("chat -c %s asked %+v", ref, last)
		}
	}
}

// interrupt reaches the kind of thing the caller names: a chat by default,
// a running step with -step. Both are UUIDs, so nothing is guessed from the
// id.
func TestInterruptReachesWhatItIsTold(t *testing.T) {
	const id = "00000000-0007-7000-8000-000000000000"
	seen := fakeForge(t, func(string, string) (int, string) { return 200, `{"stopped":["c1"]}` })
	if _, err := run(t, "interrupt", "-step", id); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "interrupt", id); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 2 || (*seen)[0].Path != "/steps/"+id+"/interrupt" || (*seen)[1].Path != "/console/"+id+"/interrupt" {
		t.Fatalf("interrupt asked %+v", *seen)
	}
}

// A complaint names the run it was filed from by its UUID, and no run
// (null) outside one or when the variable is not a run's id.
func TestAComplaintNamesItsRunByID(t *testing.T) {
	seen := fakeForge(t, func(string, string) (int, string) { return 201, `{"id":"00000000-0001-7000-8000-000000000000"}` })
	for env, want := range map[string]any{"": nil, "garbled": nil, "00000000-0046-7000-8000-000000000000": "00000000-0046-7000-8000-000000000000"} {
		t.Setenv("APPTOWN_RUN_ID", env)
		if _, err := run(t, "complain", "it was slow"); err != nil {
			t.Fatal(err)
		}
		last := (*seen)[len(*seen)-1]
		if got, ok := last.Body["runId"]; !ok || got != want {
			t.Fatalf("APPTOWN_RUN_ID=%q sent runId %v", env, got)
		}
	}
}

// A run is named the way a change is: its id, its friendly form, or its
// number inside the project. AppTown resolves the form; the command then
// asks for the run by the id it answered.
func TestARunCommandTakesARunsFriendlyForm(t *testing.T) {
	const runID = "00000000-0028-7000-8000-000000000000"
	seen := fakeForge(t, func(_, path string) (int, string) {
		switch path {
		case "/runs/shop#40", "/runs/" + runID:
			return 200, `{"run":{"id":"` + runID + `","number":40}}`
		case "/runs/" + runID + "/timing":
			return 200, `{"run":"` + runID + `","workflow":"flow","createdAt":"2026-10-13T00:00:00Z","steps":[]}`
		}
		return 404, `{"error":"no"}`
	})
	t.Setenv("APPTOWN_PROJECT", "shop")
	for _, ref := range []string{"shop#40", "40", runID} {
		*seen = nil
		_, _ = run(t, "status", ref)
		if len(*seen) == 0 || (*seen)[len(*seen)-1].Path != "/runs/"+runID {
			t.Fatalf("status %s asked %+v; want the run by its id", ref, *seen)
		}
		*seen = nil
		if _, err := run(t, "timing", "-r", ref); err != nil || len(*seen) == 0 || (*seen)[len(*seen)-1].Path != "/runs/"+runID+"/timing" {
			t.Fatalf("timing -r %s: %v, asked %+v; want the run's timeline by its id", ref, err, *seen)
		}
	}
	if _, err := runRef("shop", ""); err == nil || !strings.Contains(err.Error(), "a run is its id or <project>#<number>") {
		t.Fatalf("a run named by neither: %v", err)
	}
}
