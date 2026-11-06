package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeploymentRuntimeCommandsPostOnceToTheirDeployment(t *testing.T) {
	for _, action := range []string{"start", "stop", "restart"} {
		t.Run(action, func(t *testing.T) {
			n := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n++
				if r.Method != "POST" || r.URL.Path != "/api/v1/projects/lunch/deployments/main/"+action {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer server.Close()
			prev := api
			api = &client{base: server.URL, actor: "tester", http: server.Client(), getenv: func(string) string { return "" }}
			defer func() { api = prev }()
			if err := cmdDeployment([]string{action, "lunch", "main"}); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Fatalf("made %d requests", n)
			}
			if err := cmdDeployment([]string{action, "lunch"}); err == nil {
				t.Fatal("accepted missing deployment")
			}
		})
	}
}
