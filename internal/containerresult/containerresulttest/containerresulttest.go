// Package containerresulttest lets a test's stand-in Docker daemon answer
// the calls of a container result read (containerresult.Run and RunIn)
// without knowing their steps: the test says what the command answers,
// and the daemon keeps its stdout to itself, as a daemon that lost it
// would.
package containerresulttest

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/flokkit/apptown/internal/containerresult"
	"github.com/flokkit/apptown/internal/dockerlabel"
)

// Command is one command of a result read as the test sees it: the
// throwaway container's image (Run), or the running container it runs in
// (RunIn), and the caller's script.
type Command struct {
	Image, Container, Script string
	// Args is the docker command that made the throwaway container (its
	// options among them) or ran the script, and Env the values the CLI
	// was given for it (RunEnv).
	Args, Env []string
}

// Answer is what a command did: its exit status, and the answer it left
// in its file (nil: it left none).
type Answer struct {
	Code   int
	Answer []byte
	// Said is what reaches the container's log (its stderr, say); its
	// stdout never does.
	Said string
}

// Daemon answers result reads with Answers. It is safe for concurrent use.
type Daemon struct {
	// Answers says what each command does, asked once per command; ok
	// false leaves the call to the test's own stand-in.
	Answers func(Command) (Answer, bool)

	mu    sync.Mutex
	made  map[string]Answer // throwaway containers' answers by name, until removed
	files map[string]Answer // answers waiting to be copied out, by container:path
	execd []string          // running containers a script ran in
	// Removed counts the throwaway containers removed and Cleared the
	// files a RunIn removed, for tests of cleanup.
	Removed, Cleared int

	standing map[string]*reader // standing readers by name (RunStanding)
	// Started counts the containers started: throwaway ones and standing
	// readers made or started again.
	Started int
}

// reader is a standing reader: how it was made, and whether it runs.
type reader struct {
	class, image string
	args         []string
	running      bool
}

// StopReader stops a standing reader (running false) or removes it
// (gone), as a daemon restart or a person might.
func (d *Daemon) StopReader(name string, gone bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if gone {
		delete(d.standing, name)
	} else if r := d.standing[name]; r != nil {
		r.running = false
	}
}

// Readers names the standing readers the daemon holds.
func (d *Daemon) Readers() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for n := range d.standing {
		out = append(out, n)
	}
	return out
}

// Run answers args (after any --config) when they are a call of a result
// read: the CLI's output, its error, and true. ok is false for any other
// call, which the test answers itself.
func (d *Daemon) Run(args []string) (out []byte, err error, ok bool) {
	return d.RunEnv(args, nil)
}

