package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy/appcontract"
)

// pageClient is a daemon whose app container is published at an httptest
// server's port, so the boot's own probes reach a real HTTP app.
type pageClient struct{ port string }

func (c pageClient) Run(_ context.Context, _ string, req Command) ([]byte, error) {
	if out, ok := roomyDisk(req.Args); ok {
		return out, nil
	}
	switch {
	case len(req.Args) > 0 && req.Args[0] == "port":
		return []byte("127.0.0.1:" + c.port + "\n"), nil
	case len(req.Args) > 0 && req.Args[0] == "image":
		return []byte("\n"), nil
	}
	return nil, nil
}

// A gate's boot asks the page a visitor opens, not only the readiness
// path: an app that answers /healthz 200 and refuses / (a Rails host list
// that never took APPTOWN_ORIGINS; /up is exempt from it) would otherwise pass
// the preview, the deploy and the runtime check. The visitor's host name is sent, so an app that trusts it
// is not refused.
func TestAGateBootFailsAnAppThatRefusesItsFrontPage(t *testing.T) {
	t.Parallel()
	const visitor = "shop.apps.example.com"
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/healthz":
			w.WriteHeader(http.StatusOK)
		case r.Host != visitor:
			w.WriteHeader(http.StatusForbidden) // Blocked hosts
		case r.URL.Path == "/":
			http.Redirect(w, r, "https://"+visitor+"/dashboard", http.StatusFound)
		case r.URL.Path == "/dashboard" && r.Header.Get("X-Forwarded-Proto") == "https":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(app.Close)
	u, _ := url.Parse(app.URL)
	c, err := appcontract.Parse([]byte("version: 1\napp:\n  web:\n    image: example/shop:1\n    ports: [\"8080\"]\n    public: true\n    health: {readiness: {http: /healthz}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	boot := func(pageCheck bool, host string) (BootResult, error) {
		return NewHost(Dependencies{Secrets: liveSecrets{}, Needs: liveNeeds{}, Integrations: liveContributions{"{}"}, Routes: liveRoutes{}, Probe: upProbe{}}).Boot(context.Background(), BootRequest{Docker: Scope{Client: pageClient{u.Port()}},
			Name: "forge-preview-run-4", StackID: "pv4", Workspace: t.TempDir(), Slug: "shop", Deployment: "preview", Probe: time.Second,
			Scratch: t.TempDir(), PageCheck: pageCheck, VisitorHost: host}, c)
	}
	// The check is witnessed whichever way it goes: the path that
	// answered, the host it asked as, the status and the verdict.
	refused, err := boot(true, "")
	if be := AsBootError(err); err == nil || be.Phase != PhasePage || !strings.Contains(be.Error(), "403") {
		t.Fatalf("a refused front page passed the gate's boot: %v", err)
	}
	if p := refused.Page; p == nil || p.Passed || p.Status != http.StatusForbidden || p.Path != "/" || p.Host != u.Host || !strings.Contains(p.Verdict, "refused") {
		t.Fatalf("the refusal is not witnessed: %+v", refused.Page)
	}
	served, err := boot(true, visitor)
	if err != nil {
		t.Fatalf("an app that serves its visitors (via a same-origin redirect) failed: %v", err)
	}
	if p := served.Page; p == nil || !p.Passed || p.Status != http.StatusOK || p.Path != "/dashboard" || p.Host != visitor || p.Verdict == "" {
		t.Fatalf("a passing front page left no witness: %+v", served.Page)
	}
	if tryIt, err := boot(false, ""); err != nil || tryIt.Page != nil {
		t.Fatalf("the try-it boot asked the gate's page question: %v %+v", err, tryIt.Page)
	}
}

// pathProbe answers every probe but one path's.
type pathProbe struct{ refuses string }

func (p pathProbe) Check(ctx context.Context, _ Scope, _, _ string, _ time.Duration, probe RenderedProbe) (bool, string) {
	return probe.Path != p.refuses, ""
}

// A static site that answers "/" but not /_apptown/env.js is not running the
// forge's file server configuration (an image built before the image
// carried it runs the file server's stock page): the boot fails saying so, rather than
// calling the stock page the site.
func TestAStaticSiteBootRequiresTheForgesFileServer(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("version: 1\napp:\n  web:\n    static: site\n    ports: [\"8080\"]\n    public: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	boot := func(probe Prober) error {
		_, err := NewHost(Dependencies{Secrets: liveSecrets{}, Needs: liveNeeds{}, Integrations: liveContributions{"{}"}, Routes: liveRoutes{}, Probe: probe}).Boot(context.Background(), BootRequest{Docker: Scope{Client: pageClient{"1"}},
			Name: "forge-preview-run-5", StackID: "pv5", Workspace: t.TempDir(), Slug: "site", Deployment: "preview", Probe: time.Second, Scratch: t.TempDir()}, c)
		return err
	}
	if be := AsBootError(boot(pathProbe{refuses: "/_apptown/env.js"})); be == nil || be.Phase != PhaseProbe || !strings.Contains(be.Error(), "does not answer /_apptown/env.js") {
		t.Fatalf("a file server without AppTown's configuration passed: %v", be)
	}
	if err := boot(pathProbe{}); err != nil {
		t.Fatalf("AppTown's file server failed its boot: %v", err)
	}
}

// witnessProbe is a static site whose file server answers serves at
// ReleasePath and anything else at every other path; it notes each probe.
type witnessProbe struct {
	serves string
	asked  *[]RenderedProbe
}

func (p witnessProbe) Check(ctx context.Context, _ Scope, _, _ string, _ time.Duration, probe RenderedProbe) (bool, string) {
	*p.asked = append(*p.asked, probe)
	return probe.Path != appcontract.ReleasePath || probe.Body == p.serves, ""
}

// A standing static deployment is told its release, as App Platform's is
// (APPTOWN_RELEASE, set by AppTown after anything the contract says), and
// a self-serving image is witnessed answering it at /_apptown/release; one
// answering another release fails. An image of the earlier form, and a
// preview with no revision, keep the /_apptown/env.js check.
func TestAStandingStaticSiteIsWitnessedServingItsRelease(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("version: 1\napp:\n  web:\n    static: site\n    ports: [\"8080\"]\n    public: true\n    environment:\n      APPTOWN_RELEASE: web@apptownd\n"))
	if err != nil {
		t.Fatal(err)
	}
	const rev = "774273461cf5561e50896b409942cc67d14361ca"
	for _, tc := range []struct {
		name, serves, revision string
		recorded               *RecordedImages
		wantRelease            string // what the boot tells and witnesses; "" asks env.js
		wantErr                bool
	}{
		{name: "self-serving", serves: "web@" + rev, revision: rev, recorded: &RecordedImages{Parts: map[string]string{"web": "sha256:web"}, SelfServing: true}, wantRelease: "web@" + rev},
		{name: "built from source", serves: "web@" + rev, revision: rev, wantRelease: "web@" + rev},
		{name: "another release", serves: "web@0ld", revision: rev, recorded: &RecordedImages{Parts: map[string]string{"web": "sha256:web"}, SelfServing: true}, wantRelease: "web@" + rev, wantErr: true},
		{name: "earlier form", serves: "", revision: rev, recorded: &RecordedImages{Parts: map[string]string{"web": "sha256:web"}}},
		{name: "preview", serves: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{reply: func(req Command) ([]byte, error) {
				switch req.Args[0] {
				case "port":
					return []byte("127.0.0.1:45678\n"), nil
				case "image":
					return []byte(req.Args[len(req.Args)-1] + "\n"), nil
				}
				return nil, nil
			}}
			var asked []RenderedProbe
			_, err := NewHost(Dependencies{Secrets: liveSecrets{}, Needs: liveNeeds{}, Integrations: liveContributions{"{}"}, Routes: liveRoutes{}, Probe: witnessProbe{serves: tc.serves, asked: &asked}}).Boot(context.Background(), BootRequest{Docker: Scope{Client: client},
				Name: "forge-env-site-main", StackID: "env-site-main", Workspace: t.TempDir(), Slug: "site", Deployment: "main", Probe: time.Second, Scratch: t.TempDir(),
				Revision: tc.revision, Recorded: tc.recorded}, c)
			if be := AsBootError(err); tc.wantErr != (err != nil) || (tc.wantErr && (be.Phase != PhaseProbe || !strings.Contains(be.Error(), appcontract.ReleasePath))) {
				t.Fatalf("boot: %v", err)
			}
			var told string
			for _, args := range client.args {
				if args[0] != "run" {
					continue
				}
				for i, arg := range args {
					if arg == "-e" && i+1 < len(args) && strings.HasPrefix(args[i+1], appcontract.ReleaseEnvVar+"=") {
						told = strings.TrimPrefix(args[i+1], appcontract.ReleaseEnvVar+"=")
					}
				}
			}
			var witnessed, envJS bool
			for _, p := range asked {
				witnessed = witnessed || (p.Path == appcontract.ReleasePath && p.Body == tc.wantRelease && p.Expect == 200)
				envJS = envJS || p.Path == "/_apptown/env.js"
			}
			switch {
			case tc.wantRelease != "" && (told != tc.wantRelease || !witnessed || envJS):
				t.Fatalf("told %q, witnessed %v, env.js %v: %+v", told, witnessed, envJS, asked)
			case tc.wantRelease == "" && (witnessed || !envJS):
				t.Fatalf("an image that is not witnessed kept no env.js check: %+v", asked)
			case tc.revision == "" && told != "web@apptownd":
				t.Fatalf("a preview was told a release: %q", told)
			}
		})
	}
}

