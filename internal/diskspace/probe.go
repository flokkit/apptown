package diskspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/strs"
)

// Docker is one docker CLI call on the daemon whose disk is read: what it
// wrote on stdout and on stderr (a caller that cannot keep them apart
// gives both as stdout), and its error.
type Docker func(ctx context.Context, args ...string) (stdout, stderr string, err error)

// ProbeClass is the apptown.class of a disk read's container, and
// ProbePrefix begins its name.
const (
	ProbeClass  = "disk-probe"
	ProbePrefix = "apptown-disk-probe-"
)

// ResultFolder is where the probe container keeps its result: df's
// stdout, stderr and exit status (ResultFiles), in its own writable layer.
const ResultFolder = "/probe"

// ResultFiles are the files of ResultFolder: df's stdout, its stderr and
// its exit status.
var ResultFiles = [3]string{"df.out", "df.err", "df.rc"}

// probeScript runs df once on /d, keeps its answer and status in
// ResultFolder, and also prints them, so the log can be compared with the
// file (the log is never the answer).
const probeScript = `mkdir -p /probe || exit 97
df -Pk /d >/probe/df.out 2>/probe/df.err
rc=$?
echo $rc >/probe/df.rc
cat /probe/df.out
cat /probe/df.err >&2
exit $rc`

// probeLifetime is how long a probe container may stand before another
// read's sweep removes it: far longer than a read takes (about a second),
// so a sweep never removes a read still in progress.
const probeLifetime = 10 * time.Minute

// removalBound bounds the removal of a read's container, which runs even
// when the caller has stopped.
const removalBound = time.Minute

// ReadThroughDocker reads the filesystem holding root on dk's daemon (its
// data folder, say). Docker reports no disk figures itself, and docker
// system df walks every layer (minutes on a busy host), so a throwaway
// container of image (the tool versions' df, from its caller's snapshot)
// with no network mounts root read only and runs df there.
//
// The answer is a file, never the container's output: in CI the output
// of a container this short-lived has come back empty with exit 0, on the
// attach stream and in its log alike, most likely dropped by the daemon's
// output drain before its logger. The container writes df's stdout,
// stderr and status into ResultFolder, and after it has exited the read
// copies that folder out with docker cp (the archive API, not the
// process's output pipes) into a temporary folder here. The answer is
// accepted only when the container and df both exited 0 and df's output
// parses; anything else is an error, and new work is refused as for any
// disk not read. A file that holds the answer while the log does not is
// reported (report), with what Docker shows of the container, as the
// loss the file was made for.
//
// The container is named before it is made and removed by that name when
// the read ends, however it ends (the caller stopping included). A removal
// that fails, or a container whose creation was never answered, is left
// to a later read: each read first removes probe containers past their
// deadline label, and nothing else. now stamps that deadline and times the
// read. A read with no usable answer is an error that says why, what the
// file and the log held, and what Docker shows of the container, its image
// and its versions (evidence), gathered before the container is removed.
func ReadThroughDocker(ctx context.Context, dk Docker, image, root string, now func() time.Time, report func(string)) (Space, error) {
	if image == "" {
		return Space{}, fmt.Errorf("no df image: the tool versions name it")
	}
	start := now()
	sweepProbes(ctx, dk, start)
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return Space{}, fmt.Errorf("naming the probe container: %w", err)
	}
	name := ProbePrefix + hex.EncodeToString(suffix[:])
	local, err := os.MkdirTemp("", "apptown-disk-probe-")
	if err != nil {
		return Space{}, fmt.Errorf("a folder for the probe's result: %w", err)
	}
	defer os.RemoveAll(local)
	defer func() {
		// The caller's stop must not leave the container: the removal
		// runs to its own bound. A failure is the next read's to sweep.
		rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removalBound)
		defer cancel()
		_, _, _ = dk(rmCtx, "rm", "-f", "-v", name)
	}()
	deadline := start.Add(probeLifetime).Unix()
	if stdout, stderr, err := dk(ctx, "create", "--name", name,
		"--label", dockerlabel.Class+"="+ProbeClass, "--label", dockerlabel.Deadline+"="+strconv.FormatInt(deadline, 10),
		"--log-driver", "json-file", "--network", "none", "-v", root+":/d:ro", image, "sh", "-c", probeScript); err != nil {
		return Space{}, fmt.Errorf("creating the probe container %s: %v; stdout %q; stderr %q", name, err, clip(stdout), clip(stderr))
	}
	failed := func(why string) error {
		return fmt.Errorf("%s; probe container %s, after %s; %s", why, name, now().Sub(start).Round(time.Millisecond), evidence(ctx, dk, name))
	}
	if stdout, stderr, err := dk(ctx, "start", name); err != nil {
		return Space{}, failed(fmt.Sprintf("starting the probe container: %v; stdout %q; stderr %q", err, clip(stdout), clip(stderr)))
	}
	code, stderr, err := dk(ctx, "wait", name)
	if err != nil {
		return Space{}, failed(fmt.Sprintf("waiting for the probe container: %v; stdout %q; stderr %q", err, clip(code), clip(stderr)))
	}
	exit := strings.TrimSpace(code)
	cpOut, cpErrOut, cpErr := dk(ctx, "cp", name+":"+ResultFolder, local)
	r := readResult(filepath.Join(local, filepath.Base(ResultFolder)))
	logOut, logErr, logsErr := dk(ctx, "logs", name)
	logs := fmt.Sprintf("its log: stdout %q, stderr %q", clip(logOut), clip(logErr))
	if logsErr != nil {
		logs = fmt.Sprintf("its log could not be read: %v: %s", logsErr, clip(logOut+logErr))
	}
	space, parseErr := ParseDF([]byte(r.out))
	var why string
	switch {
	case cpErr != nil:
		why = fmt.Sprintf("copying the probe's result: %v; stdout %q; stderr %q", cpErr, clip(cpOut), clip(cpErrOut))
	case exit != "0":
		why = "the probe container exited " + strconv.Quote(exit)
	case r.missing != "":
		why = "the probe's result is incomplete: " + r.missing
	case r.rc != "0":
		why = "df exited " + strconv.Quote(r.rc)
	case parseErr != nil:
		why = parseErr.Error()
	}
	if why != "" {
		return Space{}, failed(why + "; " + r.String() + "; " + logs)
	}
	if strings.TrimSpace(logOut) != strings.TrimSpace(r.out) && report != nil {
		report(fmt.Sprintf("disk read: the probe container %s's answer was in its result file but not in what docker logs returned (lost before its logger, or in reading the log): %s; %s; %s", name, r.String(), logs, evidence(ctx, dk, name)))
	}
	return space, nil
}

