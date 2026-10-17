package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
)

func TestServiceKindSaveListDelete(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	accounts := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := accounts.SeedAdministrator(context.Background(), "admin"); err != nil {
		t.Fatal(err)
	}
	services := integration.NewService(db, integration.Dependencies{Projects: project.New(db, accounts)})
	h := NewServiceKinds(services, accounts)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.SetPathValue("capability", strings.TrimPrefix(path, "/api/v1/services/"))
		req = req.WithContext(access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "admin", Via: "session"}, Verified: true}))
		w := httptest.NewRecorder()
		switch method {
		case "POST":
			h.SaveServiceKind(w, req)
		case "GET":
			h.ListServiceKinds(w, req)
		case "DELETE":
			h.DeleteServiceKind(w, req)
		}
		return w
	}
	post := func(yaml string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]string{"yaml": yaml})
		if err != nil {
			t.Fatal(err)
		}
		return call("POST", "/api/v1/services", string(body))
	}
	spec := `capability: mailer
title: Mail Relay
description: sends mail
connector:
  fields:
    - {key: host, label: Host, help: the smtp host}
env:
  - {key: MAIL_HOST, value: "{{connector.host}}"}`
	if w := post(spec); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	kinds := services.Definitions(t.Context())
	if len(kinds) != 1 || kinds[0].Capability != "mailer" {
		t.Fatalf("serviceKinds: %+v", kinds)
	}
	if k := services.Definition(t.Context(), "mailer"); k == nil || k.Title != "Mail Relay" {
		t.Fatalf("integration.Definition: %+v", k)
	}
	if services.Definition(t.Context(), "ghost") != nil {
		t.Fatal("unknown capability must be nil")
	}
	w := call("GET", "/api/v1/services", "")
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var listed struct {
		Services []struct {
			Capability string `json:"capability"`
			Title      string `json:"title"`
			YAML       string `json:"yaml"`
			Error      string `json:"error"`
		} `json:"services"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Services) != 1 || listed.Services[0].Title != "Mail Relay" || listed.Services[0].Error != "" {
		t.Fatalf("list payload: %+v", listed.Services)
	}
	if w := post("capability: scm\ntitle: X"); w.Code != 400 {
		t.Fatalf("reserved capability must 400: %d %s", w.Code, w.Body.String())
	}
	if w := post("  "); w.Code != 400 {
		t.Fatalf("empty yaml must 400: %d", w.Code)
	}
	spec2 := strings.Replace(spec, "sends mail", "sends mail v2", 1)
	if w := post(spec2); w.Code != 200 {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	if k := services.Definition(t.Context(), "mailer"); k == nil || k.Description != "sends mail v2" {
		t.Fatalf("update did not take: %+v", k)
	}
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := services.Bindings.Save(context.Background(), "conn", "mailer", "service", `{"host":"mail.example"}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := services.Attachments.Attach(t.Context(), pid, "mailer", "conn", "{}", integration.Entered, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	if w := call("DELETE", "/api/v1/services/mailer", ""); w.Code != 409 {
		t.Fatalf("in-use delete must 409: %d %s", w.Code, w.Body.String())
	}
	if err := services.Attachments.Detach(context.Background(), pid, "mailer"); err != nil {
		t.Fatal(err)
	}
	if w := call("DELETE", "/api/v1/services/mailer", ""); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := call("DELETE", "/api/v1/services/mailer", ""); w.Code != 404 {
		t.Fatalf("second delete must 404: %d %s", w.Code, w.Body.String())
	}
	if len(services.Definitions(t.Context())) != 0 {
		t.Fatal("kind should be gone after delete")
	}
}
