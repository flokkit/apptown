package containerresult_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/containerresult"
	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
)

func TestInvalidResultIsDiagnosedBeforeRemovalWithoutEnvironment(t *testing.T) {
	t.Parallel()
	d, inner := daemon(containerresulttest.Answer{Answer: []byte("invalid")})
	refusal := errors.New("invalid answer")
	inspected := false
	dk := func(ctx context.Context, env []string, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			if d.Removed != 0 {
				return nil, errors.New("container already removed")
			}
			inspected = true
			return []byte(`{"Image":"sha256:engine","Config":{"Image":"busybox","Env":["SECRET=do-not-disclose"]},"State":{"Status":"exited","ExitCode":0}}`), nil
		}
		if args[0] == "image" {
			return []byte(`{"Id":"sha256:engine","Os":"linux","Architecture":"arm64"}`), nil
		}
		if args[0] == "version" {
			return []byte(`{"Client":{"Version":"client-witness"},"Server":{"Version":"server-witness"}}`), nil
		}
		return inner(ctx, env, args...)
	}
	answer, err := containerresult.Run(t.Context(), dk, containerresult.Spec{Image: "busybox", Script: "value", Validate: func(answer []byte) error {
		if d.Removed != 0 {
			t.Fatal("validated after removal")
		}
		if string(answer) != "invalid" {
			t.Fatalf("validator received %q", answer)
		}
		return refusal
	}})
	if answer != nil || !errors.Is(err, refusal) || !inspected || d.Removed != 1 {
		t.Fatalf("answer=%q error=%v inspected=%v removed=%d", answer, err, inspected, d.Removed)
	}
	if !strings.Contains(err.Error(), "sha256:engine") || !strings.Contains(err.Error(), "server-witness") || strings.Contains(err.Error(), "do-not-disclose") {
		t.Fatalf("wrong evidence: %v", err)
	}
}

func TestRunInValidatesBeforeClearingOnlyItsResult(t *testing.T) {
	t.Parallel()
	d, dk := daemon(containerresulttest.Answer{Answer: []byte{}})
	refusal := errors.New("empty forbidden here")
	_, err := containerresult.RunIn(t.Context(), dk, "caller-owned", containerresult.Spec{Script: "value", Validate: func([]byte) error {
		if d.Cleared != 0 {
			t.Fatal("validated after clearing result")
		}
		return refusal
	}})
	if !errors.Is(err, refusal) || d.Cleared != 1 || d.Removed != 0 {
		t.Fatalf("error=%v cleared=%d removed=%d", err, d.Cleared, d.Removed)
	}
}

func TestFailedResultNeverIncludesRawInspection(t *testing.T) {
	t.Parallel()
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprint(invalid), func(t *testing.T) {
			_, inner := daemon(containerresulttest.Answer{})
			dk := func(ctx context.Context, env []string, args ...string) ([]byte, error) {
				if args[0] == "inspect" {
					if invalid {
						return []byte(`{"Env":["SECRET=hidden-value"],broken`), nil
					}
					return []byte(`{"Env":["SECRET=hidden-value"]}`), errors.New("inspect refused")
				}
				return inner(ctx, env, args...)
			}
			_, err := containerresult.Run(t.Context(), dk, containerresult.Spec{Image: "busybox", Script: "value"})
			if !errors.Is(err, containerresult.ErrNoResult) || strings.Contains(err.Error(), "hidden-value") || !strings.Contains(err.Error(), "container inspect:") {
				t.Fatalf("unsafe or missing failure: %v", err)
			}
		})
	}
}
