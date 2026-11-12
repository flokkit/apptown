package fly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
)

var _ deploy.UsageReader = (*Driver)(nil)

// parts is every part and need Machine of the deployment in one Machine
// list, of any generation (a release's new Machines stand beside the old
// until it settles). A Machine whose host Fly cannot reach is known by its
// last known config; one that names no part even there is reported with no
// name, so no part is taken to be absent while it may be that one.
func (d *Driver) parts(ms []machine, p deploy.Placement) []deploy.PartRuntime {
	out := []deploy.PartRuntime{}
	for _, m := range ms {
		md := m.Config.Metadata
		if md == nil && m.IncompleteConfig != nil {
			md = m.IncompleteConfig.Metadata
		}
		ours := md[metaInstance] == d.Installation && md[metaDeployment] == p.ID.String()
		switch {
		case ours && (md[metaRole] == "part" || md[metaRole] == "need"):
			out = append(out, partRuntime(m, md))
		case md == nil && !knownHost(m.HostStatus):
			out = append(out, deploy.PartRuntime{ID: m.ID, Status: m.State, Unreachable: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Part < out[j].Part })
	return out
}

// partRuntime is what one Machine answer says of its part (named by md):
// its state, size, image, checks, and from its events (Fly gives the
// newest few of its current version) when it last started and how it
// exited.
func partRuntime(m machine, md map[string]string) deploy.PartRuntime {
	r := deploy.PartRuntime{Part: md[metaGroup], ID: m.ID, Status: m.State, Running: m.State == "started", Unreachable: !knownHost(m.HostStatus)}
	r.Generation, _ = strconv.ParseInt(md[metaGeneration], 10, 64) // a need's is 0
	if len(m.Checks) > 0 {
		r.Health = "unhealthy"
		if checksPass(m) {
			r.Health = "healthy"
		}
	}
	if cfg, err := m.Config.requestConfig(); err == nil {
		r.Image, r.CPULimit, r.CPUKind, r.MemoryLimit = cfg.Image, float64(cfg.Guest.CPUs), cfg.Guest.CPUKind, int64(cfg.Guest.MemoryMB)<<20
	}
	for _, e := range m.Events {
		at := time.UnixMilli(e.Timestamp).UTC()
		if e.Status == "started" && at.After(r.StartedAt) {
			r.StartedAt = at
		}
		if ex, ok := eventExit(e); ok {
			r.Exits = append(r.Exits, deploy.PartExit{At: at, Code: ex.Code, OOM: ex.OOM, Requested: ex.Requested})
		}
	}
	return r
}

// ReadUsage is each part's Machine with its CPU and memory use now, from
// Fly's metrics for the org: the CPU it used over the last minute and the
// memory its guest has in use. A part whose use Fly's metrics do not give
// says why; it is never zero.
func (d *Driver) ReadUsage(ctx context.Context, p deploy.Placement) ([]deploy.PartRuntime, error) {
	app, _ := p.Handle["appName"].(string)
	if app == "" {
		return nil, nil
	}
	c, cfg, err := d.client(ctx)
	if err != nil {
		return nil, err
	}
	ms, err := c.machines(ctx, app)
	if err != nil {
		return nil, err
	}
	parts := d.parts(ms, p)
	if len(parts) == 0 {
		return parts, nil
	}
	// fly_instance_cpu counts centiseconds per CPU and mode; the modes that
	// are not the guest's own work are left out.
	scope := "app=" + strconv.Quote(app)
	cores, cpuErr := c.query(ctx, cfg.Org, "sum by (instance) (rate(fly_instance_cpu{"+scope+`,mode!~"idle|iowait|steal"}[1m])) / 100`)
	used, memErr := c.query(ctx, cfg.Org, "fly_instance_memory_mem_total{"+scope+"} - fly_instance_memory_mem_available{"+scope+"}")
	for i := range parts {
		pr := &parts[i]
		cpu, cpuOK := cores[pr.ID]
		mem, memOK := used[pr.ID]
		switch {
		case cpuErr != nil || memErr != nil:
			pr.UseReason = "Fly's metrics did not answer: " + errors.Join(cpuErr, memErr).Error()
		case !cpuOK || !memOK:
			pr.UseReason = "Fly's metrics have no sample for its Machine yet"
		default:
			pr.Use = &deploy.PartUse{CPUCores: cpu, MemoryBytes: int64(mem)}
		}
	}
	return parts, nil
}

// query asks the org's Prometheus (https://docs.fly.io/monitoring/metrics)
// for an instant vector and answers each series' value by its instance
// label, the Machine's id.
func (c *Client) query(ctx context.Context, org, promql string) (map[string]float64, error) {
	u := c.MetricsBase + "/prometheus/" + url.PathEscape(org) + "/api/v1/query?" + url.Values{"query": {promql}}.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", metricsAuthorization(c.Token))
	res, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New(redact(err.Error(), c.Token))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("answered %d: %s", res.StatusCode, redact(strings.TrimSpace(string(raw)), c.Token))
	}
	var got struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		return nil, fmt.Errorf("reading its answer: %w", err)
	}
	if got.Status != "success" {
		return nil, fmt.Errorf("answered %s: %s", got.Status, redact(got.Error, c.Token))
	}
	out := map[string]float64{}
	for _, r := range got.Data.Result {
		s, _ := r.Value[1].(string)
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || r.Metric["instance"] == "" {
			continue
		}
		out[r.Metric["instance"]] = v
	}
	return out, nil
}

// metricsAuthorization is the token as Fly's metrics take it: a token made
// with `fly tokens create` is sent in the form it is printed in
// ("FlyV1 ..."), any other as a bearer token.
func metricsAuthorization(token string) string {
	if strings.HasPrefix(token, "FlyV1 ") {
		return token
	}
	return "Bearer " + token
}
