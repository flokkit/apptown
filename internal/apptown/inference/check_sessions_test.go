package inference

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/ids"
)

func TestCheckCredentialIsBoundToItsTaskModelAndLifetime(t *testing.T) {
	var sessions CheckSessions
	g, _ := inferenceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"model":"served","usage":{"prompt_tokens":4,"completion_tokens":2}}`)
	})
	identities := Identity{Checks: &sessions}
	g.checks = &sessions
	g.authenticate = identities.Authenticate
	g.open = func(context.Context, identity.Workload, string, string, string, time.Duration) (ids.ID, error) {
		return ids.New(time.Now()), nil
	}
	g.settle = func(context.Context, ids.ID, inferenceUsage, string) (bool, error) { return true, nil }
	token, err := sessions.Open("task-a", modelcall.Call{Model: "paid/allowed", Actor: "what-if/fixture", Purpose: "what-if"}, 20, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	call := func(model string) int {
		r := inferenceRequest("/providers/paid/chat/completions", `{"model":"`+model+`"}`)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		return w.Code
	}
	if err := sessions.begin(t.Context(), token, "another/model"); err == nil {
		t.Fatal("task credential admitted another model")
	}
	if _, err := sessions.Close(token, "task-b"); err == nil {
		t.Fatal("another task closed this credential")
	}
	if call("allowed") != 200 {
		t.Fatal("live check task refused")
	}
	u, err := sessions.Close(token, "task-a")
	if err != nil || u.Prompt != 4 || u.Completion != 2 || u.Model != "served" {
		t.Fatalf("gateway observations: %+v %v", u, err)
	}
	if call("allowed") != 401 {
		t.Fatal("credential survived task settlement")
	}
}

func TestCheckTokenLimitPreventsFurtherInferenceAndAnyVerdict(t *testing.T) {
	var s CheckSessions
	token, err := s.Open("capped", modelcall.Call{Model: "paid/allowed", Actor: "what-if/fixture", Purpose: "what-if"}, 6, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.begin(t.Context(), token, "paid/allowed"); err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := s.begin(waiting, token, "paid/allowed"); err == nil {
		t.Fatal("concurrent turn evaded a session allowance")
	}
	s.finish(token, "served", inferenceUsage{Observed: true, Input: 4, Output: 2}, "", true)
	if _, ok := s.authenticate(token); ok {
		t.Fatal("capped session remains admitted")
	}
	if err := s.begin(t.Context(), token, "paid/allowed"); err == nil {
		t.Fatal("capped session got a wrap-up turn")
	}
	u, err := s.Close(token, "capped")
	if err != nil || !strings.Contains(u.Reason, "token limit of 6") {
		t.Fatalf("cap outcome: %+v %v", u, err)
	}
	token, _ = s.Open("unknown", modelcall.Call{Model: "paid/allowed", Actor: "what-if/fixture", Purpose: "what-if"}, 60, time.Now().Add(time.Minute))
	_ = s.begin(t.Context(), token, "paid/allowed")
	s.finish(token, "", inferenceUsage{}, "", true)
	u, _ = s.Close(token, "unknown")
	if u.Reason == "" {
		t.Fatal("missing usage silently bypassed allowance")
	}
}

func TestCheckCredentialHasNoStepAuthorityAndRetainsItsSpendAttribution(t *testing.T) {
	var s CheckSessions
	token, err := s.Open("task", modelcall.Call{Model: "paid/allowed", Actor: "what-if/fixture", Purpose: "what-if"}, 100, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	a := Identity{Checks: &s, Tokens: &identity.RuntimeTokens{}}
	if _, err := a.AuthenticateStep(t.Context(), token); err == nil {
		t.Fatal("check credential acquired workflow API authority")
	}
	who, err := a.Authenticate(t.Context(), token)
	if err != nil || who.Kind != "check" || who.SessionID != "task" || who.Actor != "what-if/fixture" || s.purpose(token) != "what-if" {
		t.Fatalf("check identity: %+v %v", who, err)
	}
	_, _ = s.Close(token, "task")
}

func TestClosingAnInflightCheckRetainsUnknownSpend(t *testing.T) {
	var s CheckSessions
	token, err := s.Open("task", modelcall.Call{Model: "paid/allowed"}, 100, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.begin(t.Context(), token, "paid/allowed"); err != nil {
		t.Fatal(err)
	}
	u, err := s.Close(token, "task")
	if err != nil || u.Incomplete == "" || u.Reason == "" {
		t.Fatalf("unfinished inference looked complete: %+v %v", u, err)
	}
}

func TestCheckCredentialExpiryUsesItsOwnersClock(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	s := CheckSessions{Now: func() time.Time { return now }}
	token, err := s.Open("task", modelcall.Call{Model: "paid/allowed"}, 100, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.authenticate(token); !ok {
		t.Fatal("live credential refused")
	}
	now = now.Add(time.Minute)
	if _, ok := s.authenticate(token); ok {
		t.Fatal("credential survived its owner's expiry")
	}
	if err := s.begin(t.Context(), token, "paid/allowed"); err == nil {
		t.Fatal("expired task dispatched inference")
	}
}

// Only unknown usage on an accepted response prevents further accounting.
func TestCheckRetriesRetainKnownUsageAcrossIncompleteResponses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, served, incomplete string
		usage                    inferenceUsage
		admitted                 bool
	}{
		{name: "refused", incomplete: "check inference did not complete"},
		{name: "dropped stream with usage", served: "served", incomplete: "check inference did not complete", usage: inferenceUsage{Observed: true, Input: 4, Output: 2}, admitted: true},
		{name: "missing model with usage", usage: inferenceUsage{Observed: true, Input: 4, Output: 2}, admitted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var s CheckSessions
			token, err := s.Open("task", modelcall.Call{Model: "paid/allowed"}, 100, time.Now().Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.begin(t.Context(), token, "paid/allowed"); err != nil {
				t.Fatal(err)
			}
			s.finish(token, tc.served, tc.usage, tc.incomplete, tc.admitted)
			if err := s.begin(t.Context(), token, "paid/allowed"); err != nil {
				t.Fatalf("retry refused: %v", err)
			}
			s.finish(token, "served", inferenceUsage{Observed: true, Input: 10, Output: 2}, "", true)
			u, err := s.Close(token, "task")
			if err != nil || u.Reason != "" || u.Prompt != 10+tc.usage.Input || u.Completion != 2+tc.usage.Output {
				t.Fatalf("retry accounting: %+v %v", u, err)
			}
		})
	}
}
