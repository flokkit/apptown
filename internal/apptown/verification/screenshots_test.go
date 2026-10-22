package verification

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/execution"
)

// A report names pages on the preview only: a path, never another host, a
// scheme, or more than three.
func TestNamedPagesAreOnlyPathsOnThePreview(t *testing.T) {
	report := "# Change\n\nDid things.\n\n- Pages: /a `/b?x=1`, //evil.example/x http://evil.example /c /d /e\n\nPages: /later\n"
	pages, ignored := NamedPages(report)
	if want := []string{"/a", "/b?x=1", "/c"}; !slices.Equal(pages, want) {
		t.Fatalf("pages %v, want %v", pages, want)
	}
	for _, bad := range []string{"//evil.example/x", "http://evil.example", "/d", "/e"} {
		if !slices.Contains(ignored, bad) {
			t.Fatalf("%q was not ignored: %v", bad, ignored)
		}
	}
	if p, i := NamedPages("Pages: /ok \"/quoted\" /x<y>"); !slices.Equal(p, []string{"/ok"}) || len(i) != 2 {
		t.Fatalf("characters outside a URL path were taken: %v %v", p, i)
	}
	if p, _ := NamedPages("Pages: index.html orders/1"); !slices.Equal(p, []string{"/index.html", "/orders/1"}) {
		t.Fatalf("paths written without their slash: %v", p)
	}
	if p, i := NamedPages("no pages line"); p != nil || i != nil {
		t.Fatalf("a report with no Pages line named %v %v", p, i)
	}
}

// What the executor is asked to run: the agent image with egress and a
// fresh tree, the fixed driver as a read-only file, and every page an
// argument of its own, never part of the script; the local authority only
// on an AppTown that has one.
func TestScreenshotsAskTheExecutorForTheDriverWithPagesAsArguments(t *testing.T) {
	var got execution.Request
	run := func(_ context.Context, req execution.Request) execution.Outcome {
		got = req
		return execution.Outcome{Termination: execution.Exited, ExitCode: 0, Report: []byte(`{"pages":[]}`)}
	}
	s := NewScreenshots(Executor{Run: run, AgentImage: "agent:1"}, nil)
	s.Take(context.Background(), "https://change-1.preview.example.com/ignored?q=1#f", []string{"/a b'\"$(x)"})
	if got.Image != "agent:1" || got.Network != "" || got.Workspace == "" || got.Report != shotsReport || !strings.HasPrefix(got.Name, "apptown-shots-") {
		t.Fatalf("request: image %q network %q workspace %q report %q name %q", got.Image, got.Network, got.Workspace, got.Report, got.Name)
	}
	if len(got.Argv) < 4 || got.Argv[0] != "sh" || got.Argv[1] != "-c" || strings.Contains(got.Argv[2], "preview.example.com") || strings.Contains(got.Argv[2], "$(x)") {
		t.Fatalf("the script carries a page: %q", got.Argv)
	}
	args := got.Argv[3:]
	want := []string{"shoot", "/work/" + shotsReport,
		"1-desktop", "1280", "800", "0", "https://change-1.preview.example.com/",
		"1-phone", "390", "844", "1", "https://change-1.preview.example.com/",
		"2-desktop", "1280", "800", "0", "https://change-1.preview.example.com/a b'\"$(x)"}
	if !slices.Equal(args, want) {
		t.Fatalf("arguments\n got %q\nwant %q", args, want)
	}
	if len(got.Files) != 1 || got.Files[0].Path != shotsDir+"/shoot.mjs" || string(got.Files[0].Content) != string(shootScript) {
		t.Fatalf("files: %+v", got.Files)
	}
	if err := execution.ValidFiles(got.Files); err != nil {
		t.Fatal(err)
	}

	trial := &LocalAuthority{CACert: []byte("-----BEGIN CERTIFICATE-----\n"), Prelude: "trust " + LocalCAPath}
	NewScreenshots(Executor{Run: run, AgentImage: "agent:1"}, trial).Take(context.Background(), "https://change-1.preview.localhost:7777/", nil)
	if len(got.Files) != 2 || got.Files[1].Path != LocalCAPath || !strings.HasPrefix(got.Argv[2], "trust "+LocalCAPath+"\n") || strings.Contains(got.Argv[2], "ignore-certificate") {
		t.Fatalf("the trial's authority: files %+v script %q", got.Files, got.Argv[2])
	}
}

