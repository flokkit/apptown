package dind

import "testing"

// Only Docker's own answer that an object does not exist is absence; a
// daemon or CLI that could not answer is not.
func TestMissingIsOnlyDockersOwnAnswer(t *testing.T) {
	t.Parallel()
	for out, want := range map[string]bool{
		"Error response from daemon: No such container: x":                                         true,
		"Error response from daemon: network x not found":                                          true,
		"Error response from daemon: get x: no such volume":                                        true,
		"[]\nError: No such object: x":                                                             true,
		"error during connect: dial unix /var/run/docker.sock: connect: no such file or directory": false,
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock":                       false,
		"docker: command not found":                                                                false,
		"Error response from daemon: conflict: unable to remove":                                   false,
	} {
		if got := Missing([]byte(out)); got != want {
			t.Errorf("Missing(%q) = %v, want %v", out, got, want)
		}
	}
}
