package deployment

import "testing"

// Two deploys of one environment share one port. The first reservation wins
// even when each caller offers a different free-port candidate.
func TestDeploymentPortReservedOnce(t *testing.T) {
	t.Parallel()
	f, dep := deploymentOutcomeFixture(t)
	ports := make([]int, 6)
	results := race(6, func(i int) int {
		p, err := NewDeployments(f.db).ReserveHostPort(t.Context(), dep.ProjectID, dep.Name, "web", 41000+i)
		if err != nil {
			return -1
		}
		ports[i] = p
		return 0
	})
	if count(results, -1) != 0 {
		t.Fatalf("no reservation may fail: %v", results)
	}
	for _, p := range ports[1:] {
		if p != ports[0] || p == 0 {
			t.Fatalf("every deploy of one environment must see one port: %v", ports)
		}
	}
	if envs, err := NewDeployments(f.db).ForProject(t.Context(), dep.ProjectID); err != nil || len(envs) != 1 {
		t.Fatalf("deployments after the race: %d %v", len(envs), err)
	}
}
