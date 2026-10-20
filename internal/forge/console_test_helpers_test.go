package forge

import (
	"context"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/execfile"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/conversation"

	"flokkit.io/forge/internal/console"
)

// consoleStopped: StopConsoles has run.
func (a *App) consoleStopped() bool {
	select {
	case <-a.consoleStop:
		return true
	default:
		return false
	}
}

// consoleMetaFor is a session record as forge writes it: scoped by change
// or project, with forge's labels.
func consoleMetaFor(id string, projectID, changeID ids.ID) console.Meta {
	labels := map[string]string{conversation.ProjectLabel: projectID.String()}
	if !changeID.IsZero() {
		labels[conversation.ChangeLabel] = changeID.String()
	}
	return console.Meta{ID: id, Scope: conversation.Key(projectID, changeID), Labels: labels}
}

// withLabel returns the record with one more label.
func withLabel(m console.Meta, key, value string) console.Meta {
	m.Labels[key] = value
	return m
}

// Fixtures replacing a service rebuild its concrete dependents. Partial App
// fixtures have no console work to wire and do not acquire unrelated services.
func rebuildConsoleOwners(a *App) {
	if a.Usage != nil && a.Work != nil && a.Console != nil {
		a.changeConsole = changeConsoleFor(a)
		a.consoleAdmission = consoleAdmissionFor(a)
	}
	rebuildRoutes(a)
}

// fixtureCLI supplies an available executable through the normal path check.
// These fixtures inspect mounts; they do not execute the Forge CLI.
func fixtureCLI(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "apptown-linux")
	if err := execfile.Write(path, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// configureConsoleModel admits a model binding and chat default through their
// owners. Launch tests keep Forge's real environment preparation; this helper
// changes persisted configuration, never constructed services.
func configureConsoleModel(t *testing.T, a *App) {
	t.Helper()
	if _, err := a.Integrations.Bindings.Save(context.Background(), "fake", "model", "pi", `{"model":"fake/*","providerDef":{"baseUrl":"https://provider.invalid/v1","api":"openai-completions","apiKey":"provider-canary"},"modelsJson":"[{\"id\":\"model\",\"contextWindow\":32768,\"maxTokens\":1024}]"}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := a.Models.Set(context.Background(), "chat", ids.Nil, "", "standard", "fake/model", "fixture"); err != nil {
		t.Fatal(err)
	}
}
