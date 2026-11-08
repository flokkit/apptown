package deployment

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/diskspace/diskspacetest"
	"github.com/flokkit/apptown/stock"
)

// Each failure remains an unknown figure with a reason, independently of the
// other resource. The fake supplies both samples without sleeping.
func TestHostUseReadsKernelAndPreservesUnknownReasons(t *testing.T) {
	for _, tc := range []struct {
		name, mem, stat    string
		code               int
		total              int64
		cpus               int
		memKnown, cpuKnown bool
	}{
		{"readable", "MemTotal: 1024 kB\nMemAvailable: 512 kB\n", "", 0, 1 << 20, 8, true, true},
		{"memory mismatch", "MemTotal: 1024 kB\nMemAvailable: 512 kB\n", "", 0, 2 << 20, 8, false, true},
		{"CPU mismatch", "MemTotal: 1024 kB\nMemAvailable: 512 kB\n", "", 0, 1 << 20, 4, true, false},
		{"malformed", "MemTotal: bad\n", "cpu bad\n", 0, 1 << 20, 8, false, false},
		{"unreadable", "", "", 1, 1 << 20, 8, false, false},
		{"unknown totals", "", "", 0, 0, 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				n := 0
				d := &diskspacetest.Daemon{Kernel: func(script string) (int, []byte, string) {
					if tc.code != 0 {
						return tc.code, nil, "cannot read /proc"
					}
					if script == "cat /proc/meminfo" {
						return 0, []byte(tc.mem), ""
					}
					if tc.stat != "" {
						return 0, []byte(tc.stat), ""
					}
					n++
					stat := fmt.Sprintf("cpu %d 0 0 %d 0 0 0 0\n", 40*n, 60*n)
					for i := 0; i < 8; i++ {
						stat += fmt.Sprintf("cpu%d 0 0 0 0 0 0 0 0\n", i)
					}
					return 0, []byte(stat), ""
				}}
				dk := docker.Scope{Client: sshDockerFake(func(ctx context.Context, _ string, c docker.Command) ([]byte, error) {
					out, err, ok := d.Run(c.Args)
					if !ok {
						t.Fatalf("unexpected command %v", c.Args)
					}
					return out, err
				})}
				s := &Service{env: NewEnvironment(EnvironmentDependencies{ToolVersions: stock.Versions()}), clock: func() time.Time { return time.Unix(123, 0) }}
				m, c := s.hostUse(t.Context(), dk, tc.total, tc.cpus, "/var/lib/docker")
				for _, v := range []struct {
					f     *Figure
					known bool
				}{{m, tc.memKnown}, {c, tc.cpuKnown}} {
					if v.f.Known != v.known || !v.known && v.f.Reason == "" {
						t.Fatalf("unexpected figure: %+v", v.f)
					}
				}
				if tc.name == "readable" && (m.Value != 512<<10 || c.Value != 3.2) {
					t.Fatalf("wrong readings: %+v %+v", m, c)
				}
				if strings.Contains(tc.name, "mismatch") {
					f := m
					if tc.name == "CPU mismatch" {
						f = c
					}
					if !strings.Contains(f.Reason, "virtualised") {
						t.Fatal(f.Reason)
					}
				}
				if tc.name == "readable" {
					_, err := diskspace.ReadThroughDocker(t.Context(), func(ctx context.Context, args ...string) (string, string, error) {
						b, e := dk.Run(ctx, args...)
						return string(b), "", e
					}, s.env.ToolVersions().Images.DF, "/var/lib/docker", s.now)
					if err != nil {
						t.Fatal(err)
					}
					if d.Started() != 1 {
						t.Fatalf("disk and kernel did not share one reader: %d", d.Started())
					}
				}
				if d.Started() > 1 {
					t.Fatalf("started %d readers", d.Started())
				}
			})
		})
	}
}

// Cancellation reaches the single kernel exec, including its sampling wait.
func TestKernelExecIsCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &diskspacetest.Daemon{}
		sampled := make(chan struct{})
		dk := docker.Scope{Client: sshDockerFake(func(ctx context.Context, _ string, c docker.Command) ([]byte, error) {
			if c.Args[0] == "exec" && strings.Contains(strings.Join(c.Args, " "), "section() {") {
				close(sampled)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			out, err, ok := d.Run(c.Args)
			if !ok {
				t.Fatalf("unexpected command %v", c.Args)
			}
			return out, err
		})}
		s := &Service{env: NewEnvironment(EnvironmentDependencies{ToolVersions: stock.Versions()})}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan *Figure, 1)
		go func() { _, c := s.hostUse(ctx, dk, 8<<30, 8, "/var/lib/docker"); done <- c }()
		<-sampled
		cancel()
		c := <-done
		if c.Known || !strings.Contains(c.Reason, "canceled") {
			t.Fatalf("cancelled CPU reading: %+v", c)
		}
	})
}

