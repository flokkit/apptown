package verification

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/strs"
)

// Screenshots are AppTown's pictures of the preview a run's boot step
// booted: what a person approving the change sees of the app, taken by
// AppTown, never supplied by the agent. The browser runs where checks run, in the agent image (its Chromium headless shell,
// driven over the DevTools protocol by shoot.mjs), with egress, loading the
// preview's own address: it sees what a visitor sees, and never runs in
// AppTown's process. A page whose top frame leaves the preview's origin
// is not pictured: a picture of another site is no evidence of this one.
type Screenshots struct {
	executor Executor
	// local, on an AppTown serving itself from its own authority (the
	// desktop trial), is how a container reaches and trusts it.
	local *LocalAuthority
}

// LocalAuthority is an AppTown's own certificate authority as a capture
// needs it: its certificate (never its key), presented at LocalCAPath, and
// the shell prelude that trusts it there and leads the container's
// loopback to AppTown, the one a change chat's container runs
// (console.LocalTLSPreludeAt, given LocalCAPath).
type LocalAuthority struct {
	CACert  []byte
	Prelude string
}

// LocalCAPath is where a capture's container holds the local authority's
// certificate.
const LocalCAPath = shotsDir + "/local-ca.pem"

// NewScreenshots takes pictures through the check executor; local is the
// AppTown's own authority, nil when it has none.
func NewScreenshots(executor Executor, local *LocalAuthority) *Screenshots {
	return &Screenshots{executor: executor, local: local}
}

//go:embed shoot.mjs
var shootScript []byte

const (
	// shotsTimeout bounds the whole capture's container. Inside it the
	// driver gives each page 10 s and all of them 50 s, and keeps every
	// picture already taken when that runs out (shoot.mjs).
	shotsTimeout = 60 * time.Second
	// maxShotBytes bounds one picture: a viewport-sized PNG is tens of
	// kilobytes; a bigger one is not kept.
	maxShotBytes = 1 << 20
	// MaxNamedPages is how many pages a report may name, the home page
	// apart.
	MaxNamedPages = 3
	maxPagePath   = 200
	shotsReport   = ".apptown/screenshots.json"
	shotsDir      = "/apptown-shots"
)

// View is a browser window a page is shown in.
type View struct {
	Name          string
	Width, Height int
	Phone         bool
}

var (
	Desktop = View{Name: "desktop", Width: 1280, Height: 800}
	Phone   = View{Name: "phone", Width: 390, Height: 844, Phone: true}
)

// Shot is one picture: the page's path, the window it was shown in, and
// the PNG.
type Shot struct {
	Path string
	View string
	PNG  []byte
}

// Capture is a capture's answer: the pictures taken, the pages not
// pictured and why, or why nothing could be (Unavailable).
type Capture struct {
	Shots       []Shot
	Missing     []string
	Unavailable string
}

// pagePath is what a named page may be: a path on the preview, starting
// with one "/", in URL-path and query characters only.
var pagePath = regexp.MustCompile(`^/[A-Za-z0-9\-._~%!$&'()*+,;=:@/?]*$`)

// NamedPages reads the pages a report names on its first `Pages:` line
// (at most MaxNamedPages, the home page apart), and the entries it
// ignored: anything that is not a path on the preview itself.
func NamedPages(report string) (pages, ignored []string) {
	for _, line := range strings.Split(report, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*>"))
		if len(line) < 6 || !strings.EqualFold(line[:6], "pages:") {
			continue
		}
		for _, p := range strings.Fields(line[6:]) {
			p = strings.Trim(p, "`,")
			switch {
			case p == "" || p == "/":
			case len(p) > maxPagePath || !pagePath.MatchString(p) || strings.HasPrefix(p, "//"):
				ignored = append(ignored, strs.Truncate(p, 80))
			case len(pages) >= MaxNamedPages:
				ignored = append(ignored, p)
			default:
				pages = append(pages, p)
			}
		}
		break
	}
	return pages, ignored
}

// shotsCommand is the container's command: the local authority's prelude
// when there is one, then the driver, with every page an argument of its
// own, never part of the script.
func (s *Screenshots) shotsCommand() string {
	cmd := ""
	if s.local != nil {
		cmd = s.local.Prelude + "\n"
	}
	return cmd + `exec node ` + shotsDir + `/shoot.mjs "$@"`
}

type want struct {
	name string
	path string
	view View
}

