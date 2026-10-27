package supervisor

import (
	"os"
	"path/filepath"
	"testing"
)

func environmentFixture(t *testing.T) (Registration, []string) {
	t.Helper()
	s := testStore(t)
	r := s.registration
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r.EnvironmentFile = filepath.Join(dir, "daemon.env")
	lines := []string{"APPTOWN_DATA=" + r.DataDir, "APPTOWN_UPDATE_SUPERVISOR_SOCKET=" + r.StateDir + "/control.sock", "APPTOWN_DB=postgres://localhost/registered"}
	raw := ""
	for _, line := range lines {
		raw += line + "\n"
	}
	if err := os.WriteFile(r.EnvironmentFile, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	r.EnvironmentSHA256, err = hashFile(r.EnvironmentFile)
	if err != nil {
		t.Fatal(err)
	}
	return r, lines
}
func TestEffectiveManagedConfigurationMustMatchEnrollment(t *testing.T) {
	r, env := environmentFixture(t)
	if err := effectiveEnvironment(r, append(env, "PATH=/usr/bin")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{env[:2], append(append([]string{}, env...), "APPTOWN_VERIFY_ONLY=false"), append(append([]string{}, env...), "DOCKER_HOST=tcp://other"), append(append([]string{}, env...), env[0]), {"APPTOWN_DATA=/other", "APPTOWN_UPDATE_SUPERVISOR_SOCKET=" + r.StateDir + "/control.sock", "APPTOWN_DB=postgres://localhost/registered"}} {
		if err := effectiveEnvironment(r, bad); err == nil {
			t.Fatal("accepted effective configuration drift", bad)
		}
	}
	if err := os.WriteFile(r.EnvironmentFile, []byte("APPTOWN_DATA=/other\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := effectiveEnvironment(r, env); err == nil {
		t.Fatal("accepted changed environment file")
	}
}
func TestManagedUnitBindsStableGuardAndNoArbitraryCommands(t *testing.T) {
	r, _ := environmentFixture(t)
	body, err := ManagedUnit(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 {
		t.Fatal("empty unit")
	}
	r.StateDir += " %n"
	if _, err := ManagedUnit(r); err == nil {
		t.Fatal("accepted systemd specifier in registered path")
	}
}