// result is what the probe container left in its result folder: df's
// stdout, stderr and status, and which files could not be read.
type result struct {
	out, err, rc string
	missing      string
}

func readResult(dir string) result {
	var r result
	var missing []string
	read := func(file string) string {
		b, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			missing = append(missing, file)
		}
		return string(b)
	}
	r.out, r.err, r.rc = read(ResultFiles[0]), read(ResultFiles[1]), strings.TrimSpace(read(ResultFiles[2]))
	if r.rc == "" && !slices.Contains(missing, ResultFiles[2]) {
		missing = append(missing, ResultFiles[2]+" (empty)")
	}
	r.missing = strings.Join(missing, ", ")
	return r
}

func (r result) String() string {
	return fmt.Sprintf("its result file: df.out %q, df.err %q, df.rc %q", clip(r.out), clip(r.err), r.rc)
}

// evidence is what Docker shows of the probe container name, its image
// and its own versions, in one line, for a read that failed or whose file
// and log disagree: the fields that say which image and command ran, how
// its output was handled and how it ended, never its environment. A part
// that could not be read says so.
func evidence(ctx context.Context, dk Docker, name string) string {
	var parts []string
	var c struct {
		Image   string
		Path    string
		Args    []string
		LogPath string
		Config  struct {
			Image                           string
			Entrypoint, Cmd                 []string
			AttachStdout, AttachStderr, Tty bool
		}
		Mounts []struct {
			Source, Destination string
			RW                  bool
		}
		HostConfig struct{ LogConfig struct{ Type string } }
		State      struct {
			Status                string
			ExitCode              int
			Error                 string
			OOMKilled             bool
			StartedAt, FinishedAt string
		}
	}
	if err := inspectJSON(ctx, dk, &c, "inspect", "--type", "container", "--format", "{{json .}}", name); err != nil {
		parts = append(parts, "container inspect: "+err.Error())
	} else {
		var mounts []string
		for _, m := range c.Mounts {
			mounts = append(mounts, fmt.Sprintf("%s:%s rw=%v", m.Source, m.Destination, m.RW))
		}
		parts = append(parts, fmt.Sprintf("container image %s (%s), path %s, args %q, entrypoint %q, cmd %q, attach stdout %v stderr %v tty %v, mounts [%s], log %s at %s, state %s exit %d error %q oom %v started %s finished %s",
			c.Image, c.Config.Image, c.Path, clip(strings.Join(c.Args, " ")), c.Config.Entrypoint, clip(strings.Join(c.Config.Cmd, " ")),
			c.Config.AttachStdout, c.Config.AttachStderr, c.Config.Tty, strings.Join(mounts, "; "),
			c.HostConfig.LogConfig.Type, c.LogPath, c.State.Status, c.State.ExitCode, c.State.Error, c.State.OOMKilled, c.State.StartedAt, c.State.FinishedAt))
		var img struct {
			ID           string `json:"Id"`
			RepoDigests  []string
			Os           string
			Architecture string
			Config       struct{ Entrypoint, Cmd []string }
		}
		if c.Image == "" {
			parts = append(parts, "image inspect: the container names no image")
		} else if err := inspectJSON(ctx, dk, &img, "image", "inspect", "--format", "{{json .}}", c.Image); err != nil {
			parts = append(parts, "image inspect: "+err.Error())
		} else {
			parts = append(parts, fmt.Sprintf("image %s digests %q %s/%s entrypoint %q cmd %q", img.ID, img.RepoDigests, img.Os, img.Architecture, img.Config.Entrypoint, img.Config.Cmd))
		}
	}
	var v struct {
		Client struct{ Version string }
		Server struct {
			Version    string
			Components []struct{ Name, Version string }
		}
	}
	if err := inspectJSON(ctx, dk, &v, "version", "--format", "{{json .}}"); err != nil {
		parts = append(parts, "docker version: "+err.Error())
	} else {
		var comps []string
		for _, c := range v.Server.Components {
			comps = append(comps, c.Name+" "+c.Version)
		}
		parts = append(parts, fmt.Sprintf("docker client %s, server %s (%s)", v.Client.Version, v.Server.Version, strings.Join(comps, ", ")))
	}
	return strings.Join(parts, "; ")
}

