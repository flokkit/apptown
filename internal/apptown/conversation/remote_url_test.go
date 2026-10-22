package conversation

import (
	"slices"
	"strings"
	"testing"
)

// A chat's Git remote works whether AppTown's address is http (a local
// container) or https (a worker reaching AppTown), and its token travels
// as a header for that remote only, never in the URL.
func TestAChatsGitRemoteSendsItsTokenOutsideTheURL(t *testing.T) {
	for base, want := range map[string]string{
		"http://host.docker.internal:7778":    "http://host.docker.internal:7778/git/app",
		"https://forge.example:8443/":         "https://forge.example:8443/git/app",
		"https://forge.example/prefix":        "https://forge.example/prefix/git/app",
		"ftp://forge.example":                 "",
		"host.docker.internal:7778/no-scheme": "",
	} {
		env := mainRemote(base, "tok", "app")
		if want == "" {
			if env != nil {
				t.Errorf("%s: %q, want nothing", base, env)
			}
			continue
		}
		wantEnv := []string{"APPTOWN_MAIN_REMOTE=" + want, "GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http." + want + ".extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Bearer tok"}
		if !slices.Equal(env, wantEnv) {
			t.Errorf("%s: %q, want %q", base, env, wantEnv)
		}
		if strings.Contains(env[0], "tok") {
			t.Errorf("%s: the remote carries the token: %s", base, env[0])
		}
	}
}