// A probe that asks for a body is up only when the answer is exactly that
// body: a file server answering another release is not this one.
func TestAProbeWithABodyNeedsExactlyThatAnswer(t *testing.T) {
	t.Parallel()
	served := "web@new"
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(served)) }))
	t.Cleanup(app.Close)
	dk := Scope{Client: &fakeClient{out: "true"}}
	probe := &Probe{}
	if up, _ := probe.check(t.Context(), dk, "app", app.URL, 2*time.Second, RenderedProbe{Path: appcontract.ReleasePath, Expect: 200, Body: "web@new"}, answerOnce); !up {
		t.Fatal("the exact answer was not up")
	}
	for _, want := range []string{"web@ne", "web@new2", "web@old"} {
		if up, _ := probe.check(t.Context(), dk, "app", app.URL, 1100*time.Millisecond, RenderedProbe{Path: appcontract.ReleasePath, Expect: 200, Body: want}, answerOnce); up {
			t.Fatalf("%q answered for %q", served, want)
		}
	}
}

// A static site's front page passes when it is served, whatever it holds:
// a page with no script is still a site. A missing index.html fails.
func TestAStaticFrontPageIsJudgedByItsAnswerNotItsMarkup(t *testing.T) {
	t.Parallel()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing/" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("<h1>A plain page</h1>"))
	}))
	t.Cleanup(page.Close)
	if status, ok, why := staticPageServes(page.URL + "/"); !ok || status != 200 {
		t.Fatalf("a plain page: %d %v %s", status, ok, why)
	}
	if status, ok, why := staticPageServes(page.URL + "/missing/"); ok || status != 404 || !strings.Contains(why, "index.html") {
		t.Fatalf("a missing page: %d %v %s", status, ok, why)
	}
}
