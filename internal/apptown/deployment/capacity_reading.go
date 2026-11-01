package deployment

import "time"

const liveFresh = 15 * time.Minute
const liveUnknown = 6 * time.Hour
const sizeUnknown = 7 * 24 * time.Hour

// Each successful figure owns its timestamp. A partial check cannot renew an
// older figure by refreshing its wrapper or a sibling resource.
func mergeFigure(old, next Figure, at time.Time) Figure {
	if next.Known {
		if next.ReadAt.IsZero() {
			next.ReadAt = at
		}
		return next
	}
	if old.Known {
		return old
	}
	return next
}
func mergeResource(old, next *ResourceCapacity, at time.Time) *ResourceCapacity {
	if next == nil {
		return nil
	}
	n := *next
	if old == nil {
		old = &ResourceCapacity{}
	}
	n.Total = mergeFigure(old.Total, n.Total, at)
	if !n.Promised.Sum.AppTown.Known && !n.Promised.Sum.Other.Known && old.Promised.Sum.AppTown.Known && old.Promised.Sum.Other.Known {
		n.Promised = old.Promised
	}
	n.Promised.Sum.AppTown = mergeFigure(old.Promised.Sum.AppTown, n.Promised.Sum.AppTown, at)
	n.Promised.Sum.Other = mergeFigure(old.Promised.Sum.Other, n.Promised.Sum.Other, at)
	if n.InUse != nil {
		v := *n.InUse
		var prev Split
		if old.InUse != nil {
			prev = *old.InUse
		}
		v.AppTown = mergeFigure(prev.AppTown, v.AppTown, at)
		v.Other = mergeFigure(prev.Other, v.Other, at)
		n.InUse = &v
	}
	if n.HostUsed == nil {
		n.HostUsed = old.HostUsed
	} else {
		var prev Figure
		if old.HostUsed != nil {
			prev = *old.HostUsed
		}
		v := mergeFigure(prev, *n.HostUsed, at)
		n.HostUsed = &v
	}
	n.Overcommitted = false
	overcommit(&n)
	return &n
}
func mergeCapacity(old, next TargetCapacity, at time.Time) TargetCapacity {
	next.Tenants = append([]Tenant(nil), next.Tenants...)
	next.Parts = append([]ElasticPart(nil), next.Parts...)
	if next.TenantsAt.IsZero() && !old.TenantsAt.IsZero() {
		next.Tenants = old.Tenants
		next.TenantsAt = old.TenantsAt
	}
	next.Memory = mergeResource(old.Memory, next.Memory, at)
	next.CPU = mergeResource(old.CPU, next.CPU, at)
	if next.Disk != nil {
		n := *next.Disk
		var prev DiskCapacity
		if old.Disk != nil {
			prev = *old.Disk
		}
		n.Total = mergeFigure(prev.Total, n.Total, n.ReadAt)
		n.Free = mergeFigure(prev.Free, n.Free, n.ReadAt)
		if !next.Disk.Free.Known && old.Disk != nil {
			n.ReadAt, n.Low, n.Refuses, n.Warning, n.Floor = prev.ReadAt, prev.Low, prev.Refuses, prev.Warning, prev.Floor
		}
		next.Disk = &n
	}
	for i := range next.Tenants {
		n := &next.Tenants[i]
		if n.ReadAt.IsZero() {
			n.ReadAt = at
		}
		var prev Tenant
		for _, v := range old.Tenants {
			if v.Kind == n.Kind && v.Name == n.Name {
				prev = v
				break
			}
		}
		n.Used = mergeFigure(prev.Used, n.Used, at)
		n.Promised = mergeFigure(prev.Promised, n.Promised, at)
	}
	if next.Estimate != nil {
		var prev Figure
		if old.Estimate != nil {
			prev = *old.Estimate
		}
		v := mergeFigure(prev, *next.Estimate, at)
		next.Estimate = &v
	}
	for i := range next.Parts {
		var prior Figure
		for _, oldPart := range old.Parts {
			if oldPart.Deployment == next.Parts[i].Deployment && oldPart.Part == next.Parts[i].Part && oldPart.Need == next.Parts[i].Need && oldPart.RunsOn == next.Parts[i].RunsOn {
				prior = oldPart.USDPerMonth
				break
			}
		}
		next.Parts[i].USDPerMonth = mergeFigure(prior, next.Parts[i].USDPerMonth, at)
	}
	return next
}
func figureAt(f Figure, now time.Time, size bool) Figure {
	limit := liveUnknown
	if size {
		limit = sizeUnknown
	}
	if f.Known && (f.ReadAt.IsZero() || now.Sub(f.ReadAt) > limit) {
		f.Known = false
		f.Reason = "The last successful reading is too old."
	}
	f.Stale = !size && !f.ReadAt.IsZero() && now.Sub(f.ReadAt) > liveFresh
	return f
}
func resourceAt(r *ResourceCapacity, now time.Time) *ResourceCapacity {
	if r == nil {
		return nil
	}
	n := *r
	n.Total = figureAt(n.Total, now, true)
	n.Promised.Sum.AppTown = figureAt(n.Promised.Sum.AppTown, now, false)
	n.Promised.Sum.Other = figureAt(n.Promised.Sum.Other, now, false)
	if n.InUse != nil {
		v := *n.InUse
		v.AppTown = figureAt(v.AppTown, now, false)
		v.Other = figureAt(v.Other, now, false)
		n.InUse = &v
	}
	if n.HostUsed != nil {
		v := figureAt(*n.HostUsed, now, false)
		n.HostUsed = &v
	}
	return &n
}
func capacityAt(c TargetCapacity, now time.Time) TargetCapacity {
	c.Stale = c.ReadAt.IsZero() || now.Sub(c.ReadAt) > liveFresh
	c.LiveUnknown = c.ReadAt.IsZero() || now.Sub(c.ReadAt) > liveUnknown || !c.TenantsAt.IsZero() && now.Sub(c.TenantsAt) > liveUnknown
	c.Memory = resourceAt(c.Memory, now)
	c.CPU = resourceAt(c.CPU, now)
	if c.Disk != nil {
		d := *c.Disk
		d.Total = figureAt(d.Total, now, true)
		d.Free = figureAt(d.Free, now, false)
		c.Disk = &d
	}
	c.Stale = c.Stale || capacityHasStaleFigures(c) || !c.TenantsAt.IsZero() && now.Sub(c.TenantsAt) > liveFresh
	c.Tenants = append([]Tenant(nil), c.Tenants...)
	for i := range c.Tenants {
		c.Tenants[i].Unknown = c.Tenants[i].ReadAt.IsZero() || now.Sub(c.Tenants[i].ReadAt) > liveUnknown
		c.Tenants[i].Stale = c.Tenants[i].ReadAt.IsZero() || now.Sub(c.Tenants[i].ReadAt) > liveFresh
		c.Tenants[i].Used = figureAt(c.Tenants[i].Used, now, false)
		c.Tenants[i].Promised = figureAt(c.Tenants[i].Promised, now, false)
	}
	if c.Estimate != nil {
		v := figureAt(*c.Estimate, now, false)
		c.Estimate = &v
	}
	c.Parts = append([]ElasticPart(nil), c.Parts...)
	for i := range c.Parts {
		c.Parts[i].USDPerMonth = figureAt(c.Parts[i].USDPerMonth, now, false)
		if now.Sub(c.ReadAt) > sizeUnknown {
			c.Parts[i].RunsOn = "unknown"
		}
	}
	return c
}

