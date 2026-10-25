package docker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
)

type bindingMemory struct {
	binding deploy.StorageBinding
	found   bool
	failure error
	writes  int
}

func (m *bindingMemory) Get(context.Context, string, string) (deploy.StorageBinding, bool, error) {
	return m.binding, m.found, m.failure
}
func (m *bindingMemory) Establish(_ context.Context, b deploy.StorageBinding) error {
	if m.failure != nil {
		return m.failure
	}
	m.binding = b
	m.found = true
	m.writes++
	return nil
}
func (m *bindingMemory) Retire(context.Context, string, string) error {
	if m.failure != nil {
		return m.failure
	}
	m.found = false
	return nil
}

func TestStorageAdmissionRequiresObservedEngineAndIncarnation(t *testing.T) {
	const stack = "env-storage-main"
	volume := Current.NeedVolume(stack, "postgres")
	cname := Current.Service(stack, "postgres")
	image := "sha256:" + strings.Repeat("a", 64)
	created := time.Date(2026, 10, 25, 1, 2, 3, 123456789, time.UTC)
	svc := deploy.ServiceSpec{Name: "postgres", Image: "postgres:17", KeepsImage: true}
	original := deploy.StorageBinding{DataPath: "/data", Need: svc.Name, Volume: volume, DeclaredImage: svc.Image, ImageID: image, VolumeCreated: created, VolumeDriver: "local", Origin: "created"}
	for _, tc := range []struct {
		name                     string
		container, volume, bound bool
		restore                  bool
		change                   string
		wantError                bool
	}{
		{name: "new", volume: false},
		{name: "observed", container: true, volume: true},
		{name: "container loss", volume: true, bound: true},
		{name: "retained unknown", volume: true, wantError: true},
		{name: "same name replaced", volume: true, bound: true, change: "incarnation", wantError: true},
		{name: "changed mount after container loss", volume: true, bound: true, change: "path", wantError: true},
		{name: "changed witnessed mount", volume: true, container: true, bound: true, change: "path", wantError: true},
		{name: "changed engine", volume: true, bound: true, change: "declaration", wantError: true},
		{name: "wrong running engine", container: true, volume: true, bound: true, change: "actual", wantError: true},
		{name: "wrong mount", container: true, volume: true, change: "mount", wantError: true},
		{name: "unknown volume", volume: true, change: "unreadable", wantError: true},
		{name: "missing timestamp", volume: true, bound: true, change: "timestamp", wantError: true},
		{name: "lost bound volume", bound: true, wantError: true},
		{name: "restore matching source", volume: true, bound: true, restore: true, wantError: true},
		{name: "restore previous target", volume: true, bound: true, restore: true, wantError: true, change: "restored"},
		{name: "restore new target", bound: true, restore: true},
		{name: "database failure", volume: true, bound: true, change: "database", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &bindingMemory{binding: original, found: tc.bound}
			if tc.change == "restored" {
				m.binding.Origin = "restored"
			}
			if tc.change == "database" {
				m.failure = errors.New("database unavailable")
			}
			volumeExists := tc.volume
			spec := svc
			if tc.change == "declaration" {
				spec.Image = "postgres:18"
			}
			client := &fakeClient{reply: func(req Command) ([]byte, error) {
				line := strings.Join(req.Args, " ")
				switch {
				case line == "volume inspect "+volume:
					if tc.change == "unreadable" {
						return []byte("permission denied"), errors.New("inspect failed")
					}
					if !volumeExists {
						return []byte("Error: No such volume: " + volume), errors.New("missing")
					}
					return []byte("[]"), nil
				case strings.HasPrefix(line, "volume create "):
					volumeExists = true
					return []byte(volume), nil
				case line == "volume inspect --format {{json .}} "+volume:
					stamp := created
					if tc.change == "incarnation" {
						stamp = stamp.Add(time.Nanosecond)
					}
					if tc.change == "timestamp" {
						stamp = time.Time{}
					}
					return json.Marshal(map[string]any{"Name": volume, "CreatedAt": stamp, "Driver": "local"})
				case line == "image inspect "+spec.Image:
					return []byte("[]"), nil
				case line == "image inspect --format {{json .}} "+image || line == "image inspect --format {{json .}} "+spec.Image:
					return json.Marshal(map[string]any{"Id": image})
				case line == "inspect --format {{json .}} "+cname:
					actual, mount := image, volume
					if tc.change == "actual" {
						actual = "sha256:" + strings.Repeat("b", 64)
					}
					if tc.change == "mount" {
						mount = "someone-elses-data"
					}
					return json.Marshal(map[string]any{"Image": actual, "Config": map[string]string{"Image": svc.Image}, "Mounts": []map[string]string{{"Type": "volume", "Name": mount, "Destination": "/data"}}})
				default:
					return nil, errors.New("unexpected command: " + line)
				}
			}}
			own := Ownership{Storage: m}
			if tc.restore {
				own.RestoreStorage = bindingRestore{m}
			}
			path := "/data"
			if tc.change == "path" {
				path = "/different"
			}
			got, err := bindService(t.Context(), Scope{Client: client}, stack, cname, path, spec, tc.container, own)
			if (err != nil) != tc.wantError {
				t.Fatalf("image=%q error=%v", got, err)
			}
			if tc.wantError {
				if m.writes != 0 {
					t.Fatal("refusal established evidence")
				}
				return
			}
			if got != image {
				t.Fatalf("engine was not pinned: %q", got)
			}
			if !m.binding.VolumeCreated.Equal(created) {
				t.Fatalf("incarnation precision lost: %s", m.binding.VolumeCreated)
			}
			if !tc.bound && m.writes != 1 {
				t.Fatal("new evidence not recorded")
			}
		})
	}
}

