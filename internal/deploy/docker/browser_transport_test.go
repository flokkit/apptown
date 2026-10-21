package docker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
)

// Only AppTown sets a static part's browser values: a contract or secret
// value of the same name never stands in for them.
func TestNothingButTheForgeSetsAStaticPartsBrowserValues(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"contract", "secret"} {
		t.Run(mode, func(t *testing.T) {
			client := &fakeClient{reply: func(c Command) ([]byte, error) {
				if len(c.Args) > 0 && c.Args[0] == "port" {
					return []byte("127.0.0.1:45678\n"), nil
				}
				return nil, nil
			}}
			part := &appcontract.Part{Static: ".", Port: 8080}
			var secrets map[string]string
			if mode == "contract" {
				part.Env = map[string]string{BrowserEnvVar: `{"SENTRY_DSN":"substituted"}`}
			} else {
				secrets = map[string]string{BrowserEnvVar: "private-review-canary"}
			}
			_, err := NewHost(Dependencies{}).StartAppContainer(t.Context(), BootRequest{Docker: Scope{Client: client}, Name: "review-browser", Workspace: t.TempDir()}, part, "", secrets)
			if err != nil {
				t.Fatal(err)
			}
			var final string
			for _, args := range client.args {
				if len(args) == 0 || args[0] != "run" {
					continue
				}
				for i, arg := range args {
					if arg == "-e" && i+1 < len(args) && strings.HasPrefix(args[i+1], BrowserEnvVar+"=") {
						final = strings.TrimPrefix(args[i+1], BrowserEnvVar+"=")
					}
				}
			}
			if final != "{}" {
				t.Fatalf("public endpoint uses overridden transport: %q", final)
			}
		})
	}
}

// Browser values are read before the running container is replaced: an
// attachment that cannot be read fails the boot and leaves the working
// deployment serving.
func TestAnUnreadableBrowserValueLeavesTheRunningContainer(t *testing.T) {
	t.Parallel()
	client := &fakeClient{reply: func(Command) ([]byte, error) { return nil, nil }}
	_, err := NewHost(Dependencies{Integrations: unreadableBrowser{}}).StartAppContainer(t.Context(), BootRequest{Docker: Scope{Client: client}, Name: "serving", Workspace: t.TempDir()}, &appcontract.Part{Static: ".", Port: 8080}, "", nil)
	if err == nil || !strings.Contains(err.Error(), "browser may be given") {
		t.Fatalf("want the read failure, got %v", err)
	}
	if len(client.args) != 0 {
		t.Fatalf("the running container was touched: %v", client.args)
	}
}

type unreadableBrowser struct{}

func (unreadableBrowser) EnvFor(context.Context) []string { return nil }
func (unreadableBrowser) BrowserEnv(context.Context) (string, error) {
	return "", errors.New("the feedback attachment could not be read")
}

// A static site's browser values are read before anything in its stack
// changes, standing or a historical release preview: a failed read leaves
// migrations unrun and workers untouched. And a release image, which is the
// file server, is still given them.
func TestBrowserValuesAreReadBeforeTheStackChanges(t *testing.T) {
	t.Parallel()
	for _, releaseImage := range []string{"", "apptown-preview-build-historical"} {
		for _, readable := range []bool{false, true} {
			t.Run(fmt.Sprintf("release=%s/readable=%v", releaseImage, readable), func(t *testing.T) {
				client := &fakeClient{reply: func(c Command) ([]byte, error) {
					if len(c.Args) > 1 && c.Args[0] == "image" && c.Args[1] == "inspect" {
						return []byte("sha256:review"), nil
					}
					if len(c.Args) > 0 && c.Args[0] == "port" {
						return []byte("127.0.0.1:45678"), nil
					}
					return nil, nil
				}}
				c := &appcontract.Contract{Parts: []appcontract.Part{
					{Name: "web", Public: true, Static: ".", Port: 8080},
					{Name: "worker", Image: "worker:1"},
				}, Init: []appcontract.InitContainer{{Name: "migrate", Image: "migration:1"}}}
				req := BootRequest{Docker: Scope{Client: client}, Name: "serving", StackID: "env-review-main", Workspace: t.TempDir(), Persistent: true, Names: Current}
				if releaseImage == "" {
					req.Identity = &deploy.DockerIdentity{Deployment: "1", Generation: 2, Artifact: "review"}
				} else {
					req.Persistent, req.ReleaseImage, req.StackID = false, releaseImage, "release-preview-review"
					c.Parts, c.Init = c.Parts[:1], nil
				}
				var browser Contributions = unreadableBrowser{}
				if readable {
					browser = givenBrowser{}
				}
				_, err := NewHost(Dependencies{Needs: reviewNoNeeds{}, Secrets: reviewNoSecrets{}, Integrations: browser, Probe: reviewUp{}}).Boot(context.Background(), req, c)
				if !readable {
					if err == nil || !strings.Contains(err.Error(), "browser may be given") {
						t.Fatalf("expected the read failure, got %v", err)
					}
					for _, args := range client.args {
						if !diskRead(args) && (args[0] == "run" || args[0] == "rm") {
							t.Fatalf("changed the running stack before reading browser values: %v", client.args)
						}
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				given := false
				for _, args := range client.args {
					if args[0] == "run" && slices.Contains(args, "serving") && slices.Contains(args, BrowserEnvVar+`={"SENTRY_DSN":"https://k@e/1"}`) {
						given = true
					}
				}
				if !given {
					t.Fatalf("the public container was not given its browser values: %v", client.args)
				}
			})
		}
	}
}

type givenBrowser struct{}

func (givenBrowser) EnvFor(context.Context) []string { return nil }
func (givenBrowser) BrowserEnv(context.Context) (string, error) {
	return `{"SENTRY_DSN":"https://k@e/1"}`, nil
}

type reviewNoNeeds struct{}

func (reviewNoNeeds) Fulfil(context.Context, []string, map[string]map[string]string) ([]deploy.ServiceSpec, map[string]string, error) {
	return nil, nil, nil
}
func (reviewNoNeeds) Data(context.Context, string) (string, error) { return "", nil }

type reviewNoSecrets struct{}

func (reviewNoSecrets) Resolve(context.Context, string, string, string, *appcontract.Contract) (Delivery, error) {
	return Delivery{}, nil
}

type reviewUp struct{}

func (reviewUp) Check(context.Context, Scope, string, string, time.Duration, RenderedProbe) (bool, string) {
	return true, ""
}
