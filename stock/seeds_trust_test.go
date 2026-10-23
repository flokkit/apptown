package stock

import (
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every shipped Dockerfile (the twelve starters and the agent image) takes
// the extra certificates AppTown gives it (internal/trust): each RUN that
// downloads mounts the build secret and runs it first, and each starter's
// runtime command that runs the app in an image with a shell runs
// APPTOWN_TRUST first. The caddy file servers and distroless go-small run no
// shell (container-image guidance).
func TestShippedDockerfilesTakeTheExtraCertificates(t *testing.T) {
	t.Parallel()
	const mount = `RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; `
	fetches := regexp.MustCompile(`\b(apt-get|apk|npm (ci|install)|pip install|uv (sync|tool)|mvn|cargo install|go (build|install)|dotnet publish|bundle install|composer install|curl|pi install)\b`)
	files := map[string]string{}
	seeds, err := fs.Glob(FS, "seeds/*/Dockerfile")
	if err != nil || len(seeds) != 12 {
		t.Fatalf("%d starter Dockerfiles: %v", len(seeds), err)
	}
	for _, p := range seeds {
		b, err := fs.ReadFile(FS, p)
		if err != nil {
			t.Fatal(err)
		}
		files[p] = string(b)
	}
	agent, err := os.ReadFile("../deploy/apptown-agent/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	files["deploy/apptown-agent/Dockerfile"] = string(agent)
	for p, body := range files {
		// Instructions as Docker reads them: continued lines joined.
		body = strings.ReplaceAll(body, "\\\n", " ")
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, "RUN ") && fetches.MatchString(line) && !strings.HasPrefix(line, mount) {
				t.Errorf("%s: a RUN that downloads without the certificates: %s", p, line)
			}
			if strings.HasPrefix(line, "CMD ") && strings.HasPrefix(p, "seeds/") && !strings.Contains(line, "caddy") && line != `CMD ["/app"]` &&
				!strings.HasPrefix(line, `CMD ["sh", "-c", "eval \"${APPTOWN_TRUST:-}\"; `) {
				t.Errorf("%s: the app's command does not run the certificates first: %s", p, line)
			}
		}
	}
}
