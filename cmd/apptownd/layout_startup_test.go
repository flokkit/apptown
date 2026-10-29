package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown"
	"github.com/flokkit/apptown/internal/execfile"
	"github.com/flokkit/apptown/internal/execution"
)

func TestLayoutRefusalsPrecedeBootstrapEffects(t *testing.T) {
	if root := os.Getenv("APPTOWN_TEST_LAYOUT_CHILD"); root != "" {
		flag.CommandLine = flag.NewFlagSet("apptownd", flag.ExitOnError)
		os.Args = []string{"apptownd", "-data", root, "-local-tls", "-public-host", "https://localhost:7777"}
		if os.Getenv("APPTOWN_TEST_LAYOUT_RESET") == "1" {
			os.Args = append(os.Args, "-reset-password", "alice")
		}
		main()
		os.Exit(0)
	}
	for _, tc := range []struct {
		name, file, body, want string
		reset                  bool
	}{
		{"newer layout", "layout", strconv.Itoa(apptown.DataLayoutVersion + 1), "data directory", false},
		{"unreadable restore continuation", ".apptown-restore-intent.json", "{interrupted", "restore continuation", false},
		{"layout 1 marker", "layout", "1", "before 1.0.0-rc.9", false},
		{"layout 1 without a marker", filepath.Join("workspaces", "project-original", "HEAD"), "ref: refs/heads/main\n", "before 1.0.0-rc.9", false},
		{"unlocked pending relocation", "data-layout-upgrade.json", "{}", "relocation is pending", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.name == "unlocked pending relocation" {
				if err := os.WriteFile(filepath.Join(root, "layout"), []byte(strconv.Itoa(apptown.DataLayoutVersion)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			file := filepath.Join(root, tc.file)
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			tools := t.TempDir()
			effects := filepath.Join(tools, "effects")
			script := "#!/bin/sh\nprintf bootstrap >> \"$APPTOWN_TEST_BOOT_EFFECTS\"\nexit 99\n"
			// The real daemon subprocess selects Docker through PATH; this probe
			// witnesses whether bootstrap crosses that process boundary at all.
			if err := execfile.Write(filepath.Join(tools, "docker"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			reset := "0"
			if tc.reset {
				reset = "1"
			}
			env := append(os.Environ(), "APPTOWN_TEST_LAYOUT_CHILD="+root, "APPTOWN_TEST_LAYOUT_RESET="+reset, "APPTOWN_TEST_BOOT_EFFECTS="+effects, "APPTOWN_DB_URL=", "PATH="+tools)
			result := (execution.Runner{}).Run(ctx, execution.Local, execution.Request{
				Argv: []string{os.Args[0], "-test.run=^TestLayoutRefusalsPrecedeBootstrapEffects$"}, Env: env, Timeout: 10 * time.Second,
			})
			out, runErr := result.Output, result.RunErr
			if result.Termination != execution.Exited || result.CleanupErr != nil {
				t.Fatalf("daemon did not reach a normal refusal: %+v", result)
			}
			after, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before) {
				t.Fatalf("startup changed data directory before refusal: %s", out)
			}
			if _, err := os.Stat(effects); !os.IsNotExist(err) {
				t.Fatalf("database bootstrap ran before refusal: %s", out)
			}
			raw, err := os.ReadFile(file)
			if err != nil || string(raw) != tc.body {
				t.Fatalf("startup changed refusal evidence: %q %v", raw, err)
			}
			if runErr == nil || !strings.Contains(string(out), tc.want) {
				t.Fatalf("wrong refusal: %v %s", runErr, out)
			}
		})
	}
}