func shotsReportOf(t *testing.T, pages ...map[string]string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"pages": pages})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Only pictures the driver answered for, as PNGs within the bound, are
// taken; everything else is named, and nothing taken is unavailable.
func TestScreenshotsReadOnlyWhatWasAskedAndTaken(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nimage"))
	big := base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, maxShotBytes)...))
	base, _ := url.Parse("https://p.example/")
	s := NewScreenshots(Executor{AgentImage: "agent"}, nil)
	wants := []want{{"1-desktop", "/", Desktop}, {"1-phone", "/", Phone}, {"2-desktop", "/a", Desktop}, {"3-desktop", "/b", Desktop}, {"4-desktop", "/c", Desktop}}
	_ = s.request(t.TempDir(), base, wants)
	c := readCapture(shotsReportOf(t,
		map[string]string{"name": "1-desktop", "png": png},
		map[string]string{"name": "1-phone", "png": big},
		map[string]string{"name": "2-desktop", "reason": "left the preview for https://elsewhere.example/"},
		map[string]string{"name": "3-desktop", "png": base64.StdEncoding.EncodeToString([]byte("<html>"))},
		map[string]string{"name": "9-desktop", "png": png},
	), wants)
	if len(c.Shots) != 1 || c.Shots[0].Path != "/" || c.Shots[0].View != "desktop" || c.Unavailable != "" {
		t.Fatalf("shots %+v unavailable %q", c.Shots, c.Unavailable)
	}
	for _, m := range []string{"/ · phone: ", "/a · desktop: left the preview for https://elsewhere.example/", "/b · desktop: not a PNG", "/c · desktop: not answered"} {
		if !slices.ContainsFunc(c.Missing, func(s string) bool { return strings.HasPrefix(s, m) }) {
			t.Fatalf("missing %q in %v", m, c.Missing)
		}
	}
	if none := readCapture(shotsReportOf(t), wants[:1]); none.Unavailable == "" || len(none.Shots) != 0 {
		t.Fatalf("nothing taken is not unavailable: %+v", none)
	}
}

// A browser that never answered, or answered without a report, took no
// pictures: unavailable, with why.
func TestScreenshotsThatDidNotRunAreUnavailable(t *testing.T) {
	for name, out := range map[string]execution.Outcome{
		"timed out": {Termination: execution.TimedOut, RunErr: errors.New("timed out")},
		"no report": {Termination: execution.Exited, ExitCode: 0, ReportErr: errors.New("missing")},
		"refused":   {Termination: execution.LaunchFailed, RunErr: errors.New("the worker refused the check")},
	} {
		s := NewScreenshots(Executor{Run: func(context.Context, execution.Request) execution.Outcome { return out }, AgentImage: "agent"}, nil)
		if c := s.Take(context.Background(), "https://p.example/", nil); c.Unavailable == "" || len(c.Shots) != 0 {
			t.Fatalf("%s: %+v", name, c)
		}
	}
	if c := NewScreenshots(Executor{AgentImage: "agent"}, nil).Take(context.Background(), "https://p.example/", nil); c.Unavailable == "" {
		t.Fatal("no executor took pictures")
	}
	if c := NewScreenshots(Executor{Run: func(context.Context, execution.Request) execution.Outcome { panic("ran") }, AgentImage: "a"}, nil).Take(context.Background(), "javascript:alert(1)", nil); c.Unavailable == "" {
		t.Fatal("a non-web address was opened")
	}
}
