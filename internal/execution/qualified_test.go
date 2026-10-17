package execution

import (
	"strings"
	"testing"
)

// A worker's profile is qualified by the record of the fact set it matches,
// and only then: any fact that differs (the Docker version by its major
// version), was not measured, or a missing exclusive declaration or
// privileged container leaves it unqualified.
func TestAProfileIsQualifiedOnlyByTheFactsItWasQualifiedOn(t *testing.T) {
	q := QualifiedProfile{Record: "design/qualification/x.md", Name: ProfileWorkerVM, OS: "linux Debian GNU/Linux 13 (trixie)", Architecture: "x86_64",
		Cgroup: "v2 (systemd)", StorageDriver: "overlay2", DockerMajor: "26", DindDigest: "docker@sha256:aa3", DindVersion: "27.5.1, build 4c9b3b0"}
	matching := WorkerProfile{Name: q.Name, OS: q.OS, Architecture: q.Architecture, Kernel: "6.17.2-1-pve", Cgroup: q.Cgroup, Docker: "26.1.5+dfsg1",
		StorageDriver: q.StorageDriver, DindImage: "docker:27-dind", DindImageID: "sha256:d8", DindDigest: q.DindDigest, DindVersion: q.DindVersion, Privileged: "yes", Exclusive: true, Egress: "not contained"}
	if got := matching.qualificationIn([]QualifiedProfile{q}); got != q.Record {
		t.Fatalf("a matching profile: %q", got)
	}
	for name, change := range map[string]func(*WorkerProfile){
		"another kernel":         func(p *WorkerProfile) { p.Kernel = "6.8.12-1-pve" },
		"another local image id": func(p *WorkerProfile) { p.DindImageID = "sha256:e9" },
		"another Docker release": func(p *WorkerProfile) { p.Docker = "26.0.0" },
	} {
		p := matching
		change(&p)
		if got := p.qualificationIn([]QualifiedProfile{q}); got != q.Record {
			t.Errorf("%s: %q, want the record", name, got)
		}
	}
	for name, change := range map[string]func(*WorkerProfile){
		"another profile":           func(p *WorkerProfile) { p.Name = "worker-sysbox" },
		"another OS":                func(p *WorkerProfile) { p.OS = "linux Ubuntu 24.04.1 LTS" },
		"another architecture":      func(p *WorkerProfile) { p.Architecture = "aarch64" },
		"another cgroup mode":       func(p *WorkerProfile) { p.Cgroup = "v1 (cgroupfs)" },
		"another storage driver":    func(p *WorkerProfile) { p.StorageDriver = "vfs" },
		"another Docker major":      func(p *WorkerProfile) { p.Docker = "27.3.1" },
		"another daemon image":      func(p *WorkerProfile) { p.DindDigest = "docker@sha256:ffff" },
		"another daemon version":    func(p *WorkerProfile) { p.DindVersion = "27.5.2, build 0" },
		"no repository digest":      func(p *WorkerProfile) { p.DindDigest = "unknown: the image has no repository digest" },
		"Docker not measured":       func(p *WorkerProfile) { p.Docker = "unknown: docker info failed" },
		"not declared exclusive":    func(p *WorkerProfile) { p.Exclusive = false },
		"no privileged container":   func(p *WorkerProfile) { p.Privileged = "no: refused" },
		"privileged unknown":        func(p *WorkerProfile) { p.Privileged = "unknown: Docker could not be measured" },
		"a qualification no record": func(p *WorkerProfile) {},
	} {
		p, list := matching, []QualifiedProfile{q}
		change(&p)
		if name == "a qualification no record" {
			list[0].Record = ""
		}
		if got := p.qualificationIn(list); got != "" {
			t.Errorf("%s: qualified by %q", name, got)
		}
	}
	if got := matching.qualificationIn(nil); got != "" {
		t.Errorf("with no qualified profile: %q", got)
	}
}

// Every qualified fact set in the shipped list is complete and names the
// record it was qualified by. The records are the maintainers' and are not
// published with the code; their make target checks that each named one
// exists (scripts/maintainer.mk, check-maintainer-scripts).
func TestEveryQualifiedProfileIsCompleteAndNamesItsRecord(t *testing.T) {
	t.Parallel()
	qualified, err := QualifiedProfiles()
	if err != nil {
		t.Fatalf("qualified_profiles.json: %v", err)
	}
	for _, q := range qualified {
		for field, v := range map[string]string{"record": q.Record, "name": q.Name, "os": q.OS, "architecture": q.Architecture, "cgroup": q.Cgroup,
			"storageDriver": q.StorageDriver, "dockerMajor": q.DockerMajor, "dindDigest": q.DindDigest, "dindVersion": q.DindVersion} {
			if strings.TrimSpace(v) == "" {
				t.Errorf("%+v: no %s", q, field)
			}
		}
	}
}
