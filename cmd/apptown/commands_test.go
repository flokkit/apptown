package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/ids/idstest"
)

// recorded is one request the fake apptownd saw.
type recorded struct {
	Method, Path, Query, Actor, Auth string
	Body                             map[string]any
}

// fakeForge answers every request with reply(method, path) and records it.
// The package client points at it for the test.
func fakeForge(t *testing.T, reply func(method, path string) (int, string)) *[]recorded {
	t.Helper()
	return fakeForgeHeaders(t, func(method, path string) (int, http.Header, string) {
		code, body := reply(method, path)
		return code, nil, body
	})
}

// fakeForgeHeaders is fakeForge whose replies also carry headers, such as a
// refusal's code.
func fakeForgeHeaders(t *testing.T, reply func(method, path string) (int, http.Header, string)) *[]recorded {
	t.Helper()
	var mu sync.Mutex
	seen := &[]recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec := recorded{Method: r.Method, Path: strings.TrimPrefix(r.URL.Path, "/api/v1"), Query: r.URL.RawQuery,
			Actor: r.Header.Get("X-AppTown-Actor"), Auth: r.Header.Get("Authorization")}
		_ = json.Unmarshal(raw, &rec.Body)
		mu.Lock()
		*seen = append(*seen, rec)
		mu.Unlock()
		code, header, body := reply(r.Method, rec.Path)
		for k, v := range header {
			w.Header()[k] = v
		}
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	prev := api
	api = &client{base: srv.URL, actor: "tester", http: srv.Client(), wait: func(time.Duration) {},
		getenv: func(k string) string { return map[string]string{"APPTOWN_TOKEN": "apptown_tok"}[k] }}
	t.Cleanup(func() { api = prev })
	return seen
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// capture returns what f printed on stdout.
func capture(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	f()
	w.Close()
	os.Stdout = prev
	return <-done
}

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	c := lookup(args[0])
	if c == nil {
		t.Fatalf("no command %q", args[0])
	}
	var err error
	out := capture(t, func() { err = c.run(args[1:]) })
	return out, err
}

// Every command is reachable by its names, no name is taken twice, and
// `apptown help` documents every command: the table is the only list.
func TestEveryCommandDispatchesAndIsInHelp(t *testing.T) {
	help := helpText()
	seen := map[string]bool{}
	for _, s := range sections {
		for _, c := range s.commands {
			if c.run == nil || len(c.names) == 0 || c.help == "" {
				t.Fatalf("incomplete command %v", c.names)
			}
			for _, n := range c.names {
				if seen[n] {
					t.Fatalf("%q names two commands", n)
				}
				seen[n] = true
				if got := lookup(n); got == nil || got.help != c.help {
					t.Fatalf("%q does not dispatch to its command", n)
				}
			}
			if !strings.Contains(help, "\n  "+c.names[0]+" ") && !strings.Contains(help, "\n  "+c.names[0]+"\n") {
				t.Fatalf("help does not list %q", c.names[0])
			}
		}
	}
	for _, n := range []string{"help", "-h", "--help"} {
		if lookup(n) != &helpCommand {
			t.Fatalf("%q is not help", n)
		}
	}
	if lookup("nosuch") != nil {
		t.Fatal("an unknown name dispatches")
	}
}

// A record id that is not a number is refused with the argument named, and
// nothing reaches AppTown.
func TestBadIDsFailBeforeAnythingIsSent(t *testing.T) {
	seen := fakeForge(t, func(string, string) (int, string) { return 200, "{}" })
	for _, args := range [][]string{
		{"show", "abc"}, {"follow", "abc"}, {"land", "abc"}, {"retry", "abc"}, {"override", "abc", "why"},
		{"abandon", "abc"}, {"sendback", "abc", "note"}, {"status", "abc"}, {"advance", "abc"},
		{"abandon-run", "abc"}, {"approve", "abc"}, {"reject", "abc"}, {"run-step", "abc"},
		{"attach", "abc", "-k", "log"}, {"trace", "abc"}, {"diff", "abc"}, {"watch", "abc"},
		{"findings", "abc"}, {"wait", "abc"}, {"wait", "-c", "abc"}, {"invoke", "-c", "abc", "-w", "quick-change"},
		{"complaints", "answer", "abc", "-m", "fixed"},
	} {
		_, err := run(t, args...)
		if err == nil || !strings.Contains(err.Error(), `"abc"`) {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if len(*seen) != 0 {
		t.Fatalf("bad ids reached AppTown: %+v", *seen)
	}
}

// Representative commands send the request their docs promise, with the
// caller's identity and credentials, and flags may follow the positionals.
func TestCommandsSendTheirRequests(t *testing.T) {
	seen := fakeForge(t, func(method, path string) (int, string) {
		switch {
		case path == "/projects/shop/changes":
			return 200, `{"change":{"id":"00000000-0005-7000-8000-000000000000","number":5,"branch":"change-00000000-0005-7000-8000-000000000000"},"run":{"id":"00000000-0009-7000-8000-000000000000"}}`
		case path == "/invites":
			return 201, `{"link":"https://forge.test/invite/apptown_invite_x","note":"send it","invite":{"username":"kim","role":"admin","expiresAt":"2026-10-10T00:00:00Z"}}`
		case path == "/password-links":
			return 201, `{"link":"https://forge.test/invite/apptown_invite_y","note":"give it","invite":{"username":"kim","expiresAt":"2026-10-10T00:00:00Z"}}`
		case path == "/steps/00000000-0007-7000-8000-000000000000/approve":
			return 200, `{"name":"accept","status":"pass"}`
		case path == "/steps/00000000-0007-7000-8000-000000000000/question":
			return 200, `{"step":"accept","status":"pending","means":"I read it","landingMeans":[]}`
		case path == "/runs/00000000-0009-7000-8000-000000000000/trace":
			return 200, `{"run":{"id":"00000000-0009-7000-8000-000000000000"}}`
		case path == "/projects/shop/console":
			return 201, `{"session":{"id":"chat-1","changeId":"00000000-0005-7000-8000-000000000000"}}`
		}
		return 200, "[]"
	})
	out, err := run(t, "start", "-p", "shop", "-t", "add the cart", "--ref", "J-1")
	if err != nil || !strings.Contains(out, "change shop#5 opened on change-00000000-0005-7000-8000-000000000000") {
		t.Fatalf("start: %q %v", out, err)
	}
	if _, err := run(t, "approve", "00000000-0007-7000-8000-000000000000", "-m", "looks right", "-y"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "runs", "-p", "shop"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, "trace", "00000000-0009-7000-8000-000000000000", "--json")
	if err != nil || !strings.Contains(out, "\"id\": \"00000000-0009-7000-8000-000000000000\"") {
		t.Fatalf("trace --json after the id: %q %v", out, err)
	}
	if _, err := run(t, "token", "revoke", "cli"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, "invite", "kim", "--role", "admin")
	if err != nil || !strings.Contains(out, "https://forge.test/invite/apptown_invite_x") {
		t.Fatalf("invite prints its link: %q %v", out, err)
	}
	out, err = run(t, "password-link", "kim", "--days", "2")
	if err != nil || !strings.Contains(out, "https://forge.test/invite/apptown_invite_y") || !strings.Contains(out, "password reset for kim") {
		t.Fatalf("password-link prints its link: %q %v", out, err)
	}
	// Task text first, flags after: the flags must not end up in the body.
	if _, err := run(t, "quick", "add", "stats", "-p", "shop", "--no-follow"); err != nil {
		t.Fatal(err)
	}
	// Taking a stopped step to chat, as the change page does.
	if _, err := run(t, "chat", "-p", "shop", "-c", "00000000-0005-7000-8000-000000000000", "-step", "00000000-0007-7000-8000-000000000000"); err != nil {
		t.Fatal(err)
	}
	want := []recorded{
		{Method: "POST", Path: "/projects/shop/changes", Body: map[string]any{"title": "add the cart", "type": "feature", "body": "", "externalRef": "J-1"}},
		{Method: "GET", Path: "/steps/00000000-0007-7000-8000-000000000000/question"},
		{Method: "POST", Path: "/steps/00000000-0007-7000-8000-000000000000/approve", Body: map[string]any{"user": "tester", "decision": "approve", "comment": "looks right"}},
		{Method: "GET", Path: "/runs", Query: "status=running&project=shop"},
		{Method: "GET", Path: "/runs/00000000-0009-7000-8000-000000000000/trace"},
		{Method: "DELETE", Path: "/tokens/cli"},
		{Method: "POST", Path: "/invites", Body: map[string]any{"username": "kim", "role": "admin", "label": "", "days": float64(0)}},
		{Method: "POST", Path: "/password-links", Body: map[string]any{"username": "kim", "days": float64(2)}},
		{Method: "POST", Path: "/projects/shop/changes", Body: map[string]any{"title": "add stats", "body": "add stats", "workflow": "quick-change"}},
		{Method: "POST", Path: "/projects/shop/console", Body: map[string]any{"changeId": "00000000-0005-7000-8000-000000000000", "continueStep": "00000000-0007-7000-8000-000000000000"}},
	}
	if len(*seen) != len(want) {
		t.Fatalf("requests: %+v", *seen)
	}
	for i, w := range want {
		got := (*seen)[i]
		if got.Method != w.Method || got.Path != w.Path || got.Query != w.Query || got.Actor != "tester" || got.Auth != "Bearer apptown_tok" {
			t.Fatalf("request %d: got %+v, want %+v", i, got, w)
		}
		for k, v := range w.Body {
			if got.Body[k] != v {
				t.Fatalf("request %d body %q: got %v, want %v", i, k, got.Body[k], v)
			}
		}
	}
}

// Behind the front door a restarting AppTown answers 503; a read waits and
// asks again. A refusal reads as the server's own sentence.
func TestClientRetriesARestartAndReportsTheServersWords(t *testing.T) {
	calls := 0
	fakeForge(t, func(string, path string) (int, string) {
		calls++
		if path == "/refused" {
			return 409, `{"error":"the change is already landing"}`
		}
		if calls == 1 {
			return 503, ""
		}
		return 200, `{"ok":true}`
	})
	var out struct{ OK bool }
	if err := api.get("/x", &out); err != nil || !out.OK || calls != 2 {
		t.Fatalf("retry after 503: %v %+v after %d calls", err, out, calls)
	}
	if _, err := api.request("POST", "/refused", nil); err == nil || err.Error() != "the change is already landing" {
		t.Fatalf("refusal: %v", err)
	}
}

// A 502, 503 or 504 after a mutation was sent says nothing about whether
// apptownd acted: a proxy answers so when apptownd did the work and the answer
// was lost on the way back. The mutation is never sent again; its outcome
// is reported as unknown, with how to look.
func TestClientNeverResendsAMutationThatGotAnAnswer(t *testing.T) {
	for _, status := range []int{502, 503, 504} {
		effects := 0
		fakeForge(t, func(method, _ string) (int, string) {
			if method == "POST" {
				effects++ // the effect happened; only its answer is lost
			}
			return status, ""
		})
		_, err := api.request("POST", "/projects/shop/changes", map[string]any{"title": "x"})
		var unknown *unknownOutcome
		if !errors.As(err, &unknown) || effects != 1 || !strings.Contains(err.Error(), "the outcome is unknown") {
			t.Fatalf("POST answered %d: sent %d times, err %v; want once and an unknown outcome", status, effects, err)
		}
	}
}

// A mutation is sent again only when it provably never left: the
// connection was never made. A connection lost after the request was
// written is an unknown outcome too.
func TestClientResendsAMutationOnlyWhenItNeverLeft(t *testing.T) {
	sent := 0
	fakeForge(t, func(string, string) (int, string) { sent++; return 200, `{"ok":true}` })
	inner := api.http.Transport
	refused := true
	api.http = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		if refused {
			refused = false
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
		}
		return inner.RoundTrip(r)
	})}
	if _, err := api.request("POST", "/projects/shop/changes", map[string]any{}); err != nil || sent != 1 {
		t.Fatalf("after a refused dial: %v, sent %d", err, sent)
	}
	api.http = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		sent++ // written, then the connection broke
		return nil, io.ErrUnexpectedEOF
	})}
	sent = 0
	_, err := api.request("POST", "/projects/shop/changes", map[string]any{})
	if unknown := (*unknownOutcome)(nil); !errors.As(err, &unknown) || sent != 1 {
		t.Fatalf("after a broken connection: %v, sent %d; want one send and an unknown outcome (exit 4)", err, sent)
	}
	// A request that never left is an ordinary failure, not an unknown one.
	api.http = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
	})}
	_, err = api.request("POST", "/projects/shop/changes", map[string]any{})
	if unknown := (*unknownOutcome)(nil); err == nil || errors.As(err, &unknown) {
		t.Fatalf("a mutation that never left: %v; want an ordinary error", err)
	}
}

