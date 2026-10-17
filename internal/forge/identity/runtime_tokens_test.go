package identity

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/ids/idstest"
)

// The key is created once, readable only by the forge, and every later start
// reads the same key: an attempt keeps its token across a restart.
func TestRuntimeTokenKeyIsCreatedOnceOwnerOnlyAndReused(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), RuntimeTokenKeyFile)
	first, err := openRuntimeTokens(file)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || info.Size() != runtimeTokenKeySize {
		t.Fatalf("key file %v, %d bytes", info.Mode(), info.Size())
	}
	created, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := openRuntimeTokens(file)
	if err != nil {
		t.Fatal(err)
	}
	if kept, err := os.ReadFile(file); err != nil || !bytes.Equal(kept, created) {
		t.Fatalf("key replaced on reopen: %v", err)
	}
	if first.Step(idstest.N(7), 2) != restarted.Step(idstest.N(7), 2) {
		t.Fatal("the same attempt got another token after a restart")
	}
	if step, attempt, ok := restarted.StepAttempt(first.Step(idstest.N(7), 2)); !ok || step != idstest.N(7) || attempt != 2 {
		t.Fatalf("restarted forge does not recognise the attempt: %d %d %t", step, attempt, ok)
	}
	if entries, err := os.ReadDir(filepath.Dir(file)); err != nil || len(entries) != 1 {
		t.Fatalf("creation left files behind: %v %v", entries, err)
	}
}

// Without its key the forge could not recognise a running agent, so a key
// that cannot be read or is not a key refuses the start.
func TestRuntimeTokenKeyThatCannotBeReadRefusesTheStart(t *testing.T) {
	t.Parallel()
	for name, prepare := range map[string]func(file string) error{
		"unreadable":      func(file string) error { return writeKey(file, 0o000, runtimeTokenKeySize) },
		"a directory":     func(file string) error { return os.Mkdir(file, 0o700) },
		"short":           func(file string) error { return writeKey(file, 0o600, 5) },
		"empty":           func(file string) error { return writeKey(file, 0o600, 0) },
		"folder missing":  func(file string) error { return os.RemoveAll(filepath.Dir(file)) },
		"folder readonly": func(file string) error { return os.Chmod(filepath.Dir(file), 0o500) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			file := filepath.Join(dir, "data", RuntimeTokenKeyFile)
			if err := os.Mkdir(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(filepath.Dir(file), 0o700) })
			if err := prepare(file); err != nil {
				t.Fatal(err)
			}
			if tokens, err := openRuntimeTokens(file); err == nil {
				t.Fatalf("started without a usable key: %+v", tokens)
			}
		})
	}
}

func writeKey(file string, mode os.FileMode, size int) error {
	if err := os.WriteFile(file, bytes.Repeat([]byte{1}, size), 0o600); err != nil {
		return err
	}
	return os.Chmod(file, mode)
}

// A token names its attempt only when its MAC holds under this forge's key;
// any other spelling, another step's MAC or another key's token names nothing.
func TestStepTokenNamesOnlyTheAttemptItsMACHoldsFor(t *testing.T) {
	t.Parallel()
	tokens, err := openRuntimeTokens(filepath.Join(t.TempDir(), RuntimeTokenKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	other, err := openRuntimeTokens(filepath.Join(t.TempDir(), RuntimeTokenKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	token := tokens.Step(idstest.N(12), 3)
	if step, attempt, ok := tokens.StepAttempt(token); !ok || step != idstest.N(12) || attempt != 3 {
		t.Fatalf("own token: %d %d %t", step, attempt, ok)
	}
	mac := token[strings.LastIndex(token, "_")+1:]
	for name, forged := range map[string]string{
		"another step":       fmt.Sprintf("rt_13_3_%s", mac),
		"another attempt":    fmt.Sprintf("rt_12_4_%s", mac),
		"another key":        other.Step(idstest.N(12), 3),
		"truncated mac":      token[:len(token)-1],
		"padded step":        fmt.Sprintf("rt_012_3_%s", mac),
		"extra part":         token + "_1",
		"no prefix":          strings.TrimPrefix(token, "rt_"),
		"negative attempt":   fmt.Sprintf("rt_12_-3_%s", mac),
		"empty":              "",
		"assist-style token": "run_" + mac,
	} {
		if step, attempt, ok := tokens.StepAttempt(forged); ok {
			t.Errorf("%s: %q named step %d attempt %d", name, forged, step, attempt)
		}
	}
	var keyless RuntimeTokens
	if _, _, ok := keyless.StepAttempt(keyless.Step(idstest.N(12), 3)); ok {
		t.Error("a forge without a key recognised a token")
	}
}

// openRuntimeTokens opens tokens the way startup recovery does, through Open
// on a zero RuntimeTokens.
func openRuntimeTokens(file string) (*RuntimeTokens, error) {
	tokens := &RuntimeTokens{}
	if err := tokens.Open(file); err != nil {
		return nil, err
	}
	return tokens, nil
}
