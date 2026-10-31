package deployment

import (
	"strings"
	"testing"
)

// A container that the host lists as running can have no stats yet. Its row
// must not discard memory readings from other containers on the same host.
func TestRunningContainerWithoutStatsLeavesHostMemoryKnown(t *testing.T) {
	t.Parallel()
	w := newDigestWorldWith(t, "", sizedNeedYAML, nil)
	b := w.publish(t)
	if rel, err := w.promote(t, b.ID, "prod", w.lab); err != nil || rel.State != Released {
		t.Fatalf("release: %+v %v", rel, err)
	}
	missing := w.names(t, "prod").Service("postgres")
	d := w.hosts.daemon("lab")
	other := w.names(t, "prod").App()
	if !d.Running(missing) || !d.Running(other) {
		t.Fatal("deployment containers are not running")
	}
	d.SetReply(func(args []string) ([]byte, error, bool) {
		switch {
		case len(args) > 1 && args[0] == "ps" && args[1] == "-q":
			return []byte(missing + "\n" + other + "\n"), nil, true
		case len(args) > 4 && args[0] == "inspect" && strings.Contains(args[4], "CpuQuota"):
			return []byte("/" + missing + "\t0\t0\t0\t0\t\tenv\t\n/" + other + "\t0\t0\t0\t0\t\tenv\t\n"), nil, true
		case len(args) > 0 && args[0] == "stats":
			return []byte(missing + "\t-- / --\n" + other + "\t20MiB / 1GiB\n"), nil, true
		}
		return nil, nil, false
	})
	c, err := backgroundCapacity(t, w.s, "lab")
	if err != nil {
		t.Fatal(err)
	}
	use := c.Memory.InUse
	if use == nil || !use.AppTown.Known || !use.Other.Known || use.AppTown.Value+use.Other.Value != 20<<20 {
		t.Fatalf("no-stats container discarded host memory: %+v", use)
	}
}