// request is what the executor runs for the pictures of base's pages, in
// tree.
func (s *Screenshots) request(tree string, base *url.URL, wants []want) execution.Request {
	argv := []string{"sh", "-c", s.shotsCommand(), "shoot", "/work/" + shotsReport}
	for _, w := range wants {
		page := *base
		page.Path, page.RawPath, page.RawQuery, page.Fragment = "", "", "", ""
		phone := "0"
		if w.view.Phone {
			phone = "1"
		}
		argv = append(argv, w.name, fmt.Sprint(w.view.Width), fmt.Sprint(w.view.Height), phone, strings.TrimSuffix(page.String(), "/")+w.path)
	}
	files := []execution.File{{Path: shotsDir + "/shoot.mjs", Content: shootScript, Mode: 0o444}}
	if s.local != nil {
		files = append(files, execution.File{Path: LocalCAPath, Content: s.local.CACert, Mode: 0o444})
	}
	return execution.Request{
		Workspace: tree, Image: s.executor.AgentImage, Argv: argv, NoEntrypoint: true,
		Timeout: shotsTimeout, Report: shotsReport, Files: files,
		Name: execution.UniqueName("forge-shots"),
	}
}

// Take pictures the preview at base (its public address, as the boot
// recorded it): the home page in a desktop and a phone window, and each
// named page in a desktop window.
func (s *Screenshots) Take(ctx context.Context, base string, pages []string) Capture {
	if s == nil || s.executor.Run == nil {
		return Capture{Unavailable: "AppTown has no check executor to take screenshots with"}
	}
	if s.executor.AgentImage == "" {
		return Capture{Unavailable: "the check executor names no agent image, which carries the browser"}
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Capture{Unavailable: fmt.Sprintf("the preview's address %q is not a web address", base)}
	}
	wants := []want{{"1-desktop", "/", Desktop}, {"1-phone", "/", Phone}}
	for i, p := range pages {
		wants = append(wants, want{fmt.Sprintf("%d-desktop", i+2), p, Desktop})
	}
	tree, err := os.MkdirTemp("", "apptown-shots-")
	if err != nil {
		return Capture{Unavailable: "a folder for the screenshots: " + err.Error()}
	}
	defer os.RemoveAll(tree)
	// The container may run as another user: its report folder is its own
	// to write.
	if err := os.Mkdir(filepath.Join(tree, ".apptown"), 0o777); err != nil {
		return Capture{Unavailable: "a folder for the screenshots: " + err.Error()}
	}
	_ = os.Chmod(filepath.Join(tree, ".apptown"), 0o777)
	out := s.executor.Run(ctx, s.request(tree, u, wants))
	if !out.Answered() {
		why := strs.FirstNonBlank(errText(out.RunErr), string(out.Termination))
		return Capture{Unavailable: "the browser did not finish: " + strs.Truncate(why+" "+strings.TrimSpace(string(out.Output)), 300)}
	}
	if out.ReportErr != nil || len(out.Report) == 0 {
		why := strs.FirstNonBlank(errText(out.ReportErr), strings.TrimSpace(string(out.Output)), "no report")
		return Capture{Unavailable: "the browser wrote no pictures: " + strs.Truncate(why, 300)}
	}
	return readCapture(out.Report, wants)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// readCapture reads the driver's report against what was asked: a page
// the report does not answer is missing, never taken.
func readCapture(report []byte, wants []want) Capture {
	var r struct {
		Pages []struct {
			Name     string `json:"name"`
			FinalURL string `json:"finalUrl"`
			PNG      string `json:"png"`
			Reason   string `json:"reason"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(report, &r); err != nil {
		return Capture{Unavailable: "reading the pictures: " + err.Error()}
	}
	byName := map[string]int{}
	for i, p := range r.Pages {
		byName[p.Name] = i
	}
	var c Capture
	for _, w := range wants {
		label := w.path + " · " + w.view.Name
		i, ok := byName[w.name]
		if !ok {
			c.Missing = append(c.Missing, label+": not answered")
			continue
		}
		p := r.Pages[i]
		if p.PNG == "" {
			c.Missing = append(c.Missing, label+": "+strs.Truncate(strs.FirstNonBlank(p.Reason, "not taken"), 200))
			continue
		}
		png, err := base64.StdEncoding.DecodeString(p.PNG)
		switch {
		case err != nil:
			c.Missing = append(c.Missing, label+": not a picture")
		case len(png) > maxShotBytes:
			c.Missing = append(c.Missing, fmt.Sprintf("%s: %d bytes, over the %d limit", label, len(png), maxShotBytes))
		case !strings.HasPrefix(string(png), "\x89PNG\r\n\x1a\n"):
			c.Missing = append(c.Missing, label+": not a PNG")
		default:
			c.Shots = append(c.Shots, Shot{Path: w.path, View: w.view.Name, PNG: png})
		}
	}
	if len(c.Shots) == 0 {
		c.Unavailable = "no page could be pictured: " + strings.Join(c.Missing, "; ")
	}
	return c
}
