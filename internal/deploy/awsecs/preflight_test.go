package awsecs

import (
	"context"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

func TestStageOneRefusesUnsupportedShapesBeforeEffects(t *testing.T) {
	for _, tt := range []struct {
		name, want string
		change     func(*deploy.Placement, *deploy.Source)
	}{
		{"single public", "", func(*deploy.Placement, *deploy.Source) {}},
		{"worker", "", func(_ *deploy.Placement, s *deploy.Source) {
			s.Contract.Parts[0].Public = false
			s.Contract.Parts[0].Port = 0
		}},
		{"postgres", "", func(_ *deploy.Placement, s *deploy.Source) { s.Contract.Needs = []string{"postgres"} }},
		{"multipart", "single-part", func(_ *deploy.Placement, s *deploy.Source) {
			s.Contract.Parts = append(s.Contract.Parts, appcontract.Part{Name: "worker"})
		}},
		{"s3", "s3 need", func(_ *deploy.Placement, s *deploy.Source) { s.Contract.Needs = []string{"s3"} }},
		{"redis", "redis need", func(_ *deploy.Placement, s *deploy.Source) { s.Contract.Needs = []string{"redis"} }},
		{"mariadb", "mariadb need", func(_ *deploy.Placement, s *deploy.Source) { s.Contract.Needs = []string{"mariadb"} }},
		{"data directory", "data directory", func(_ *deploy.Placement, s *deploy.Source) { s.Contract.Parts[0].DataDir = "/data" }},
		{"shared database", "dedicated database", func(_ *deploy.Placement, s *deploy.Source) {
			s.Contract.Needs = []string{"postgres"}
			s.Contract.NeedParams = map[string]map[string]string{"postgres": {"instance": "shared"}}
		}},
		{"unsupported major", "16 and 17", func(_ *deploy.Placement, s *deploy.Source) {
			s.Contract.Needs = []string{"postgres"}
			s.Contract.NeedParams = map[string]map[string]string{"postgres": {"version": "15"}}
		}},
		{"gate", "gate", func(p *deploy.Placement, _ *deploy.Source) { p.Gate = &deploy.Gate{} }},
		{"local image", "by digest", func(_ *deploy.Placement, s *deploy.Source) { s.Images["web"] = "local:latest" }},
		{"local init image", "init steps", func(_ *deploy.Placement, s *deploy.Source) {
			s.Contract.Init = []appcontract.InitContainer{{Name: "migrate"}}
			s.Init = map[string]string{"migrate": "local:latest"}
		}},
		{"size", "no Fargate size", func(_ *deploy.Placement, s *deploy.Source) { s.Contract.Parts[0].Resources.CPUs = 33 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := deploy.Placement{}
			src := deploy.Source{Contract: &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true, Port: 8080}}}, Images: map[string]string{"web": "registry/app@sha256:abc"}}
			tt.change(&p, &src)
			d := NewDriver(Dependencies{Client: func(context.Context) (*API, Config, Estate, error) {
				t.Fatal("preflight called AWS")
				return nil, Config{}, Estate{}, nil
			}})
			err := d.Preflight(t.Context(), p, src, nil, func(string, ...any) {})
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %s", err, tt.want)
			}
		})
	}
}

func TestFargateFitsOnlySupportedPairs(t *testing.T) {
	for _, tt := range []struct {
		memory int64
		cpus   float64
		want   taskSize
	}{{0, 0, taskSize{256, 512}}, {1500 << 20, 0, taskSize{256, 2048}}, {4 << 30, .5, taskSize{512, 4096}}, {9 << 30, 1, taskSize{2048, 9216}}, {61 << 30, 8, taskSize{16384, 65536}}, {121 << 30, 16, taskSize{32768, 249856}}, {1 << 30, 32, taskSize{32768, 61440}}} {
		got, err := fit(appcontract.Resources{MemoryBytes: tt.memory, CPUs: tt.cpus})
		if err != nil || got != tt.want {
			t.Fatalf("%d bytes, %.2g CPUs: %+v %v; want %+v", tt.memory, tt.cpus, got, err, tt.want)
		}
	}
	if _, err := fit(appcontract.Resources{MemoryBytes: 245 << 30}); err == nil {
		t.Fatal("accepted memory larger than any task")
	}
}

func TestALBRefusesHealthItCannotHonour(t *testing.T) {
	for _, tt := range []struct {
		name string
		part appcontract.Part
		want string
	}{
		{"default", appcontract.Part{Public: true, Port: 8080, Intent: &appcontract.Health{Readiness: &appcontract.Probe{HTTP: "/ready"}}}, ""},
		{"worker probe", appcontract.Part{Intent: &appcontract.Health{Readiness: &appcontract.Probe{HTTP: "/ready"}}}, "without a public route"},
		{"threshold", appcontract.Part{Public: true, Port: 8080, Intent: &appcontract.Health{Readiness: &appcontract.Probe{HTTP: "/ready", SuccessThreshold: 1}}}, "2 through 10"},
		{"timeout", appcontract.Part{Public: true, Port: 8080, Intent: &appcontract.Health{Readiness: &appcontract.Probe{HTTP: "/ready", Timeout: "1s"}}}, "at least 2 seconds"},
		{"distinct probes", appcontract.Part{Public: true, Port: 8080, Intent: &appcontract.Health{Readiness: &appcontract.Probe{HTTP: "/ready"}, Liveness: &appcontract.Probe{HTTP: "/live"}}}, "different readiness and liveness"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := partHealth(tt.part)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("%v; want %s", err, tt.want)
			}
		})
	}
}
