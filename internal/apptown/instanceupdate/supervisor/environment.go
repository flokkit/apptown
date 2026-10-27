package supervisor

import (
	"errors"
	"os"
	"regexp"
	"strings"
)

var environmentKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// Managed environment files deliberately use literal assignments, not shell or
// systemd quoting/expansion. That lets enrollment bind the exact effective daemon
// configuration without introducing another command/environment interpreter.
func registeredEnvironment(r Registration) (map[string]string, error) {
	if err := compareFile(r.EnvironmentFile, r.EnvironmentSHA256); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(r.EnvironmentFile)
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, errors.New("managed environment is too large")
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !environmentKey.MatchString(key) || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\\\"'`$") {
			return nil, errors.New("managed environment requires literal KEY=value assignments")
		}
		if _, exists := values[key]; exists {
			return nil, errors.New("managed environment repeats a key")
		}
		values[key] = value
	}
	if values["APPTOWN_DATA"] != r.DataDir || values["APPTOWN_UPDATE_SUPERVISOR_SOCKET"] != r.StateDir+"/control.sock" {
		return nil, errors.New("managed environment does not bind registered data and supervisor paths")
	}
	return values, nil
}
func effectiveEnvironment(r Registration, env []string) error {
	want, err := registeredEnvironment(r)
	if err != nil {
		return err
	}
	actual := map[string]string{}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			return errors.New("invalid effective environment")
		}
		if _, exists := actual[key]; exists {
			return errors.New("effective environment repeats a key")
		}
		actual[key] = value
	}
	for key, value := range want {
		if got, ok := actual[key]; !ok || got != value {
			return errors.New("effective managed environment differs from registration")
		}
	}
	for key := range actual {
		if strings.HasPrefix(key, "APPTOWN_") || strings.HasPrefix(key, "DOCKER_") {
			if _, ok := want[key]; !ok {
				return errors.New("unregistered daemon configuration is present")
			}
		}
	}
	return nil
}
