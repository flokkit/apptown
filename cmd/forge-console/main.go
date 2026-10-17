// forge-console opens a console on a directory and puts your terminal on
// it: the Console subsystem standing alone, with nothing of the forge
// linked. A model, a key and a workspace are all it needs.
//
//	forge-console -workspace ~/code/thing -model anthropic/claude-sonnet-5 -env ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY
//
// The session lives under -data (default ~/.forge-console); Ctrl-] detaches
// and, unless -keep, ends the session. -resume <id> reopens a kept one.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/pi"
	"flokkit.io/forge/internal/poll"

	"golang.org/x/term"
)

type envFlag []string

func (e *envFlag) String() string     { return strings.Join(*e, ",") }
func (e *envFlag) Set(v string) error { *e = append(*e, v); return nil }

func main() {
	var env envFlag
	var (
		workspace = flag.String("workspace", ".", "the directory the agent works in (mounted at /work, read-only unless -write)")
		write     = flag.Bool("write", false, "mount the workspace writable")
		model     = flag.String("model", "", "the model, as pi names it (e.g. anthropic/claude-sonnet-5)")
		image     = flag.String("image", "forge-agent", "the agent image")
		data      = flag.String("data", filepath.Join(os.Getenv("HOME"), ".forge-console"), "where sessions live")
		steering  = flag.String("steering", "", "a file whose text steers the agent; default: a plain sentence")
		docker    = flag.Bool("docker", false, "give the agent a private Docker daemon")
		keep      = flag.Bool("keep", false, "leave the session on disk and its container stopped when you detach")
		resume    = flag.String("resume", "", "reopen a kept session by id")
		say       = flag.String("say", "", "send one message and print the reply instead of attaching a terminal")
	)
	var files envFlag
	flag.Var(&env, "env", "an environment variable for the agent, KEY=VALUE (repeatable): provider keys go here")
	flag.Var(&files, "file", "a file to place in the session, DEST=SRC, DEST relative to the session dir (repeatable): e.g. pi-home/.pi/agent/models.json=$HOME/.pi/agent/models.json")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Experimental: not promised in 1.x; it exists to show the console library stands alone (stock/docs/guides/compatibility.md).")
		fmt.Fprintln(os.Stderr, "usage of forge-console:")
		flag.PrintDefaults()
	}
	flag.Parse()
	if err := run(*workspace, *write, *model, *image, *data, *steering, *docker, *keep, *resume, *say, env, files); err != nil {
		fmt.Fprintln(os.Stderr, "forge-console:", err)
		os.Exit(1)
	}
}

func run(workspace string, write bool, model, image, data, steeringFile string, docker, keep bool, resume, say string, env, files []string) error {
	ctx := context.Background()
	staged := map[string]console.Staged{"pi-home/.pi/agent/settings.json": {Data: []byte("{\"quietStartup\": true, \"defaultProjectTrust\": \"never\"}\n")}}
	for _, f := range files {
		dest, src, ok := strings.Cut(f, "=")
		if !ok {
			return fmt.Errorf("-file wants DEST=SRC, got %q", f)
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		staged[dest] = console.Staged{Data: b, Mode: 0o600}
	}
	ws, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	runtime := pi.New(pi.Config{
		Run:   pi.CLI("docker"),
		Image: func() string { return image },
		Dind:  pi.DindConfig{Image: "docker:27-dind"},
	})
	defer runtime.Close()
	text := "You are helping with the code in /work. Read before you answer; say plainly what you find."
	if steeringFile != "" {
		b, err := os.ReadFile(steeringFile)
		if err != nil {
			return err
		}
		text = string(b)
	}
	chats := console.New(console.Dependencies{
		Agent: runtime,
		Environment: console.Static{Provision: console.Provision{
			Model: model,
			Env:   env,
			Files: staged,
			Notes: "This console was opened by forge-console; there is no forge to talk to.",
		}},
		Config: console.Config{DataDir: data, NoDind: !docker},
	})
	if err := chats.Resume(ctx); err != nil {
		return err
	}
	var s *console.Session
	if resume != "" {
		if s, err = chats.Get(ctx, resume); err != nil {
			return fmt.Errorf("session %s: %w", resume, err)
		}
	} else {
		s, err = chats.Open(ctx, console.OpenRequest{Scope: "dir:" + ws, Workspace: console.Workspace{Path: ws, Writable: write || docker}, Docker: docker, Steering: text, Actor: os.Getenv("USER"), Model: model, Pinned: model != ""})
		if err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "session %s on %s (model %q)\n", s.ID, s.Checkout, s.Model)
	if say != "" {
		if err := sayAndWait(ctx, chats, s, say); err != nil {
			return err
		}
	} else if err := attach(ctx, chats, s); err != nil {
		return err
	}
	if keep {
		fmt.Fprintf(os.Stderr, "kept: forge-console -resume %s\n", s.ID)
		return chats.Stop(ctx, s.ID)
	}
	_, err = chats.End(ctx, s.ID)
	return err
}

// attach puts this terminal on the agent's screen until Ctrl-].
func attach(ctx context.Context, chats *console.Service, s *console.Session) error {
	fd := int(os.Stdin.Fd())
	cols, rows, _ := term.GetSize(fd)
	t, err := chats.Attach(ctx, s.ID, console.Size{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return err
	}
	defer t.Close()
	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, old)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			if c, r, err := term.GetSize(fd); err == nil {
				_ = t.Resize(console.Size{Cols: uint16(c), Rows: uint16(r)})
			}
		}
	}()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(os.Stdout, t)
		close(done)
	}()
	buf := make([]byte, 1024)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return nil
		}
		for i := 0; i < n; i++ {
			if buf[i] == 0x1d { // Ctrl-]
				fmt.Fprint(os.Stderr, "\r\ndetached\r\n")
				return nil
			}
		}
		if _, err := t.Write(buf[:n]); err != nil {
			return nil
		}
		select {
		case <-done:
			return nil
		default:
		}
	}
}

// sayAndWait sends one message and prints the agent's reply once it hands back.
func sayAndWait(ctx context.Context, chats *console.Service, s *console.Session, text string) error {
	if err := chats.Send(ctx, s.ID, text); err != nil {
		return err
	}
	before := len(s.Messages())
	for {
		page, err := chats.Messages(ctx, s.ID, 0)
		if err != nil {
			return err
		}
		if len(page.Messages) > before && page.Turn == console.HandedBack {
			for _, m := range page.Messages[before:] {
				if m.Role == "helper" && strings.TrimSpace(m.Text) != "" {
					fmt.Println(m.Text)
				}
			}
			return nil
		}
		if err := poll.Wait(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}