func TestStorageRetirementFollowsConfirmedOwnedVolumeDeletion(t *testing.T) {
	for _, tc := range []struct {
		name                                    string
		present, backedUp, unknown, recordFails bool
		wantRetained                            bool
	}{
		{name: "deleted", present: true, backedUp: true},
		{name: "already absent", backedUp: true},
		{name: "kept without backup", present: true, wantRetained: true},
		{name: "unreadable", present: true, backedUp: true, unknown: true, wantRetained: true},
		{name: "record failure", present: true, backedUp: true, recordFails: true, wantRetained: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newOwnedHost()
			if tc.present {
				h = newOwnedHost("volume database")
			}
			h.unknown["volume database"] = tc.unknown
			m := &bindingMemory{found: true}
			if tc.recordFails {
				m.failure = errors.New("database unavailable")
			}
			out := TearDownOwned(t.Context(), h.scope(), []deploy.Owned{{Kind: "volume", ID: "database", Need: "postgres", Data: true}}, "", tc.backedUp, m)
			if m.found != tc.wantRetained {
				t.Fatalf("evidence retained=%t want=%t", m.found, tc.wantRetained)
			}
			if tc.wantRetained && len(out.Left) == 0 {
				t.Fatal("unsettled storage silently omitted")
			}
		})
	}
}

type bindingRestore struct{ memory *bindingMemory }

func (r bindingRestore) Image(_ string, declared string, _ *deploy.StorageBinding) (string, bool, error) {
	return declared, false, nil
}
func (r bindingRestore) Restore(ctx context.Context, _ *deploy.StorageBinding, target deploy.StorageBinding) error {
	target.Origin = "restored"
	return r.memory.Establish(ctx, target)
}

func TestRestoreRefusesEveryExistingEngineVolumeBeforeStartingAny(t *testing.T) {
	const stack = "restore-target"
	later := Current.NeedVolume(stack, "second")
	host := newOwnedHost("volume " + later)
	memory := &bindingMemory{}
	_, err := EnsureStandingServices(t.Context(), Scope{Client: &fakeClient{reply: func(req Command) ([]byte, error) {
		if len(req.Args) > 0 && req.Args[0] == "image" {
			return json.Marshal(map[string]string{"Id": "sha256:" + strings.Repeat("a", 64)})
		}
		return host.reply(req)
	}}}, stack, []deploy.ServiceSpec{{Name: "first", KeepsImage: true}, {Name: "second", KeepsImage: true}}, "production", Ownership{Storage: memory, RestoreStorage: bindingRestore{memory}, Needs: map[string]string{"first": "/data", "second": "/data"}})
	if err == nil || !strings.Contains(err.Error(), later) {
		t.Fatalf("existing later volume admitted: %v", err)
	}
	if len(host.runs) != 0 || len(host.created) != 0 || len(host.removed) != 0 {
		t.Fatalf("partial restore started effects: runs=%v creates=%v removals=%v", host.runs, host.created, host.removed)
	}
}