// An AppTown that reads a change and then drops the connection, or whose
// answer breaks off part way, may have acted: the change's outcome is
// unknown (exit 4), never an ordinary failure (exit 1). A read under the
// same failure is asked again.
func TestAChangeWhoseAnswerIsLostIsAnUnknownOutcome(t *testing.T) {
	for _, tc := range []struct {
		name  string
		serve func(w http.ResponseWriter)
	}{
		{"dropped", func(w http.ResponseWriter) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
		}},
		{"cut short", func(w http.ResponseWriter) {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"change":`)
			w.(http.Flusher).Flush()
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				conn.Close()
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Counted on the server's goroutines, read on the test's: a
			// dropped connection orders them for the client, not for the
			// race detector.
			var posts, gets atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(r.Body)
				if r.Method == "POST" {
					posts.Add(1)
				} else if gets.Add(1) > 1 {
					_, _ = io.WriteString(w, `{"ok":true}`)
					return
				}
				tc.serve(w)
			}))
			t.Cleanup(srv.Close)
			prev := api
			api = &client{base: srv.URL, actor: "tester", http: srv.Client(), wait: func(time.Duration) {}, getenv: func(string) string { return "" }}
			t.Cleanup(func() { api = prev })
			_, err := api.request("POST", "/projects/shop/changes", map[string]any{"title": "x"})
			if unknown := (*unknownOutcome)(nil); !errors.As(err, &unknown) || posts.Load() != 1 {
				t.Fatalf("a change whose answer was lost: %v after %d sends; want one send and an unknown outcome", err, posts.Load())
			}
			if !strings.Contains(err.Error(), "the connection broke after the request was sent") {
				t.Fatalf("the sentence names the broken connection: %v", err)
			}
			if tc.name == "dropped" {
				var out struct{ OK bool }
				if err := api.get("/x", &out); err != nil || !out.OK {
					t.Fatalf("a read is asked again: %v %+v", err, out)
				}
			}
		})
	}
}

func TestParseArgsTakesFlagsAnywhere(t *testing.T) {
	fs := newFlags("t")
	p := fs.String("p", "", "")
	y := fs.Bool("y", false, "")
	pos := parseArgs(fs, []string{"58", "-p", "shop", "note", "-y", "--", "-literal"})
	if *p != "shop" || !*y || strings.Join(pos, "|") != "58|note|-literal" {
		t.Fatalf("p=%q y=%v pos=%v", *p, *y, pos)
	}
	if id, err := parseID(idstest.N(58).String(), "change id"); err != nil || id != idstest.N(58) {
		t.Fatalf("parseID: %d %v", id, err)
	}
	for _, bad := range []string{"", "0", "-3", "58a"} {
		if _, err := parseID(bad, "run id"); err == nil {
			t.Fatalf("parseID accepted %q", bad)
		}
	}
}

// A trace names the run's change. The CLI once read a field apptownd did not
// send and never showed it.
func TestTraceNamesTheChange(t *testing.T) {
	fakeForge(t, func(method, path string) (int, string) {
		return 200, `{"run":{"id":"00000000-0007-7000-8000-000000000000","status":"done","workflowName":"quick-change"},"projectSlug":"shop",
			"change":{"id":"00000000-000c-7000-8000-000000000000","number":12,"title":"add the cart"},"events":[]}`
	})
	out := capture(t, func() {
		if err := cmdTrace([]string{"00000000-0007-7000-8000-000000000000"}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, `change shop#12 "add the cart"`) {
		t.Fatalf("trace output: %q", out)
	}
}

// A note written as a markdown bullet ("- fix the tests") is text, as agents
// write them; every other dash-shaped argument is still a flag, so a mistyped
// flag is an error rather than part of the task.
func TestMarkdownBulletsAreTextAndMistypedFlagsAreErrors(t *testing.T) {
	fs := newFlags("t")
	p := fs.String("p", "", "")
	y := fs.Bool("y", false, "")
	pos := parseArgs(fs, []string{"12", "- fix the tests", "-p", "shop", "-y"})
	if *p != "shop" || !*y || strings.Join(pos, "|") != "12|- fix the tests" {
		t.Fatalf("p=%q y=%v pos=%v", *p, *y, pos)
	}
	// "--" ends flags: what follows is text however it is spelled, and flags
	// before it still apply.
	q := newFlags("t")
	qp := q.String("p", "", "")
	pos = parseArgs(q, []string{"-p", "shop", "--", "-p is literal", "--projet"})
	if *qp != "shop" || strings.Join(pos, "|") != "-p is literal|--projet" {
		t.Fatalf("after --: p=%q pos=%v", *qp, pos)
	}
	var said bytes.Buffer
	strict := flag.NewFlagSet("t", flag.ContinueOnError)
	strict.SetOutput(&said)
	strict.String("project", "", "")
	pos = parseArgs(strict, []string{"add stats", "--projet", "x"})
	if !strings.Contains(said.String(), "flag provided but not defined: -projet") {
		t.Fatalf("a mistyped flag must be refused, got %q with %v", said.String(), pos)
	}
}

// An approval's sentence is shown before the decision is sent, as the web's
// decision form shows it; for a change's acceptance that includes what the
// landing would ask. Without a terminal to ask at, -y says it was read.
func TestApproveShowsWhatItAgreesToBeforeSending(t *testing.T) {
	seen := fakeForge(t, func(method, path string) (int, string) {
		if path == "/steps/00000000-0007-7000-8000-000000000000/question" {
			return 200, `{"step":"accept","status":"pending","means":"I read it","landingMeans":["it merges and ships on my say-so"]}`
		}
		return 200, `{"name":"accept","status":"pass"}`
	})
	out, err := run(t, "approve", "00000000-0007-7000-8000-000000000000")
	if err == nil || !strings.Contains(err.Error(), "rerun with -y") {
		t.Fatalf("approved without a terminal or -y: %v", err)
	}
	for _, want := range []string{"Approving means: “I read it”", "this also approves it: “it merges and ships on my say-so”"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	for _, r := range *seen {
		if r.Method == "POST" {
			t.Fatalf("a decision was sent before it was confirmed: %+v", r)
		}
	}
	if _, err := run(t, "approve", "00000000-0007-7000-8000-000000000000", "-y"); err != nil {
		t.Fatal(err)
	}
	last := (*seen)[len(*seen)-1]
	if last.Method != "POST" || last.Path != "/steps/00000000-0007-7000-8000-000000000000/approve" {
		t.Fatalf("approve -y: %+v", last)
	}
	// What it showed of the landing goes back, so only those words are agreed to.
	if also, _ := last.Body["alsoApproves"].([]any); len(also) != 1 || also[0] != "it merges and ships on my say-so" {
		t.Fatalf("approve -y sent: %+v", last.Body)
	}
}

// Accepting the work starts the landing on AppTown. The loop follows that
// landing instead of asking to start one, which AppTown refuses.
func TestLoopFollowsTheLandingThatAcceptanceStarted(t *testing.T) {
	var landingSeen bool
	seen := fakeForge(t, func(method, path string) (int, string) {
		switch path {
		case "/runs/00000000-000a-7000-8000-000000000000":
			return 200, `{"run":{"id":"00000000-000a-7000-8000-000000000000","status":"done","currentStage":"accept"},"steps":[]}`
		case "/runs/00000000-000b-7000-8000-000000000000":
			landingSeen = true
			return 200, `{"run":{"id":"00000000-000b-7000-8000-000000000000","status":"done","currentStage":"ship"},"steps":[]}`
		case "/changes/00000000-0005-7000-8000-000000000000":
			if landingSeen {
				return 200, `{"change":{"id":"00000000-0005-7000-8000-000000000000","state":"shipped"},"projectSlug":"shop","runs":[{"id":"00000000-000b-7000-8000-000000000000","status":"done","role":"landing"},{"id":"00000000-000a-7000-8000-000000000000","status":"done","role":"working"}]}`
			}
			return 200, `{"change":{"id":"00000000-0005-7000-8000-000000000000","state":"landing"},"projectSlug":"shop","runs":[{"id":"00000000-000b-7000-8000-000000000000","status":"running","role":"landing"},{"id":"00000000-000a-7000-8000-000000000000","status":"done","role":"working"}]}`
		}
		return 200, `[]`
	})
	if err := followChange(idstest.N(5), idstest.N(10), "shop", true); err != nil {
		t.Fatal(err)
	}
	if !landingSeen {
		t.Fatal("the loop did not follow the landing the acceptance started")
	}
	for _, r := range *seen {
		if r.Method == "POST" && strings.HasSuffix(r.Path, "/land") {
			t.Fatalf("the loop asked to start a second landing: %+v", r)
		}
	}
}

// A landing the acceptance started that has already ended (catch-up with
// main ended it) is reported, not followed by a second landing: one
// follow -y started landings 132 and 133 three seconds apart (sweep 7, C3).
func TestLoopDoesNotStartASecondLandingAfterTheAcceptancesEnded(t *testing.T) {
	pollEvery = 0
	seen := fakeForge(t, func(method, path string) (int, string) {
		switch path {
		case "/runs/00000000-0083-7000-8000-000000000000":
			return 200, `{"run":{"id":"00000000-0083-7000-8000-000000000000","status":"done","currentStage":"build"},"steps":[]}`
		case "/changes/00000000-003b-7000-8000-000000000000":
			return 200, `{"change":{"id":"00000000-003b-7000-8000-000000000000","number":59,"state":"in_progress"},"projectSlug":"shop","runs":[{"id":"00000000-0084-7000-8000-000000000000","status":"abandoned","role":"landing"},{"id":"00000000-0083-7000-8000-000000000000","status":"done","role":"working"}]}`
		}
		return 200, `[]`
	})
	out := capture(t, func() {
		if err := followChange(idstest.N(59), idstest.N(131), "shop", true); err != nil {
			t.Error(err)
		}
	})
	for _, r := range *seen {
		if r.Method == "POST" {
			t.Fatalf("the loop sent %s %s after the acceptance's landing ended", r.Method, r.Path)
		}
	}
	if !strings.Contains(out, "landing ended abandoned") || !strings.Contains(out, "apptown land shop#59") {
		t.Fatalf("the ended landing was not reported:\n%s", out)
	}
}

// Accepted work whose landing AppTown has queued is waited for, by the
// forge's stated fact, and the landing it starts is followed, not landed a
// second time.
func TestLoopWaitsForTheLandingTheForgeQueued(t *testing.T) {
	pollEvery = 0
	reads := 0
	followed := false
	seen := fakeForge(t, func(method, path string) (int, string) {
		switch path {
		case "/runs/00000000-0083-7000-8000-000000000000":
			reads++
			if reads < 4 {
				return 200, `{"run":{"id":"00000000-0083-7000-8000-000000000000","status":"done","currentStage":"build"},"landingQueued":true,"steps":[]}`
			}
			return 200, `{"run":{"id":"00000000-0083-7000-8000-000000000000","status":"done","currentStage":"build"},"steps":[]}`
		case "/runs/00000000-0084-7000-8000-000000000000":
			followed = true
			return 200, `{"run":{"id":"00000000-0084-7000-8000-000000000000","status":"done","currentStage":"ship"},"steps":[]}`
		case "/changes/00000000-003b-7000-8000-000000000000":
			if reads < 3 {
				return 200, `{"change":{"id":"00000000-003b-7000-8000-000000000000","state":"in_progress"},"projectSlug":"shop","runs":[{"id":"00000000-0083-7000-8000-000000000000","status":"done","role":"working"}]}`
			}
			return 200, `{"change":{"id":"00000000-003b-7000-8000-000000000000","state":"landing"},"projectSlug":"shop","runs":[{"id":"00000000-0084-7000-8000-000000000000","status":"running","role":"landing"},{"id":"00000000-0083-7000-8000-000000000000","status":"done","role":"working"}]}`
		}
		return 200, `[]`
	})
	capture(t, func() {
		if err := followChange(idstest.N(59), idstest.N(131), "shop", true); err != nil {
			t.Error(err)
		}
	})
	for _, r := range *seen {
		if r.Method == "POST" {
			t.Fatalf("the loop sent %s %s while AppTown's landing was queued", r.Method, r.Path)
		}
	}
	if !followed {
		t.Fatal("the loop did not wait for the queued landing and follow it")
	}
}

// Finished work is offered a landing only when AppTown offers one (its
// land act); otherwise the loop says so and sends nothing.
func TestLoopOffersALandingOnlyWhenTheForgeDoes(t *testing.T) {
	pollEvery = 0
	for _, offered := range []bool{true, false} {
		next := `[]`
		if offered {
			next = `[{"act":"land","stepId":"00000000-0000-7000-8000-000000000000","step":""}]`
		}
		seen := fakeForge(t, func(method, path string) (int, string) {
			switch {
			case path == "/runs/00000000-0083-7000-8000-000000000000":
				return 200, `{"run":{"id":"00000000-0083-7000-8000-000000000000","status":"done","currentStage":"build"},"steps":[]}`
			case path == "/changes/00000000-003b-7000-8000-000000000000":
				return 200, `{"change":{"id":"00000000-003b-7000-8000-000000000000","state":"in_progress"},"projectSlug":"shop","next":` + next + `,"runs":[{"id":"00000000-0083-7000-8000-000000000000","status":"done","role":"working"}]}`
			case method == "POST" && path == "/changes/00000000-003b-7000-8000-000000000000/land":
				return 409, `{"error":"stop here"}`
			}
			return 200, `{}`
		})
		out := capture(t, func() { _ = followChange(idstest.N(59), idstest.N(131), "shop", true) })
		landed := false
		for _, r := range *seen {
			landed = landed || r.Method == "POST" && r.Path == "/changes/00000000-003b-7000-8000-000000000000/land"
		}
		if landed != offered || !offered && !strings.Contains(out, "offers no landing") {
			t.Fatalf("offered=%v: landed=%v\n%s", offered, landed, out)
		}
	}
}

// A landing that stopped short of shipping says where it left the change: a
// blocked one keeps it landing, so it never claims it is back in progress;
// an ended one put it back in progress.
func TestALandingThatStoppedSaysWhereTheChangeIs(t *testing.T) {
	blocked := capture(t, func() { printLandingStopped("shop#7", "blocked") })
	if strings.Contains(blocked, "back in progress") || !strings.Contains(blocked, "still landing") || !strings.Contains(blocked, "apptown sendback shop#7") {
		t.Errorf("blocked: %q", blocked)
	}
	ended := capture(t, func() { printLandingStopped("shop#7", "abandoned") })
	if !strings.Contains(ended, "back in progress") {
		t.Errorf("ended: %q", ended)
	}
}

// Opening a project or a run ends with the next useful command in the
// loop's own words: a first change after `new`, following the change
// after `invoke` (they once pointed at invoke flags and a run's status).
func TestNewAndInvokeEndWithTheLoopsNextCommand(t *testing.T) {
	seen := fakeForge(t, func(_, path string) (int, string) {
		switch path {
		case "/projects":
			return 200, `{"slug":"shop"}`
		case "/projects/shop/repo":
			return 200, `{"defaultBranch":"main"}`
		case "/projects/shop/pack":
			return 200, `{"pack":"go-small","guidanceImports":22,"workflowsInstalled":["quick-change"]}`
		case "/projects/shop/changes":
			return 200, `{"change":{"id":"00000000-0005-7000-8000-000000000000","number":5},"run":{"id":"00000000-0009-7000-8000-000000000000","currentStage":"build"}}`
		case "/changes/shop#5":
			return 200, `{"change":{"id":"00000000-0005-7000-8000-000000000000","number":5},"projectSlug":"shop"}`
		case "/changes/00000000-0005-7000-8000-000000000000/runs":
			return 200, `{"run":{"id":"00000000-000a-7000-8000-000000000000","currentStage":"build"}}`
		}
		return 404, `{"error":"no"}`
	})
	out, err := run(t, "new", "shop", "--pack", "go-small")
	if err != nil || !strings.Contains(out, `apptown quick -p shop "<what to change>"`) || strings.Contains(out, "apptown invoke") || strings.Contains(out, "guidance imports") {
		t.Fatalf("new: %v\n%s", err, out)
	}
	// The creation names the pack, so AppTown refuses one it does not
	// offer before making the project.
	if create := (*seen)[0]; create.Path != "/projects" || create.Body["pack"] != "go-small" {
		t.Fatalf("the creation did not carry the pack: %+v", create)
	}
	for _, args := range [][]string{{"invoke", "-p", "shop", "-w", "quick-change", "-t", "x"}, {"invoke", "-c", "shop#5", "-w", "quick-change"}} {
		out, err := run(t, args...)
		if err != nil || !strings.Contains(out, "next: apptown follow shop#5") {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	// Inside a project a bare number is that project's change.
	if out, err := run(t, "invoke", "-p", "shop", "-c", "5", "-w", "quick-change"); err != nil || !strings.Contains(out, "next: apptown follow 5") {
		t.Fatalf("a bare number in a project: %v\n%s", err, out)
	}
}

// typing answers the CLI's questions with the given lines for one test.
func typing(t *testing.T, lines string) {
	t.Helper()
	prev := stdin
	stdin = bufio.NewReader(strings.NewReader(lines))
	t.Cleanup(func() { stdin = prev })
}

// apptown delete sends nothing until the slug is typed back, then sends the
// confirmation AppTown requires and follows the deletion until the
// project is deleted and kept.
func TestDeleteAsksForTheSlugAndFollowsTheDeletion(t *testing.T) {
	pollEvery = 0
	reads := 0
	seen := fakeForge(t, func(method, path string) (int, string) {
		switch {
		case path == "/projects/shop/deployments":
			return 200, `[{"name":"main"}]`
		case method == "DELETE" && path == "/projects/shop":
			return 202, `{"projectId":"00000000-0001-7000-8000-000000000000","slug":"shop","state":"deleting","remaining":["main"]}`
		case path == "/projects/shop/deletion":
			reads++
			if reads < 3 {
				return 200, `{"projectId":"00000000-0001-7000-8000-000000000000","slug":"shop","state":"deleting","remaining":["main"]}`
			}
			return 200, `{"projectId":"00000000-0001-7000-8000-000000000000","slug":"shop","state":"deleted","keptUntil":"2026-11-08T00:00:00Z"}`
		}
		return 404, `{"error":"no"}`
	})
	typing(t, "shpo\n")
	out, err := run(t, "delete", "shop")
	if err == nil || !strings.Contains(out, "its main deployment") {
		t.Fatalf("a mistyped slug: %v\n%s", err, out)
	}
	for _, r := range *seen {
		if r.Method == "DELETE" {
			t.Fatalf("deleted on a mistyped slug: %+v", r)
		}
	}
	typing(t, "shop\n")
	out, err = run(t, "delete", "shop")
	if err != nil || !strings.Contains(out, "waiting on: main") || !strings.Contains(out, "kept until 2026-11-08") {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	for _, r := range *seen {
		if r.Method == "DELETE" && r.Body["confirm"] != "shop" {
			t.Fatalf("the delete did not carry the confirmation: %+v", r)
		}
	}
}

// A deletion that stops for a person is told as AppTown says it, with
// how to go on, and exits non-zero.
func TestDeleteThatStopsSaysWhyAndHowToGoOn(t *testing.T) {
	pollEvery = 0
	stalled := "review could not be taken down: the final backup fell short"
	fakeForge(t, func(method, path string) (int, string) {
		switch {
		case path == "/projects/shop/deployments":
			return 200, `[{"name":"main"},{"name":"review"}]`
		case method == "DELETE" && path == "/projects/shop":
			return 202, `{"projectId":"00000000-0001-7000-8000-000000000000","slug":"shop","state":"deleting"}`
		case path == "/projects/shop/deletion":
			return 200, `{"projectId":"00000000-0001-7000-8000-000000000000","slug":"shop","state":"deleting","stalled":"` + stalled + `","remaining":["review","main"]}`
		}
		return 404, `{"error":"no"}`
	})
	typing(t, "shop\n")
	if _, err := run(t, "delete", "shop"); err == nil || !strings.Contains(err.Error(), stalled) || !strings.Contains(err.Error(), "apptown delete shop again") {
		t.Fatalf("stalled delete: %v", err)
	}
}

// apptown review starts a review of the project named (never the whole
// estate by accident) and follows it to a summary of what does not conform.
func TestReviewStartsAScopedReviewAndSummarisesIt(t *testing.T) {
	pollEvery = 0
	reads := 0
	seen := fakeForge(t, func(method, path string) (int, string) {
		switch {
		case method == "POST" && path == "/conformance/review":
			return 202, `{"id":"00000000-0011-7000-8000-000000000000","number":17,"status":"running"}`
		case path == "/conformance/reviews/00000000-0011-7000-8000-000000000000":
			reads++
			if reads < 3 {
				return 200, `{"review":{"status":"running","projectsDone":0,"projectsTotal":1,"currentProject":"shop"},"verdicts":[]}`
			}
			return 200, `{"review":{"status":"done","projectsDone":1,"projectsTotal":1},"verdicts":[
				{"projectSlug":"shop","check":"readme-plain","state":"does-not-conform","severity":"low","detail":"jargon"},
				{"projectSlug":"shop","check":"healthz","state":"conforms"},{"projectSlug":"shop","check":"sbom","state":"conforms"}]}`
		}
		return 404, `{"error":"no"}`
	})
	out, err := run(t, "review", "-p", "shop")
	if err != nil || !strings.Contains(out, "review 17 of shop") || !strings.Contains(out, "now shop") ||
		!strings.Contains(out, "2 conforms, 1 does-not-conform") || !strings.Contains(out, "shop · readme-plain (low)") {
		t.Fatalf("review: %v\n%s", err, out)
	}
	start := (*seen)[0]
	if projects, _ := start.Body["projects"].([]any); len(projects) != 1 || projects[0] != "shop" || start.Body["project"] != nil {
		t.Fatalf("the review was not scoped to shop: %+v", start.Body)
	}
	if _, err := run(t, "review", "--no-follow"); err != nil {
		t.Fatal(err)
	}
	if every := (*seen)[len(*seen)-1]; every.Path != "/conformance/review" || every.Body["projects"] != nil {
		t.Fatalf("a whole-estate review named projects: %+v", every)
	}
}

// apptown whatif asks for a published version on the projects -p names and
// prints every result class but unchanged, with the reason a row was not
// judged; an interrupted what-if is a failure, not an answer.
func TestWhatIfJudgesAVersionOnNamedProjects(t *testing.T) {
	pollEvery = 0
	state, done := "done", 2
	seen := fakeForge(t, func(method, path string) (int, string) {
		switch {
		case method == "POST" && path == "/conformance/what-if":
			return 202, `{"id":"00000000-0009-7000-8000-000000000000"}`
		case path == "/conformance/what-if/00000000-0009-7000-8000-000000000000":
			return 200, `{"state":"` + state + `","detail":"AppTown restarted","projectsDone":` + strconv.Itoa(done) + `,"projectsTotal":2,"rows":[
				{"projectSlug":"hello-rust","block":"lang/selection/rust","check":"runtime-stage-not-build-image","result":"would-fail","detail":"the final stage runs FROM rust:1.84.1"},
				{"projectSlug":"shop","block":"lang/selection/rust","check":"runtime-stage-not-build-image","result":"unchanged"},
				{"projectSlug":"shop","block":"build/container-image","check":"dockerfile-lint","result":"not-assessed","reason":"no-image","detail":"the app ships none"},
				{"projectSlug":"shop","block":"old/block","check":"gone","result":"no-longer-required"}]}`
		}
		return 404, `{"error":"no"}`
	})
	out, err := run(t, "whatif", "rust-service", "7", "-p", "hello-rust, shop")
	if err != nil || !strings.Contains(out, "1 no-longer-required, 1 not-assessed, 1 unchanged, 1 would-fail") ||
		!strings.Contains(out, "would-fail · hello-rust · runtime-stage-not-build-image") ||
		!strings.Contains(out, "no-image the app ships none") || strings.Contains(out, "unchanged · shop") {
		t.Fatalf("whatif: %v\n%s", err, out)
	}
	start := (*seen)[0]
	if projects, _ := start.Body["projects"].([]any); start.Body["pack"] != "rust-service" || start.Body["version"] != float64(7) ||
		len(projects) != 2 || projects[0] != "hello-rust" || projects[1] != "shop" || start.Body["rubrics"] != nil {
		t.Fatalf("the what-if asked: %+v", start.Body)
	}
	state, done = "interrupted", 1
	out, err = run(t, "whatif", "rust-service", "7", "-p", "hello-rust,shop")
	if err == nil || !strings.Contains(err.Error(), "ended interrupted — AppTown restarted") ||
		!strings.Contains(out, "partial: 1 of 2 projects judged") || strings.Contains(out, "✓") {
		t.Fatalf("an interrupted what-if: %v\n%s", err, out)
	}
	for _, bad := range [][]string{{"whatif", "rust-service", "0", "-p", "shop"}, {"whatif", "rust-service", "-p", "shop"}, {"whatif", "show"}} {
		if _, err := run(t, bad...); err == nil {
			t.Fatalf("%v was accepted", bad)
		}
	}
}

// apptown whatif with no -p asks for the eligible projects (no projects in
// the body, never an empty list), -p names each project, and following it
// prints one line per project with the checks behind its outcome. A
// what-if that was interrupted exits non-zero; one that finished exits zero
// whatever it found. Kills: the default sending an empty list; -p keeping
// only one project; an interrupted what-if reported as success; a
// project's failures or reasons left out.
func TestWhatIfAsksForTheEligibleProjectsAndSaysWhatEachWouldChange(t *testing.T) {
	pollEvery = 0
	reads := 0
	state := "done"
	seen := fakeForge(t, func(method, path string) (int, string) {
		switch {
		case method == "POST" && path == "/conformance/what-if":
			return 202, `{"id":"00000000-0004-7000-8000-000000000000"}`
		case path == "/conformance/what-if/00000000-0004-7000-8000-000000000000":
			reads++
			if reads < 2 {
				return 200, `{"id":"00000000-0004-7000-8000-000000000000","pack":"team","version":2,"state":"running","projectsDone":0,"projectsTotal":2,"projects":[]}`
			}
			return 200, `{"id":"00000000-0004-7000-8000-000000000000","pack":"team","version":2,"state":"` + state + `","projectsDone":2,"projectsTotal":2,"projects":[
				{"projectSlug":"shop","sha":"0123456789abcdef","outcome":"new-failures","newFailures":[{"block":"docs","check":"readme"},{"block":"deps","check":"audit","exceptedToday":true}],"notAssessed":[]},
				{"projectSlug":"till","sha":"fedcba9876543210","outcome":"could-not-assess","newFailures":[],"notAssessed":[{"block":"img","check":"scan","reason":"no-image","why":"no image of the app at this commit could be used"}],"fixed":1}]}`
		}
		return 404, `{"error":"no"}`
	})
	out, err := run(t, "whatif", "team", "2")
	if err != nil || !strings.Contains(out, "every project on an earlier version") || !strings.Contains(out, "1 new failures · 1 could not assess") ||
		!strings.Contains(out, "docs · readme; deps · audit (excepted today)") || !strings.Contains(out, "img · scan: no image of the app at this commit could be used; 1 fixed") {
		t.Fatalf("what-if: %v\n%s", err, out)
	}
	if start := (*seen)[0]; start.Body["projects"] != nil || start.Body["pack"] != "team" || start.Body["version"] != float64(2) {
		t.Fatalf("the default named projects: %+v", start.Body)
	}
	if _, err := run(t, "whatif", "team", "2", "-p", "shop,till", "--no-follow"); err != nil {
		t.Fatal(err)
	}
	if named, _ := (*seen)[len(*seen)-1].Body["projects"].([]any); len(named) != 2 || named[0] != "shop" || named[1] != "till" {
		t.Fatalf("-p named each project: %+v", named)
	}
	state = "interrupted"
	if out, err := run(t, "whatif", "show", "00000000-0004-7000-8000-000000000000"); err == nil || !strings.Contains(err.Error(), "ended interrupted") || !strings.Contains(out, "partial") {
		t.Fatalf("an interrupted what-if: %v\n%s", err, out)
	}
}

// apptown whatif judges model rubrics only with --rubrics and a spend limit
// in dollars, sent as micro-dollars; a limit that is not above zero is
// refused before anything is sent. It says the limit before it starts and
// what was spent at the end ("at least" when some spend is unknown), and
// says so when rubrics were not included. Kills: rubrics sent without the
// flag; the limit sent in dollars or rounded away; a zero limit sent;
// unknown spend shown as a known total.
func TestWhatIfIncludesRubricsOnlyUnderASpendLimit(t *testing.T) {
	pollEvery = 0
	rubrics, refuse := true, false
	seen := fakeForge(t, func(method, path string) (int, string) {
		switch {
		case method == "POST" && path == "/conformance/what-if" && refuse:
			return 503, `{"error":"the queue is unavailable"}`
		case method == "POST" && path == "/conformance/what-if":
			return 202, `{"id":"00000000-0005-7000-8000-000000000000"}`
		case path == "/conformance/what-if/00000000-0005-7000-8000-000000000000":
			return 200, `{"id":"00000000-0005-7000-8000-000000000000","state":"done","projectsDone":1,"projectsTotal":1,"rubrics":` + strconv.FormatBool(rubrics) + `,"spendLimitMicros":2500000,"costMicros":800000,"incomplete":1,"rows":[],"projects":[]}`
		}
		return 404, `{"error":"no"}`
	})
	out, err := run(t, "whatif", "build-pack", "3", "--rubrics", "2.5")
	if err != nil || !strings.Contains(out, "sent while spend is under $2.50") || !strings.Contains(out, "spent at least $0.80 of $2.50 (some spend unknown)") {
		t.Fatalf("whatif --rubrics: %v\n%s", err, out)
	}
	if b := (*seen)[0].Body; b["rubrics"] != true || b["spendLimitMicros"] != float64(2500000) {
		t.Fatalf("the rubrics and limit asked: %+v", b)
	}
	rubrics = false
	out, err = run(t, "whatif", "build-pack", "3")
	if err != nil || !strings.Contains(out, "model rubrics not included") {
		t.Fatalf("whatif without rubrics: %v\n%s", err, out)
	}
	if b := (*seen)[len(*seen)-2].Body; b["rubrics"] != nil || b["spendLimitMicros"] != nil {
		t.Fatalf("rubrics asked without the flag: %+v", b)
	}
	// The limit's rule is said before the what-if is asked for, not after:
	// a start AppTown refuses still printed it.
	refuse = true
	if out, err := run(t, "whatif", "build-pack", "3", "--rubrics", "2.5"); err == nil || !strings.Contains(out, "sent while spend is under $2.50") {
		t.Fatalf("the limit was not stated before starting: %v\n%s", err, out)
	}
	refuse = false
	sent := len(*seen)
	for _, bad := range []string{"0", "-1", "x", "0.0000001"} {
		if _, err := run(t, "whatif", "build-pack", "3", "--rubrics", bad); err == nil {
			t.Fatalf("--rubrics %s was accepted", bad)
		}
	}
	if len(*seen) != sent {
		t.Fatalf("a refused limit sent a request: %+v", (*seen)[sent:])
	}
}

// apptown whatif --own-packs newest sends each project's own pack at the
// newest version AppTown offers, as explicit versions (so the record pins
// them); --also sends the versions named; both at once is refused before
// anything is asked.
func TestWhatIfMovesOtherPacksWithTheNamedOne(t *testing.T) {
	pollEvery = 0
	seen := fakeForge(t, func(method, path string) (int, string) {
		switch {
		case method == "GET" && path == "/conformance/what-if":
			return 200, `{"whatIf":null,"eligible":[],"ownPacksNewest":[{"pack":"django-htmx","version":43},{"pack":"java-spring","version":38}]}`
		case method == "POST" && path == "/conformance/what-if":
			return 202, `{"id":"00000000-0006-7000-8000-000000000000"}`
		case path == "/conformance/what-if/00000000-0006-7000-8000-000000000000":
			return 200, `{"id":"00000000-0006-7000-8000-000000000000","state":"done","rows":[],"projects":[]}`
		}
		return 404, `{"error":"no"}`
	})
	out, err := run(t, "whatif", "apptown", "35", "--own-packs", "newest", "--no-follow")
	if err != nil || !strings.Contains(out, "with, for projects already on them: django-htmx v43, java-spring v38") {
		t.Fatalf("--own-packs newest: %v\n%s", err, out)
	}
	last := func() map[string]any { return (*seen)[len(*seen)-1].Body }
	if got := fmt.Sprint(last()["also"]); got != "[map[pack:django-htmx version:43] map[pack:java-spring version:38]]" {
		t.Fatalf("the versions sent: %s", got)
	}
	if _, err := run(t, "whatif", "apptown", "35", "--also", "rails@38", "--no-follow"); err != nil || fmt.Sprint(last()["also"]) != "[map[pack:rails version:38]]" {
		t.Fatalf("--also: %v %+v", err, last())
	}
	sent := len(*seen)
	for _, args := range [][]string{{"--also", "rails@38", "--own-packs", "newest"}, {"--own-packs", "latest"}, {"--also", "rails"}} {
		if _, err := run(t, append([]string{"whatif", "apptown", "35"}, args...)...); err == nil {
			t.Fatalf("%v was accepted", args)
		}
	}
	if len(*seen) != sent {
		t.Fatalf("a refused request was sent: %+v", (*seen)[sent:])
	}
}

// apptown whatif show of a what-if still queued or running says so, how far
// it has got, and that unreached projects are not yet judged: never
// "partial", "before it ended" or "not evaluated", which say it stopped
// (found live on what-ifs 7–10). Kills: the in-progress states printed as
// an end; unreached projects printed as not evaluated while it runs.
func TestWhatIfShowSaysQueuedAndRunningAsTheyAre(t *testing.T) {
	state := "queued"
	fakeForge(t, func(method, path string) (int, string) {
		if path != "/conformance/what-if/00000000-0007-7000-8000-000000000000" {
			return 404, `{"error":"no"}`
		}
		if state == "queued" {
			return 200, `{"id":"00000000-0007-7000-8000-000000000000","state":"queued","projectsDone":0,"projectsTotal":0,"rows":[],"projects":[
				{"projectSlug":"a","outcome":"not-yet-judged","newFailures":[],"notAssessed":[]},{"projectSlug":"b","outcome":"not-yet-judged","newFailures":[],"notAssessed":[]}]}`
		}
		return 200, `{"id":"00000000-0007-7000-8000-000000000000","state":"running","projectsDone":1,"projectsTotal":3,"rows":[{"projectSlug":"a","block":"x","check":"y","result":"unchanged"}],"projects":[
			{"projectSlug":"a","sha":"0123456789abcdef","outcome":"no-new-failures","newFailures":[],"notAssessed":[]},
			{"projectSlug":"b","outcome":"not-yet-judged","newFailures":[],"notAssessed":[]},{"projectSlug":"c","outcome":"not-yet-judged","newFailures":[],"notAssessed":[]}]}`
	})
	for _, c := range []struct{ state, says string }{{"queued", "queued: waiting behind another review or what-if"}, {"running", "running: 1 of 3 projects judged so far"}} {
		state = c.state
		out, err := run(t, "whatif", "show", "00000000-0007-7000-8000-000000000000")
		if err != nil || !strings.Contains(out, c.says) || !strings.Contains(out, "not yet judged") {
			t.Fatalf("%s: %v\n%s", c.state, err, out)
		}
		for _, ended := range []string{"partial", "before it ended", "not evaluated"} {
			if strings.Contains(out, ended) {
				t.Fatalf("a %s what-if reads as ended (%q):\n%s", c.state, ended, out)
			}
		}
	}
}

// apptown whatif adopt asks each named project's adopt door in turn with the
// what-if's number, goes on after a refusal or an unknown answer, says
// which each was, and exits zero only when every project opened a Change.
// -p is required: there is no "all". Kills: stopping at the first refusal;
// an unknown answer printed as opened or refused; success reported when
// one did not open; AppTown asked with values of the CLI's own.
func TestWhatIfAdoptOpensAChangePerNamedProject(t *testing.T) {
	refuseLedger := true
	seen := fakeForge(t, func(method, path string) (int, string) {
		switch {
		case method == "POST" && path == "/projects/ledger/adoption/adopt" && refuseLedger:
			return 409, `{"error":"the project is on revision 3, not 2 — prepare again"}`
		case method == "POST" && path == "/projects/till/adoption/adopt":
			return 503, `{"error":"database unavailable"}`
		case method == "POST" && strings.HasSuffix(path, "/adoption/adopt"):
			return 200, `{"changeId":"00000000-0029-7000-8000-000000000000","candidate":{},"workflow":"quick-change"}`
		}
		return 404, `{"error":"no"}`
	})
	out, err := run(t, "whatif", "adopt", "00000000-0007-7000-8000-000000000000", "-p", "ledger,till,shop")
	if err == nil || !strings.Contains(err.Error(), "1 of 3 projects opened") {
		t.Fatalf("mixed outcomes exit non-zero: %v\n%s", err, out)
	}
	for _, want := range []string{"ledger             refused: the project is on revision 3, not 2", "till               not known whether it opened", "shop               Change 00000000-0029-7000-8000-000000000000", "quick-change working"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	var asked []string
	for _, r := range *seen {
		if r.Method == "POST" {
			asked = append(asked, r.Path)
			if r.Body["fromWhatIf"] != "00000000-0007-7000-8000-000000000000" || r.Body["viaChange"] != true || len(r.Body) != 2 {
				t.Fatalf("AppTown was asked with more than the what-if: %+v", r.Body)
			}
		}
	}
	if len(asked) != 3 {
		t.Fatalf("each named project is asked, after a refusal too: %v", asked)
	}
	refuseLedger = false
	if out, err := run(t, "whatif", "adopt", "00000000-0007-7000-8000-000000000000", "-p", "ledger,shop"); err != nil || !strings.Contains(out, "2 adoption Changes opened, as assessed in what-if 00000000-0007-7000-8000-000000000000") {
		t.Fatalf("all opened: %v\n%s", err, out)
	}
	for _, bad := range [][]string{{"whatif", "adopt", "7"}, {"whatif", "adopt", "x", "-p", "shop"}, {"whatif", "adopt", "-p", "shop"}} {
		if _, err := run(t, bad...); err == nil {
			t.Fatalf("%v was accepted", bad)
		}
	}
}

// apptown whatif adopt --accept-in-advance reads each project's words for
// a yes in advance, prints them, and sends each project back exactly its
// own; a project that offers none is opened without one, and the yes is
// not given without the person's y. Kills: one project's words sent for
// another; a yes sent where none was offered or none was given.
func TestWhatIfAdoptSendsEachProjectItsOwnWordsInAdvance(t *testing.T) {
	seen := fakeForge(t, func(method, path string) (int, string) {
		switch {
		case method == "GET" && path == "/projects/ledger/adoption/in-advance":
			return 200, `{"workflow":"quick-change","acceptance":"I accept it now","landing":["I answer for it now"]}`
		case method == "GET" && path == "/projects/shop/adoption/in-advance":
			return 200, `{"workflow":"","acceptance":"","landing":[],"notOffered":"this project has no quick-change or implement workflow"}`
		case method == "POST" && strings.HasSuffix(path, "/adoption/adopt"):
			return 200, `{"changeId":"00000000-0029-7000-8000-000000000000","candidate":{},"workflow":"quick-change"}`
		}
		return 404, `{"error":"no"}`
	})
	typing(t, "n\n")
	if _, err := run(t, "whatif", "adopt", "00000000-0007-7000-8000-000000000000", "-p", "ledger,shop", "--accept-in-advance"); err == nil || !strings.Contains(err.Error(), "not given") {
		t.Fatalf("a yes in advance the person did not give: %v", err)
	}
	for _, r := range *seen {
		if r.Method == "POST" {
			t.Fatalf("a Change was opened without the yes: %+v", r)
		}
	}
	out, err := run(t, "whatif", "adopt", "00000000-0007-7000-8000-000000000000", "-p", "ledger,shop", "--accept-in-advance", "-y")
	if err != nil || !strings.Contains(out, "ledger: you say yes now, meaning:\n  “I accept it now”\n  “I answer for it now”") || !strings.Contains(out, "shop               no yes in advance: this project has no quick-change") {
		t.Fatalf("the words were not shown: %v\n%s", err, out)
	}
	bodies := map[string]map[string]any{}
	for _, r := range *seen {
		if r.Method == "POST" {
			bodies[strings.Split(r.Path, "/")[2]] = r.Body
		}
	}
	if got := fmt.Sprint(bodies["ledger"]["inAdvance"]); got != "map[shown:[I accept it now I answer for it now]]" {
		t.Fatalf("ledger was sent %s", got)
	}
	if _, sent := bodies["shop"]["inAdvance"]; sent || len(bodies) != 2 {
		t.Fatalf("shop offers no yes in advance, but was sent %+v", bodies)
	}
}

// A help flag anywhere among a command's arguments prints that command's
// lines from apptown help and has no other effect; `apptown dev up --help` once
// started a local boot. After "--" it is an argument like any other.
func TestHelpFlagPrintsTheCommandsHelpAndDoesNothingElse(t *testing.T) {
	t.Chdir(t.TempDir())
	dev := lookup("dev")
	var err error
	out := capture(t, func() { err = dev.invoke([]string{"up", "--help"}) })
	if err != nil || out != dev.help+"\n" {
		t.Fatalf("apptown dev up --help: err=%v out=%q", err, out)
	}
	if _, statErr := os.Stat(".apptown"); !os.IsNotExist(statErr) {
		t.Fatalf("apptown dev up --help wrote .apptown: %v", statErr)
	}

	var got []string
	literal := command{names: []string{"x"}, help: "  x", run: func(a []string) error { got = a; return nil }}
	if err := literal.invoke([]string{"--", "--help"}); err != nil || len(got) != 2 {
		t.Fatalf("a --help after -- did not reach the command: %v %v", err, got)
	}
}

const questionWithAdds = `{"step":"accept","status":"pending","means":"I read it","landingMeans":[],
	"adds":{"base":"b","head":"h","digest":"d1","readFrom":"secrets, services and app parts","nothingNew":"",
	"rows":[],"lines":[{"kind":"added","text":"Secrets: STRIPE_KEY"},{"kind":"unknown","text":"App parts: unknown, .apptown/app.yaml at h does not parse"}]}}`

// What the change adds is shown before an approval is sent, naming what was
// read, and the approval sends back the digest of the list it showed.
func TestApproveShowsAndSendsWhatTheChangeAdds(t *testing.T) {
	seen := fakeForge(t, func(method, path string) (int, string) {
		if path == "/steps/00000000-0007-7000-8000-000000000000/question" {
			return 200, questionWithAdds
		}
		return 200, `{"name":"accept","status":"pass"}`
	})
	out, err := run(t, "approve", "00000000-0007-7000-8000-000000000000", "-y")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"This change adds", "Approving accepts these into the app. Read from secrets, services and app parts.", "Secrets: STRIPE_KEY", "App parts: unknown"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	last := (*seen)[len(*seen)-1]
	if last.Method != "POST" || last.Body["adds"] != "d1" {
		t.Fatalf("approve sent: %+v", last)
	}
}

// apptown show puts what the waiting approval's commit adds under the change,
// and says so when it could not be read rather than leaving it out.
func TestShowSaysWhatTheWaitingApprovalAdds(t *testing.T) {
	for name, approval := range map[string]struct{ body, want string }{
		"nothing new": {`{"stepId":"00000000-0007-7000-8000-000000000000","adds":{"digest":"d","readFrom":"secrets, services and app parts","nothingNew":"Nothing new in secrets, services or app parts","lines":[]}}`, "Nothing new in secrets, services or app parts"},
		"unreadable":  {`{"stepId":"00000000-0007-7000-8000-000000000000","addsError":"git exploded"}`, "what the change adds could not be read: git exploded"},
	} {
		t.Run(name, func(t *testing.T) {
			fakeForge(t, func(method, path string) (int, string) {
				if path == "/changes/00000000-0003-7000-8000-000000000000" {
					return 200, `{"projectSlug":"shop","change":{"id":"00000000-0003-7000-8000-000000000000","title":"T","state":"in_progress"},"approval":` + approval.body + `}`
				}
				return 404, `{"error":"no"}`
			})
			out, err := run(t, "show", "00000000-0003-7000-8000-000000000000")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "This change adds") || !strings.Contains(out, approval.want) {
				t.Fatalf("show printed:\n%s", out)
			}
		})
	}
}

// `apptown pack recommend` sends the version a person chose and says that no
// project moved; `apptown pack versions` shows each version's name, base and
// inclusion, marking the recommended one. A bare pack name still applies.
func TestPackRecommendAndVersions(t *testing.T) {
	history := `{"name":"team","recommended":2,"latest":3,"versions":[
	 {"version":3,"name":"follows go-small v4","basedOn":2,"includedPack":"go-small","includedVersion":4,"note":"follows go-small v4; go-small v4 changed deps/minimal-deps, which you replace"},
	 {"version":2,"basedOn":1,"includedPack":"go-small","includedVersion":3,"note":"published contents"}]}`
	alreadyRecommended := false
	seen := fakeForge(t, func(_, path string) (int, string) {
		if path == "/packs/team/recommended" && alreadyRecommended {
			return 409, `{"error":"that version is already recommended; nothing changed and the what-if was not recorded"}`
		}
		if path == "/packs/team/recommended" || path == "/packs/team" {
			return 200, history
		}
		return 404, `{"error":"no"}`
	})
	out, err := run(t, "pack", "recommend", "team", "v3")
	if err != nil || !strings.Contains(out, "team v3 is recommended. No project moved") {
		t.Fatalf("recommend: %v\n%s", err, out)
	}
	last := (*seen)[len(*seen)-1]
	if last.Method != "POST" || last.Path != "/packs/team/recommended" || last.Body["version"] != float64(3) {
		t.Fatalf("recommend sent %+v", last)
	}
	out, err = run(t, "pack", "versions", "team")
	if err != nil || !strings.Contains(out, `  v3 "follows go-small v4" from v2, includes go-small v4`) || !strings.Contains(out, "* v2 from v1") ||
		!strings.Contains(out, "which you replace") {
		t.Fatalf("versions: %v\n%s", err, out)
	}
	if _, err := run(t, "pack", "recommend", "team", "latest"); err == nil {
		t.Fatal("a version that is not a number was sent")
	}
	if last.Body["whatIf"] != nil {
		t.Fatalf("a recommendation named a what-if it was not given: %+v", last.Body)
	}
	out, err = run(t, "pack", "recommend", "team", "3", "--what-if", "00000000-000c-7000-8000-000000000000")
	if last := (*seen)[len(*seen)-1]; err != nil || last.Body["whatIf"] != "00000000-000c-7000-8000-000000000000" || !strings.Contains(out, "as assessed in what-if 00000000-000c-7000-8000-000000000000") {
		t.Fatalf("recommend --what-if: %v %+v\n%s", err, last.Body, out)
	}
	// AppTown refuses a what-if on the version already recommended
	// (nothing moved, nothing recorded): the command fails with its words
	// and never says the what-if was recorded.
	alreadyRecommended = true
	out, err = run(t, "pack", "recommend", "team", "2", "--what-if", "00000000-000d-7000-8000-000000000000")
	if err == nil || !strings.Contains(err.Error(), "already recommended") || strings.Contains(out, "as assessed in what-if 00000000-000d") {
		t.Fatalf("recommending the recommended version on a what-if: %v\n%s", err, out)
	}
	if _, err := run(t, "pack", "recommend", "team", "2", "--what-if", "x"); err == nil {
		t.Fatal("a what-if that is not a number was sent")
	}
}

// `apptown eval start` shows what starting means and starts only when the
// person types "start", sending the meaning they were shown; `apptown eval
// show` says the cost is "at least" what is known when some was not.
func TestEvalStartShowsTheMeaningFirst(t *testing.T) {
	const meaning = "I start this eval; its throwaway copies may land and deploy to the eval domain."
	seen := fakeForge(t, func(method, path string) (int, string) {
		switch {
		case path == "/evals/start":
			return 200, `{"meaning":"` + meaning + `","profiles":["standard","frontier"],"maxChanges":5}`
		case method == "POST" && path == "/evals":
			return 201, `{"id":"00000000-0007-7000-8000-000000000000"}`
		case path == "/evals/00000000-0007-7000-8000-000000000000":
			return 200, `{"id":"00000000-0007-7000-8000-000000000000","number":7,"startedBy":"alice","subject":"frontier","state":"held","reason":"1 model call spent an unknown amount","budgetMicros":5000000,"costMicros":1250000,"incomplete":1,
			  "arms":[{"id":"00000000-0001-7000-8000-000000000000","sourceChangeId":"00000000-000c-7000-8000-000000000000","arm":"baseline","profile":"standard","state":"working","projectId":"00000000-0028-7000-8000-000000000000","changeId":"00000000-0029-7000-8000-000000000000","score":{"checks":[]}}],
			  "armCosts":{"00000000-0001-7000-8000-000000000000":{"costMicros":1250000,"incomplete":1}},"complaints":[{"id":"00000000-0003-7000-8000-000000000000","about":"way-of-working","armId":"00000000-0001-7000-8000-000000000000","text":"the pack asks for tables"}]}`
		}
		return 404, `{"error":"no"}`
	})
	typing(t, "no\n")
	if out, err := run(t, "eval", "start", "--changes", "00000000-000c-7000-8000-000000000000,00000000-000f-7000-8000-000000000000", "--model", "frontier", "--budget", "5"); err == nil || !strings.Contains(out, meaning) {
		t.Fatalf("started without the word: %v\n%s", err, out)
	}
	for _, r := range *seen {
		if r.Method == "POST" {
			t.Fatalf("sent %+v without confirmation", r)
		}
	}
	typing(t, "start\n")
	if _, err := run(t, "eval", "start", "--changes", "00000000-000c-7000-8000-000000000000,00000000-000f-7000-8000-000000000000", "--model", "frontier", "--budget", "5"); err != nil {
		t.Fatal(err)
	}
	last := (*seen)[len(*seen)-1]
	if last.Method != "POST" || last.Path != "/evals" || last.Body["shown"] != meaning || last.Body["budgetDollars"] != float64(5) || last.Body["profile"] != "frontier" {
		t.Fatalf("start sent %+v", last)
	}
	out, err := run(t, "eval", "show", "00000000-0007-7000-8000-000000000000")
	if err != nil || !strings.Contains(out, "at least $1.25") || !strings.Contains(out, "apptown eval continue 7") || !strings.Contains(out, "about way-of-working") {
		t.Fatalf("show: %v\n%s", err, out)
	}
}

// apptown schedules shows each schedule's cron, next and last firing in UTC,
// says what the last firing did, and never shows a failed read as none.
func TestSchedulesListsNextAndLastFiring(t *testing.T) {
	fakeForge(t, func(method, path string) (int, string) {
		switch path {
		case "/projects/shop/schedules":
			return 200, `{"evalCopy":false,"schedules":[
{"source":{"workflow":"dependency-uplift","version":3},"cron":"0 6 1 * *","timeZone":"UTC","skipIfActive":true,"next":"2026-11-01T06:00:00Z","due":false,"last":{"at":"2026-10-01T06:00:00Z","outcome":"opened","changeId":"00000000-0029-7000-8000-000000000000"}},
{"source":{"workflow":"nightly","version":1},"cron":"0 2 * * *","timeZone":"UTC","skipIfActive":true,"next":"2026-10-12T02:00:00Z","due":false,"last":{"at":"2026-10-11T02:00:00Z","outcome":"skipped"}},
{"source":{"workflow":"weekly","version":1},"cron":"0 2 * * 1","timeZone":"UTC","skipIfActive":true,"next":"2026-10-12T02:00:00Z","due":false}]}`
		case "/projects/copy/schedules":
			return 200, `{"evalCopy":true,"schedules":[]}`
		}
		return 500, `{"error":"database down"}`
	})
	out, err := run(t, "schedules", "-p", "shop")
	for _, want := range []string{"dependency-uplift  0 6 1 * *", "2026-11-01 06:00", "2026-10-01 06:00 opened change 00000000-0029-7000-8000-000000000000", "skipped: previous run still active", "never"} {
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("schedules lacks %q: %v\n%s", want, err, out)
		}
	}
	if out, err := run(t, "schedules", "-p", "copy"); err != nil || !strings.Contains(out, "runs no schedules") {
		t.Fatalf("eval copy: %v\n%s", err, out)
	}
	if out, err := run(t, "schedules", "-p", "broken"); err == nil || strings.Contains(out, "no schedules") {
		t.Fatalf("a failed read: %v\n%s", err, out)
	}
}

// apptown executing is what an operator asks before restarting an AppTown: exit
// 0 when nothing would be interrupted, 3 when something would, with each
// step named by its run's and its own id.
func TestExecutingExitsThreeWhenSomethingWouldBeInterrupted(t *testing.T) {
	run1, step1 := idstest.N(7), idstest.N(8)
	executing := `{"executing":[],"operations":[]}`
	fakeForge(t, func(_, path string) (int, string) {
		if path != "/runs/executing" {
			return 404, `{"error":"no"}`
		}
		return 200, executing
	})
	code := -1
	prev := exitProcess
	exitProcess = func(c int) { code = c }
	t.Cleanup(func() { exitProcess = prev })
	if out, err := run(t, "executing"); err != nil || code != -1 || !strings.Contains(out, "nothing is executing") {
		t.Fatalf("idle: %v exit %d %q", err, code, out)
	}
	executing = fmt.Sprintf(`{"executing":[{"project":"shop","runId":%q,"stepId":%q,"step":"implement","type":"agent","elapsedS":60,"title":"t"}]}`, run1, step1)
	out, err := run(t, "executing")
	if err != nil || code != 3 {
		t.Fatalf("busy: %v exit %d; want 3", err, code)
	}
	if !strings.Contains(out, run1.String()) || !strings.Contains(out, step1.String()) {
		t.Fatalf("each executing step is named by its run's and its own id: %q", out)
	}
}

// A running operation (a restore, a copy, a take-down) is interrupted by a
// restart as a step is: apptown executing names it and exits 3 though no
// step runs, so a deploy waits for it.
//
// Kills: apptown executing reading only the steps (soak finding 56).
func TestExecutingCountsARunningOperation(t *testing.T) {
	fakeForge(t, func(_, path string) (int, string) {
		if path != "/runs/executing" {
			return 404, `{"error":"no"}`
		}
		return 200, `{"executing":[],"operations":[{"number":41,"kind":"deployment.restore","deployment":"fresh","step":"restoring s3","askedBy":"bls","elapsedS":75}]}`
	})
	code := -1
	prev := exitProcess
	exitProcess = func(c int) { code = c }
	t.Cleanup(func() { exitProcess = prev })
	out, err := run(t, "executing")
	if err != nil || code != 3 {
		t.Fatalf("a running operation alone: %v exit %d; want 3\n%s", err, code, out)
	}
	for _, want := range []string{"41", "deployment.restore", "fresh", "restoring s3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the operation is named (%q): %q", want, out)
		}
	}
}