// The cgroup route reads the daemon's slice, even when /proc describes a larger
// machine. A broken counter remains unknown without trying another route.
func TestHostUseChoosesCgroupCounters(t *testing.T) {
	for _, tc := range []struct {
		name, scope, current, stat, limit, cpu string
		memKnown, cpuKnown                     bool
		reason                                 string
	}{
		{"slice", "cgroup", "5000", "anon 3000\ninactive_file 500\n", "max", "", true, true, ""},
		{"bad current", "cgroup", "bad", "inactive_file 500\n", "max", "", false, true, "memory.current"},
		{"bad memory stat", "cgroup", "5000", "anon 3000\n", "max", "", false, true, "inactive_file"},
		{"bad CPU", "cgroup", "5000", "inactive_file 500\n", "max", "usage_usec bad\n", true, false, "usage_usec"},
		{"limit mismatch", "cgroup", "5000", "inactive_file 500\n", "9999", "", false, true, "memory.max"},
		{"bad limit", "cgroup", "5000", "inactive_file 500\n", "bad", "", false, true, "memory.max"},
		{"v1 mismatch", "v1", "", "", "", "", false, false, "cgroup v2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				samples := 0
				d := &diskspacetest.Daemon{Kernel: func(string) (int, []byte, string) { t.Fatal("cgroup route read /proc counters"); return 1, nil, "" }, Cgroup: func(file string) (int, []byte, string) {
					value := ""
					switch file {
					case "scope":
						value = tc.scope
					case "memory.current":
						value = tc.current
					case "memory.stat":
						value = tc.stat
					case "memory.max":
						value = tc.limit
					case "cpu.stat":
						samples++
						value = tc.cpu
						if value == "" {
							value = fmt.Sprintf("usage_usec %d\nuser_usec 10\n", samples*1500000)
						}
					default:
						t.Fatalf("unexpected cgroup file %q", file)
					}
					return 0, []byte(value), ""
				}}
				dk := docker.Scope{Client: sshDockerFake(func(ctx context.Context, _ string, c docker.Command) ([]byte, error) {
					out, err, ok := d.Run(c.Args)
					if !ok {
						t.Fatalf("unexpected command %v", c.Args)
					}
					return out, err
				})}
				s := &Service{env: NewEnvironment(EnvironmentDependencies{ToolVersions: stock.Versions()}), clock: time.Now}
				m, c := s.hostUse(t.Context(), dk, 10000, 2, "/var/lib/docker")
				if m.Known != tc.memKnown || c.Known != tc.cpuKnown {
					t.Fatalf("memory %+v CPU %+v", m, c)
				}
				if m.Known && m.Value != 4500 || c.Known && c.Value != 1.5 {
					t.Fatalf("memory %+v CPU %+v", m, c)
				}
				for _, f := range []*Figure{m, c} {
					if !f.Known && (!strings.Contains(f.Reason, tc.reason) || f.Value != 0) {
						t.Fatalf("wrong unknown: %+v", f)
					}
				}
				_, err := diskspace.ReadThroughDocker(t.Context(), func(ctx context.Context, args ...string) (string, string, error) {
					b, e := dk.Run(ctx, args...)
					return string(b), "", e
				}, s.env.ToolVersions().Images.DF, "/var/lib/docker", s.now)
				if err != nil || d.Started() != 1 {
					t.Fatalf("shared disk reader: %d, %v", d.Started(), err)
				}
			})
		})
	}
}

// The daemon clock can stand still or include SSH latency. Only the sampled
// host uptime determines the cgroup CPU interval; bad time leaves memory known.
func TestCgroupCPUUsesHostUptime(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, second, reason string
		known                bool
		cores                float64
	}{
		{"two seconds", "102.00 0.00", "", true, 0.75},
		{"fractional seconds", "100.50 0.00", "", true, 3},
		{"stopped", "100.00 0.00", "did not advance", false, 0},
		{"backwards", "99.00 0.00", "did not advance", false, 0},
		{"malformed", "bad 0.00", "uptime", false, 0},
		{"missing value", "", "uptime", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			samples, times := 0, 0
			d := &diskspacetest.Daemon{
				Cgroup: func(file string) (int, []byte, string) {
					value := ""
					switch file {
					case "scope":
						value = "cgroup"
					case "memory.current":
						value = "5000"
					case "memory.stat":
						value = "inactive_file 500"
					case "memory.max":
						value = "max"
					case "cpu.stat":
						samples++
						value = fmt.Sprintf("usage_usec %d", samples*1500000)
					default:
						t.Fatalf("unexpected counter %s", file)
					}
					return 0, []byte(value), ""
				},
				Uptime: func() string {
					times++
					if times == 1 {
						return "100.00 0.00"
					}
					return tc.second
				},
			}
			dk := docker.Scope{Client: sshDockerFake(func(ctx context.Context, _ string, c docker.Command) ([]byte, error) {
				out, err, ok := d.Run(c.Args)
				if !ok {
					t.Fatalf("unexpected command %v", c.Args)
				}
				return out, err
			})}
			s := &Service{env: NewEnvironment(EnvironmentDependencies{ToolVersions: stock.Versions()}), clock: func() time.Time { return time.Unix(123, 0) }}
			m, c := s.hostUse(t.Context(), dk, 10000, 2, "/var/lib/docker")
			if !m.Known || m.Value != 4500 || c.Known != tc.known || c.Value != tc.cores || !strings.Contains(c.Reason, tc.reason) {
				t.Fatalf("memory %+v CPU %+v", m, c)
			}
		})
	}
}