// inspectJSON runs a docker call that prints one JSON object and decodes
// it into v, from its first brace (a caller's combined output may lead
// with a notice).
func inspectJSON(ctx context.Context, dk Docker, v any, args ...string) error {
	// What Docker printed is never kept here: it may hold the container's
	// environment, which the fields chosen above leave out.
	out, _, err := dk(ctx, args...)
	if err != nil {
		return err
	}
	i := strings.Index(out, "{")
	if i < 0 {
		return errors.New("no JSON in its answer")
	}
	if err := json.NewDecoder(strings.NewReader(out[i:])).Decode(v); err != nil {
		return fmt.Errorf("its answer could not be read: %v", err)
	}
	return nil
}

// sweepProbes removes the probe containers past their deadline, or with
// none that can be read: those a read could not remove. A container of
// another read still in progress is inside its deadline and is left. A
// sweep that fails changes nothing about this read; the next one sweeps
// again.
func sweepProbes(ctx context.Context, dk Docker, now time.Time) {
	out, _, err := dk(ctx, "ps", "-a", "--filter", "label="+dockerlabel.Class+"="+ProbeClass,
		"--format", `{{.ID}} {{.Label "`+dockerlabel.Deadline+`"}}`)
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		id, deadline, _ := strings.Cut(strings.TrimSpace(line), " ")
		if id == "" {
			continue
		}
		if at, err := strconv.ParseInt(deadline, 10, 64); err != nil || at < now.Unix() {
			_, _, _ = dk(ctx, "rm", "-f", "-v", id)
		}
	}
}

func clip(s string) string { return strs.Truncate(strings.TrimSpace(s), 200) }