// RunEnv is Run for a call the CLI was given env for (NAME=value), which
// the command's Env carries.
func (d *Daemon) RunEnv(args, env []string) (out []byte, err error, ok bool) {
	if len(args) > 1 && args[0] == "--config" {
		args = args[2:]
	}
	if len(args) == 0 {
		return nil, nil, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.made == nil {
		d.made, d.files, d.standing = map[string]Answer{}, map[string]Answer{}, map[string]*reader{}
	}
	last := args[len(args)-1]
	switch args[0] {
	case "ps":
		line := strings.Join(args, " ")
		if strings.Contains(line, dockerlabel.Class+"="+containerresult.Class) {
			return nil, nil, true // nothing left over
		}
		if strings.Contains(line, "{{.Names}}\t{{.State}}") {
			class := strings.TrimPrefix(flag(args, "--filter"), "label="+dockerlabel.Class+"=")
			var out strings.Builder
			for n, r := range d.standing {
				if r.class == class {
					state := "exited"
					if r.running {
						state = "running"
					}
					fmt.Fprintf(&out, "%s\t%s\n", n, state)
				}
			}
			return []byte(out.String()), nil, true
		}
	case "run":
		name := flag(args, "--name")
		if len(args) < 2 || args[1] != "-d" || !strings.HasPrefix(name, containerresult.StandingPrefix) {
			return nil, nil, false
		}
		if _, ok := d.standing[name]; ok {
			return []byte("Error response from daemon: Conflict. The container name \"/" + name + "\" is already in use"), exitError(125), true
		}
		class := strings.TrimPrefix(flag(args, "--label"), dockerlabel.Class+"=")
		d.standing[name] = &reader{class: class, image: args[len(args)-3], args: append([]string(nil), args...), running: true}
		d.Started++
		return []byte(name + "\n"), nil, true
	case "create":
		name := flag(args, "--name")
		if !strings.HasPrefix(name, containerresult.Prefix) {
			return nil, nil, false
		}
		inner, ok := unwrap(last)
		if !ok {
			return nil, fmt.Errorf("not a result script: %q", last), true
		}
		c := Command{Image: args[len(args)-4], Script: inner, Args: append([]string(nil), args...), Env: env}
		a, ok := d.Answers(c)
		if !ok {
			return nil, nil, false
		}
		d.made[name] = a
		return []byte(name + "\n"), nil, true
	case "start":
		if _, ok := d.made[last]; ok {
			d.Started++
			return nil, nil, true
		}
		if r, ok := d.standing[last]; ok {
			r.running = true
			d.Started++
			return nil, nil, true
		}
	case "logs":
		if a, ok := d.made[last]; ok {
			return []byte(a.Said), nil, true // never its stdout: that was lost
		}
	case "wait":
		if a, ok := d.made[last]; ok {
			if a.Code == 0 && a.Answer != nil {
				d.files[last+":/tmp/apptown-result"] = a
			}
			return []byte(fmt.Sprintf("%d\n", a.Code)), nil, true
		}
	case "inspect":
		if _, ok := d.made[last]; ok {
			return []byte(`{"Status":"exited","ExitCode":0}`), nil, true
		}
	case "rm":
		if _, ok := d.made[last]; ok {
			delete(d.made, last)
			d.Removed++
			return nil, nil, true
		}
		if _, ok := d.standing[last]; ok {
			delete(d.standing, last)
			return nil, nil, true
		}
	case "cp":
		if len(args) != 3 {
			return nil, nil, false
		}
		a, ok := d.files[args[1]]
		if !ok {
			name, _, _ := strings.Cut(args[1], ":")
			if _, made := d.made[name]; made || d.execs(name) {
				return []byte("Error response from daemon: Could not find the file " + args[1]), fmt.Errorf("exit status 1"), true
			}
			return nil, nil, false
		}
		if err := os.WriteFile(args[2], a.Answer, 0o600); err != nil {
			return nil, err, true
		}
		return nil, nil, true
	case "exec":
		at := 1
		for at < len(args) && args[at] == "-e" {
			at += 2
		}
		if at+3 >= len(args) {
			return nil, nil, false
		}
		container := args[at]
		if args[at+1] == "rm" && strings.HasPrefix(args[at+3], "/tmp/apptown-result-") {
			delete(d.files, container+":"+args[at+3])
			d.Cleared++
			return nil, nil, true
		}
		if args[at+1] != "sh" || args[at+2] != "-c" {
			return nil, nil, false
		}
		inner, ok := unwrap(last)
		if !ok {
			return nil, nil, false
		}
		c := Command{Container: container, Script: inner, Args: append([]string(nil), args...), Env: env}
		if r, ok := d.standing[container]; ok {
			if !r.running {
				return []byte("Error response from daemon: container " + container + " is not running"), exitError(1), true
			}
			// A standing reader's read is judged as the reader was made.
			c.Image, c.Args = r.image, append(append([]string(nil), r.args...), args...)
		} else if strings.HasPrefix(container, containerresult.StandingPrefix) {
			return []byte("Error response from daemon: No such container: " + container), exitError(1), true
		}
		a, ok := d.Answers(c)
		if !ok {
			return nil, nil, false
		}
		path := last[strings.LastIndex(last, " ")+1:]
		if a.Code != 0 {
			return []byte(a.Said), exitError(a.Code), true
		}
		if a.Answer != nil {
			d.files[container+":"+path] = a
		}
		d.execd = append(d.execd, container)
		return nil, nil, true
	}
	return nil, nil, false
}

func (d *Daemon) execs(container string) bool {
	for _, c := range d.execd {
		if c == container {
			return true
		}
	}
	return false
}

// unwrap is the caller's script inside a result read's wrapping.
func unwrap(s string) (string, bool) {
	i := strings.LastIndex(s, "\n) >")
	if !strings.HasPrefix(s, "( ") || i < 0 {
		return "", false
	}
	return s[2:i], true
}

func flag(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// exitError is a docker CLI that exited with a status.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitError) ExitCode() int { return int(e) }

// Framed is data as a framed command sends it (containerresult.Framed):
// the data, then its trailer. A stand-in daemon answers a copy with it.
func Framed(data []byte) []byte {
	return append(append([]byte{}, data...), fmt.Sprintf("APPTOWN-STREAM-END %020d %x\n", len(data), sha256.Sum256(data))...)
}

// WriteString writes a copy to a step's output as a stand-in daemon's
// container sends it: framed when the output is a framed command's (a
// containerresult.Receiver), as it is otherwise.
func WriteString(w io.Writer, copy string) (int, error) {
	data := []byte(copy)
	if _, framed := w.(*containerresult.Receiver); framed {
		data = Framed(data)
	}
	return w.Write(data)
}

// Destination is the writer a step's output goes to in the end: the one a
// receiver passes a framed copy on to.
func Destination(w io.Writer) io.Writer {
	if r, ok := w.(*containerresult.Receiver); ok {
		return r.Unwrap()
	}
	return w
}

// Unframe is the command inside a framed one (containerresult.Framed), or
// s as it is when it is not framed: a stand-in daemon recognises a copy's
// command by what its kind declared.
func Unframe(s string) string {
	before, after, _ := strings.Cut(containerresult.Framed("\x00"), "\x00")
	if strings.HasPrefix(s, before) && strings.HasSuffix(s, after) && len(s) >= len(before)+len(after) {
		return s[len(before) : len(s)-len(after)]
	}
	return s
}

// UnframeLine is a docker command line, its words joined by spaces, with a
// framed command in it unframed.
func UnframeLine(line string) string {
	before, after, _ := strings.Cut(containerresult.Framed("\x00"), "\x00")
	i := strings.Index(line, before)
	j := strings.LastIndex(line, after)
	if i < 0 || j < i+len(before) {
		return line
	}
	return line[:i] + line[i+len(before):j] + line[j+len(after):]
}

// UnframeArgs is a docker command's arguments with a framed command among
// them unframed (UnframeLine): what a stand-in daemon whose containers run
// the frame sees as the command.
func UnframeArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = UnframeLine(a)
	}
	return out
}

// Configure installs an answerer supplied by a composing test fake.
func (d *Daemon) Configure(answer func(string, string, string, []string, []string) (int, []byte, string, bool)) {
	*d = Daemon{Answers: func(c Command) (Answer, bool) {
		code, data, said, ok := answer(c.Image, c.Container, c.Script, c.Args, c.Env)
		return Answer{Code: code, Answer: data, Said: said}, ok
	}}
}
func (d *Daemon) StartedCount() int { return d.Started }
