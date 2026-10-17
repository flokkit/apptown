package pi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What the runtime stamps comes from its clock, not the real one: the
// containers' forge.created labels and the pinned session file's name and
// header.
func TestTheRuntimeStampsFromItsClock(t *testing.T) {
	t.Parallel()
	r, in, f := fixture(t)
	at := time.Date(2031, 3, 1, 9, 0, 0, 0, time.UTC)
	r.cfg.Now = func() time.Time { return at }
	if err := os.MkdirAll(filepath.Join(in.Dir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.provision(in); err != nil {
		t.Fatal(err)
	}
	labelled := 0
	for _, args := range f.calls {
		call := strings.Join(args, " ")
		if (args[0] == "create" || args[0] == "run") && strings.Contains(call, "forge.created=") {
			if !strings.Contains(call, "forge.created=2031-03-01T09:00:00Z") {
				t.Fatalf("a container labelled off the runtime's clock: %s", call)
			}
			labelled++
		}
	}
	if labelled < 2 {
		t.Fatalf("%d containers labelled; want the chat's and its daemon's", labelled)
	}
	names, _ := filepath.Glob(filepath.Join(in.Dir, "sessions", "*.jsonl"))
	if len(names) != 1 || !strings.HasPrefix(filepath.Base(names[0]), "2031-03-01T09-00-00") {
		t.Fatalf("session files %q; want one named for the runtime's clock", names)
	}
	header, _ := os.ReadFile(names[0])
	if !strings.Contains(string(header), `"timestamp":"2031-03-01T09:00:00`) {
		t.Fatalf("the session header is not stamped from the runtime's clock: %s", header)
	}
}

// An attached chat's container is checked again only when 15 s have
// passed on the runtime's clock since it last was.
func TestAnAttachedChatIsRecheckedByTheRuntimesClock(t *testing.T) {
	t.Parallel()
	r, in, f := fixture(t)
	at := time.Date(2031, 3, 1, 9, 0, 0, 0, time.UTC)
	now := at.Add(5 * time.Second)
	r.cfg.Now = func() time.Time { return now }
	terminal, err := os.CreateTemp(t.TempDir(), "pty")
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	st := r.state(in.ID)
	st.ptmx, st.dockerChecked = terminal, at
	if err := r.Start(t.Context(), in); err != nil || len(f.calls) != 0 {
		t.Fatalf("checked again 5 s after the last check: %v, %d commands", err, len(f.calls))
	}
	now = at.Add(20 * time.Second)
	_ = r.Start(t.Context(), in)
	if len(f.calls) == 0 {
		t.Fatal("not checked again 20 s after the last check")
	}
}
