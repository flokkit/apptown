package deployment

import (
	"strings"
	"testing"
	"time"
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
			return []byte(missing + "\t-- / --\n" + other + "\t20MiB / 1GiB\t1.00%\n"), nil, true
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

// Both whole-host figures survive the attached daemon's background reading and
// the stored capacity returned to the target page. Container sums cannot pass.
func TestAttachedCapacityCarriesWholeHostMemoryAndCPU(t *testing.T) {
	t.Parallel()
	w := newDigestWorldWith(t, "", sizedNeedYAML, nil)
	before := len(w.hosts.commands("lab"))
	c, err := backgroundCapacity(t, w.s, "lab")
	if err != nil {
		t.Fatal(err)
	}
	// Exactly one kernel script runs for the whole capacity reading. Disk's
	// separate df command and result-file cleanup are not kernel samples.
	kernelExecs := 0
	for _, command := range w.hosts.commands("lab")[before:] {
		if strings.HasPrefix(command, "exec ") && strings.Contains(command, "sh -c") && !strings.Contains(command, "df -Pk /d") {
			kernelExecs++
		}
	}
	if kernelExecs != 1 {
		t.Fatalf("capacity reading made %d kernel execs, want one", kernelExecs)
	}

	if w.lab.Origin != OriginAttached {
		t.Fatalf("not an attached host: %+v", w.lab)
	}
	if c.Memory.HostUsed == nil || !c.Memory.HostUsed.Known || c.Memory.HostUsed.Value != 2<<30 {
		t.Fatalf("host memory missing: %+v", c.Memory)
	}
	if c.CPU.HostUsed == nil || !c.CPU.HostUsed.Known || c.CPU.HostUsed.Value != 3.2 {
		t.Fatalf("host CPU missing: %+v", c.CPU)
	}
	if capacityFailure(c) != "" {
		t.Fatal(capacityFailure(c))
	}
}

// A failed refresh retains the last number and time, but also the current
// per-tenant failure. A fresh retained value stays fresh; a successful retry
// clears the failure without renewing siblings.
func TestTenantMemoryFailureRetainsReasonUntilSuccessfulRetry(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	old := TargetCapacity{TenantsAt: at, Tenants: []Tenant{{Kind: "deployment", Name: "shop/main", Used: Figure{Known: true, Value: 42, ReadAt: at}}}}
	next := TargetCapacity{TenantsAt: at.Add(time.Minute), Tenants: []Tenant{{Kind: "deployment", Name: "shop/main", Used: unknown("the daemon did not report app-shop-db")}}}
	got := capacityAt(mergeCapacity(old, next, next.TenantsAt), next.TenantsAt)
	f := got.Tenants[0].Used
	if !f.Known || f.Stale || f.Value != 42 || !f.ReadAt.Equal(at) || !strings.Contains(f.Reason, "app-shop-db") {
		t.Fatalf("failed refresh changed retained figure: %+v", f)
	}
	expired := capacityAt(got, at.Add(7*time.Hour)).Tenants[0].Used
	if expired.Known || !strings.Contains(expired.Reason, "app-shop-db") {
		t.Fatalf("expiry hid the failure: %+v", expired)
	}
	next.Tenants[0].Used = known(64)
	got = mergeCapacity(got, next, at.Add(2*time.Minute))
	f = got.Tenants[0].Used
	if !f.Known || f.Stale || f.Reason != "" || f.Value != 64 || !f.ReadAt.Equal(at.Add(2*time.Minute)) {
		t.Fatalf("retry did not refresh: %+v", f)
	}
}