func TestNewStoragePullsEngineBeforeCreatingVolume(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "cold image cache", true: "failed pull"}[fails], func(t *testing.T) {
			const stack = "new-storage"
			svc := deploy.ServiceSpec{Name: "postgres", Image: "postgres:16-alpine", KeepsImage: true}
			volume := Current.NeedVolume(stack, svc.Name)
			h := newOwnedHost("network " + Current.Network(stack))
			m := &bindingMemory{}
			image := "sha256:" + strings.Repeat("a", 64)
			pulled := false
			client := &fakeClient{reply: func(req Command) ([]byte, error) {
				line := strings.Join(req.Args, " ")
				switch {
				case line == "image inspect "+svc.Image:
					if !pulled {
						return []byte("Error: No such image: " + svc.Image), errors.New("absent")
					}
					return []byte("[]"), nil
				case line == "pull "+svc.Image:
					if h.objects["volume "+volume] {
						t.Fatal("volume created before engine pull")
					}
					if fails {
						return nil, errors.New("registry unavailable")
					}
					pulled = true
					return nil, nil
				case line == "image inspect --format {{json .}} "+svc.Image:
					if !pulled {
						t.Fatal("image evidence invented before pull")
					}
					return json.Marshal(map[string]string{"Id": image})
				case line == "volume inspect --format {{json .}} "+volume:
					return json.Marshal(map[string]any{"Name": volume, "CreatedAt": time.Date(2026, 10, 25, 0, 0, 0, 1, time.UTC), "Driver": "local"})
				case len(req.Args) > 0 && req.Args[0] == "run":
					if !m.found || m.binding.ImageID != image || !strings.Contains(line, image) {
						t.Fatal("engine started before immutable binding")
					}
				}
				return h.reply(req)
			}}
			_, err := EnsureStandingServices(t.Context(), Scope{Client: client}, stack, []deploy.ServiceSpec{svc}, "production", Ownership{Storage: m, Needs: map[string]string{svc.Name: "/data"}})
			if (err != nil) != fails {
				t.Fatalf("first start: %v", err)
			}
			if fails && (len(h.created) != 0 || len(h.runs) != 0 || m.found) {
				t.Fatalf("failed pull left storage or engine: creates=%v runs=%v binding=%t", h.created, h.runs, m.found)
			}
			if !fails && len(h.runs) != 1 {
				t.Fatalf("cold first start did not run engine: %v", h.runs)
			}
		})
	}
}

func TestStorageBindingSurvivesSelectorRemoval(t *testing.T) {
	for _, scenario := range []string{"engine-flag", "data-path", "unreadable"} {
		t.Run(scenario, func(t *testing.T) {
			svc := deploy.ServiceSpec{Name: "custom-db", Image: "postgres:17", KeepsImage: true}
			path := "/data"
			memory := &bindingMemory{found: true, binding: deploy.StorageBinding{Need: svc.Name, Volume: Current.NeedVolume("bound", svc.Name), DataPath: path}}
			if scenario == "data-path" {
				path = ""
			} else {
				svc.KeepsImage = false
			}
			if scenario == "unreadable" {
				memory.failure = errors.New("database unavailable")
			}
			client := &fakeClient{reply: func(req Command) ([]byte, error) {
				t.Errorf("selector refusal reached Docker: %v", req.Args)
				return nil, errors.New("unexpected")
			}}
			_, err := EnsureStandingServices(t.Context(), Scope{Client: client}, "bound", []deploy.ServiceSpec{svc}, "synthetic", Ownership{Storage: memory, Needs: map[string]string{svc.Name: path}})
			if err == nil || !memory.found || memory.writes != 0 {
				t.Fatalf("selector bypass: err=%v binding=%+v", err, memory)
			}
		})
	}
	// Ordinary unbound non-engine services keep their existing lifecycle.
	h := newOwnedHost()
	memory := &bindingMemory{}
	_, err := EnsureStandingServices(t.Context(), Scope{Client: &fakeClient{reply: h.reply}}, "unbound", []deploy.ServiceSpec{{Name: "s3", Image: "object-store:1"}}, "production", Ownership{Storage: memory, Needs: map[string]string{"s3": "/data"}})
	if err != nil || len(h.runs) == 0 || memory.found {
		t.Fatalf("unbound non-engine changed: err=%v runs=%v binding=%+v", err, h.runs, memory)
	}
}

type exactBindingRestore struct{ bindingRestore }

func (r exactBindingRestore) Image(_ string, _ string, source *deploy.StorageBinding) (string, bool, error) {
	return source.ImageID, true, nil
}
func TestPhysicalRestoreRefusesChangedDataPathBeforeCreation(t *testing.T) {
	source := deploy.StorageBinding{Need: "redis", Volume: Current.NeedVolume("target", "redis"), ImageID: "sha256:" + strings.Repeat("a", 64), DataPath: "/original"}
	memory := &bindingMemory{found: true, binding: source}
	h := newOwnedHost()
	_, err := EnsureStandingServices(t.Context(), Scope{Client: &fakeClient{reply: h.reply}}, "target", []deploy.ServiceSpec{{Name: "redis", Image: "redis:7", KeepsImage: true}}, "production", Ownership{Storage: memory, RestoreStorage: exactBindingRestore{bindingRestore{memory}}, Needs: map[string]string{"redis": "/changed"}})
	if err == nil || !strings.Contains(err.Error(), "data path") {
		t.Fatalf("changed physical layout admitted: %v", err)
	}
	if len(h.runs) != 0 || len(h.created) != 0 || len(h.removed) != 0 {
		t.Fatalf("physical layout refusal had effects: %+v", h)
	}
}