func capacityHasStaleFigures(c TargetCapacity) bool {
	for _, r := range []*ResourceCapacity{c.Memory, c.CPU} {
		if r == nil {
			continue
		}
		if r.Promised.Sum.AppTown.Stale || r.Promised.Sum.Other.Stale {
			return true
		}
		if r.InUse != nil && (r.InUse.AppTown.Stale || r.InUse.Other.Stale) {
			return true
		}
		if r.HostUsed != nil && r.HostUsed.Stale {
			return true
		}
	}
	for _, t := range c.Tenants {
		if t.Used.Stale || t.Promised.Stale {
			return true
		}
	}
	return c.Disk != nil && c.Disk.Free.Stale
}

func capacityHasExpiredUse(c TargetCapacity) bool {
	expired := func(f Figure) bool { return !f.ReadAt.IsZero() && !f.Known }
	for _, r := range []*ResourceCapacity{c.Memory, c.CPU} {
		if r == nil {
			continue
		}
		if expired(r.Promised.Sum.AppTown) || expired(r.Promised.Sum.Other) {
			return true
		}
		if r.InUse != nil && (expired(r.InUse.AppTown) || expired(r.InUse.Other)) {
			return true
		}
		if r.HostUsed != nil && expired(*r.HostUsed) {
			return true
		}
	}
	for _, t := range c.Tenants {
		if expired(t.Used) || expired(t.Promised) {
			return true
		}
	}
	return false
}

// A first partial check can establish a reading from the figures it actually
// read. An entirely unread check has no successful time to publish.
func successfulCapacityTime(c TargetCapacity) time.Time {
	var at time.Time
	figure := func(f Figure) {
		if f.Known && f.ReadAt.After(at) {
			at = f.ReadAt
		}
	}
	for _, r := range []*ResourceCapacity{c.Memory, c.CPU} {
		if r == nil {
			continue
		}
		// Promised sums come from AppTown's own records, not the host, so
		// they never show that the host answered.
		figure(r.Total)
		if r.InUse != nil {
			figure(r.InUse.AppTown)
			figure(r.InUse.Other)
		}
		if r.HostUsed != nil {
			figure(*r.HostUsed)
		}
	}
	if c.Disk != nil {
		figure(c.Disk.Total)
		figure(c.Disk.Free)
	}
	if c.Estimate != nil {
		figure(*c.Estimate)
	}
	for _, p := range c.Parts {
		figure(p.USDPerMonth)
	}
	for _, t := range c.Tenants {
		figure(t.Used)
		figure(t.Promised)
	}
	if c.TenantsAt.After(at) {
		at = c.TenantsAt
	}
	return at
}
