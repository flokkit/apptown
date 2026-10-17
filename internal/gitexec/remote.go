package gitexec

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Remote is an https Git repository outside the forge, for a project that
// starts from existing code (design/operations/start-from-existing-code.md).
// The caller has already decided the URL may be fetched and resolved its
// host to the address Pin names; git is held to that address so a second
// DNS answer cannot send it elsewhere.
type Remote struct {
	// URL is https, with no user or password in it.
	URL string
	// Pin is "host:port:address", the libcurl resolve entry for the URL's
	// own host and port.
	Pin string
	// Password, when set, is offered to the server through GIT_ASKPASS from
	// the helper's own environment: never in the URL, an argument, the
	// repository's config or a log. The user name offered is "forge".
	Password string
}

// remoteFlags hold git to https at the pinned address, with no redirects
// (a redirect could name an address nobody checked), no hooks, no
// submodules, and no credential helper but the one askpass.
func remoteFlags(pin string) []string {
	return []string{
		"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
		"-c", "core.attributesFile=/dev/null", "-c", "submodule.recurse=false",
		"-c", "protocol.allow=never", "-c", "protocol.https.allow=always",
		"-c", "http.followRedirects=false", "-c", "http.curloptResolve=" + pin,
		"-c", "credential.helper=", "-c", "transfer.fsckObjects=true",
	}
}

// askpass is the helper git asks for a user name and a password. It reads
// the password from its own environment, so the value is in no file.
const askpass = "#!/bin/sh\ncase \"$1\" in\nUsername*) echo forge ;;\n*) printf '%s\\n' \"$FORGE_GIT_PASSWORD\" ;;\nesac\n"

// remoteCommand is git against r, hardened as the hub's commands are (only
// PATH and an empty HOME from the environment, no system or global config),
// run in dir. done removes the HOME and the helper.
func remoteCommand(ctx context.Context, dir string, r Remote, args []string) (*exec.Cmd, func(), error) {
	if !strings.HasPrefix(r.URL, "https://") || r.Pin == "" {
		return nil, func() {}, fmt.Errorf("a remote fetch needs an https URL and a pinned address")
	}
	cmd, done, err := homeCommand(ctx, append(remoteFlags(r.Pin), args...))
	if err != nil {
		return nil, done, err
	}
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	if r.Password != "" {
		helper := filepath.Join(envValue(cmd.Env, "HOME"), "askpass")
		if err := os.WriteFile(helper, []byte(askpass), 0o700); err != nil {
			done()
			return nil, func() {}, err
		}
		cmd.Env = append(cmd.Env, "GIT_ASKPASS="+helper, "FORGE_GIT_PASSWORD="+r.Password)
	}
	return cmd, done, nil
}

// RemoteHead is the branch a remote's HEAD names, "" when it names none.
func RemoteHead(ctx context.Context, r Remote) (string, error) {
	out, err := runRemote(ctx, "", r, "ls-remote", "--symref", "--end-of-options", r.URL, "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if target, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			branch, _, _ := strings.Cut(target, "\t")
			return branch, nil
		}
	}
	return "", nil
}

// FetchRemote fetches refspecs from r into the repository at dir, checking
// every object it receives.
func FetchRemote(ctx context.Context, dir string, r Remote, refspecs ...string) error {
	_, err := runRemote(ctx, dir, r, append([]string{"fetch", "--no-tags", "--update-head-ok", "--end-of-options", r.URL}, refspecs...)...)
	return err
}

// remoteTimeout bounds one remote command: a server that stops answering
// does not hold a project's seed open for ever.
const remoteTimeout = 10 * time.Minute

func runRemote(ctx context.Context, dir string, r Remote, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()
	cmd, done, err := remoteCommand(ctx, dir, r, args)
	if err != nil {
		return "", err
	}
	defer done()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("git %s: the repository did not answer within %s", args[0], remoteTimeout)
		}
		return "", &RemoteFailed{Op: args[0], Why: remoteFailure(stderr.String())}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// RemoteFailed is a remote command that failed, in the forge's own words.
// Git's own diagnostics are never passed on: they carry text the remote
// server chose, which can echo the credential it was offered.
type RemoteFailed struct{ Op, Why string }

func (e *RemoteFailed) Error() string { return "git " + e.Op + ": " + e.Why }

// remoteFailure classifies git's stderr into a fixed sentence.
func remoteFailure(stderr string) string {
	s := strings.ToLower(stderr)
	switch {
	case strings.Contains(s, "authentication failed") || strings.Contains(s, "error: 401") || strings.Contains(s, "error: 403") || strings.Contains(s, "could not read username") || strings.Contains(s, "could not read password"):
		return "the repository refused access: it needs a credential, or the one offered was not accepted"
	case strings.Contains(s, "error: 404") || strings.Contains(s, "not found"):
		return "there is no repository at that address"
	case strings.Contains(s, "could not resolve host") || strings.Contains(s, "failed to connect") || strings.Contains(s, "connection refused") || strings.Contains(s, "timed out"):
		return "the repository could not be reached"
	case strings.Contains(s, "ssl") || strings.Contains(s, "certificate") || strings.Contains(s, "tls"):
		return "the repository's https certificate was not accepted"
	case strings.Contains(s, "redirect"):
		return "the repository answered with a redirect, which the forge does not follow; use the address it redirects to"
	case strings.Contains(s, "fsck") || strings.Contains(s, "bad object") || strings.Contains(s, "invalid"):
		return "the repository sent objects that failed the forge's checks"
	}
	return "the fetch failed (the server's own message is not shown: it could repeat a credential)"
}

// envValue is name's value in env, "" when absent.
func envValue(env []string, name string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v
		}
	}
	return ""
}
