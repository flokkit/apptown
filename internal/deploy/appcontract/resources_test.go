package appcontract

import (
	"strings"
	"testing"
)

const profiledApp = `
app:
  web:
    build: .
    public: true
    environment: { LOG_LEVEL: info, REGION: syd }
    resources: { memory: 512M, cpu: 1 }
  worker:
    build: .
    command: ["python", "worker.py"]
profiles:
  preview:
    web: { resources: { memory: 256M } }
  production:
    web:
      resources: { memory: 2G, cpu: 2 }
      environment: { LOG_LEVEL: warn }
    worker:
      resources: { memory: 1Gi }
`

func part(c *Contract, name string) Part {
	for _, p := range c.Parts {
		if p.Name == name {
			return p
		}
	}
	return Part{}
}

// A profile changes only what it declares: resources field by field,
// environment key by key, and only for the deployments that use it; the
// declaration itself stays the base.
func TestAProfileChangesOnlyWhatItDeclares(t *testing.T) {
	c, err := Parse([]byte(profiledApp))
	if err != nil {
		t.Fatal(err)
	}
	if got := part(c, "web").Resources; got != (Resources{MemoryBytes: 512 << 20, CPUs: 1}) {
		t.Fatalf("base web resources %+v", got)
	}
	if part(c, "worker").Resources.Declared() {
		t.Fatalf("an undeclared worker has a limit: %+v", part(c, "worker").Resources)
	}
	if got := c.ProfileNames(); strings.Join(got, ",") != "preview,production" {
		t.Fatalf("profiles %v", got)
	}

	preview := c.ForProfile("preview")
	if got := part(preview, "web").Resources; got != (Resources{MemoryBytes: 256 << 20, CPUs: 1}) {
		t.Errorf("preview web %+v: memory alone must keep the base cpu", got)
	}
	if part(preview, "worker").Resources.Declared() {
		t.Errorf("preview changed the worker it does not name")
	}

	prod := c.ForProfile("production")
	if got := part(prod, "web").Resources; got != (Resources{MemoryBytes: 2 << 30, CPUs: 2}) {
		t.Errorf("production web %+v", got)
	}
	if got := part(prod, "worker").Resources; got != (Resources{MemoryBytes: 1 << 30}) {
		t.Errorf("production worker %+v", got)
	}
	if env := part(prod, "web").Env; env["LOG_LEVEL"] != "warn" || env["REGION"] != "syd" {
		t.Errorf("production web environment %v: the profile merges over the base, key by key", env)
	}

	if c.ForProfile("default") != c || c.ForProfile("staging") != c {
		t.Errorf("a profile app.yaml does not declare must be the base unchanged")
	}
	if part(c, "web").Resources.MemoryBytes != 512<<20 || part(c, "web").Env["LOG_LEVEL"] != "info" {
		t.Errorf("applying a profile changed the declaration: %+v", part(c, "web"))
	}

	over := prod.WithResources(map[string]Resources{"web": {MemoryBytes: 4 << 30}})
	if got := part(over, "web").Resources; got != (Resources{MemoryBytes: 4 << 30, CPUs: 2}) {
		t.Errorf("an override replaces field by field over the profile: %+v", got)
	}
	if part(prod, "web").Resources.MemoryBytes != 2<<30 {
		t.Errorf("an override changed the contract it was applied to")
	}
}

// What a profile or a part's resources cannot say is refused by name,
// never dropped: a dropped limit is no limit.
func TestTheContractRefusesResourcesAndProfilesItCannotHonour(t *testing.T) {
	base := "app:\n  web:\n    build: .\n    public: true\n"
	for _, c := range []struct{ name, yaml, want string }{
		{"memory without a unit", base + "    resources: { memory: 512 }\n", "whole number of M or G"},
		{"memory in kilobytes", base + "    resources: { memory: 512K }\n", "whole number of M or G"},
		{"memory below the floor", base + "    resources: { memory: 16M }\n", "below the smallest"},
		{"a fractional memory", base + "    resources: { memory: 1.5G }\n", "whole number"},
		{"cpu zero", base + "    resources: { cpu: 0 }\n", "positive number of CPUs"},
		{"cpu a word", base + "    resources: { cpu: lots }\n", "positive number of CPUs"},
		{"an empty resources", base + "    resources: {}\n", "neither memory nor cpu"},
		{"a misspelt resources key", base + "    resources: { mem: 1G }\n", "field mem not found"},
		{"a profile named default", base + "profiles:\n  default:\n    web: { resources: { memory: 1G } }\n", "default is the base"},
		{"a profile name with capitals", base + "profiles:\n  Prod:\n    web: { resources: { memory: 1G } }\n", "not a profile name"},
		{"a profile part that is not a part", base + "profiles:\n  prod:\n    api: { resources: { memory: 1G } }\n", `"api" is not a part`},
		{"a profile changing a command", base + "profiles:\n  prod:\n    web: { command: [\"x\"] }\n", "field command not found"},
		{"a profile changing nothing", base + "profiles:\n  prod:\n    web: {}\n", "changes neither"},
		{"a profile setting the browser values", base + "profiles:\n  prod:\n    web: { environment: { APPTOWN_BROWSER_ENV: x } }\n", "only AppTown sets"},
		{"a profile with a bad memory", base + "profiles:\n  prod:\n    web: { resources: { memory: lots } }\n", "profiles: prod: web: resources"},
	} {
		_, err := Parse([]byte(c.yaml))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error naming %q", c.name, err, c.want)
		}
	}
}

func TestMemoryIsReadAndWrittenAsAppYamlWritesIt(t *testing.T) {
	for in, want := range map[string]int64{"32M": 32 << 20, "512M": 512 << 20, "512Mi": 512 << 20, "1G": 1 << 30, "2Gi": 2 << 30} {
		got, err := ParseMemory(in)
		if err != nil || got != want {
			t.Errorf("ParseMemory(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if FormatMemory(1<<30) != "1G" || FormatMemory(1536<<20) != "1536M" {
		t.Errorf("FormatMemory: %s %s", FormatMemory(1<<30), FormatMemory(1536<<20))
	}
	if (Resources{}).MemoryWords() != "no limit" || (Resources{}).CPUWords() != "no limit" {
		t.Errorf("an undeclared resource must read as no limit")
	}
}

// One deployment uses one profile, whichever file declares it: the
// explicit choice, then preview for the preview deployment, then default.
func TestADeploymentUsesOneProfileWhicheverFileDeclaresIt(t *testing.T) {
	declared := map[string]bool{"preview": true, "production": true}
	for _, c := range []struct {
		chosen, deployment string
		declared           map[string]bool
		want               string
		how                ProfileChoice
	}{
		{"production", "main", declared, "production", ProfileChosen},
		{"production", "preview", declared, "production", ProfileChosen},
		{"", "preview", declared, "preview", ProfilePreview},
		{"", "preview", map[string]bool{"production": true}, "default", ProfileBase},
		{"", "main", declared, "default", ProfileBase},
		{"staging", "main", declared, "default", ProfileBase}, // a choice no file declares any more
		{"default", "preview", declared, "default", ProfileChosen},
	} {
		got, how := DeploymentProfile(c.chosen, c.deployment, c.declared)
		if got != c.want || how != c.how {
			t.Errorf("chosen %q on %s: %s (%s), want %s (%s)", c.chosen, c.deployment, got, how, c.want, c.how)
		}
	}
}

// A need is sized exactly as a part is: declared on the need, changed by
// a profile field by field, overridden by an operator, and never handed
// to the catalogue as a parameter.
func TestANeedIsSizedAsAPartIs(t *testing.T) {
	c, err := Parse([]byte(`
app:
  web: { build: ., public: true }
needs:
  postgres:
    version: "16"
    resources: { memory: 512M, cpu: 1 }
  redis: {}
profiles:
  production:
    postgres: { resources: { memory: 2G } }
  preview:
    redis: { resources: { memory: 64M } }
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.NeedResources["postgres"]; got != (Resources{MemoryBytes: 512 << 20, CPUs: 1}) {
		t.Fatalf("postgres resources %+v", got)
	}
	if _, ok := c.NeedResources["redis"]; ok {
		t.Fatalf("an undeclared need has a limit: %+v", c.NeedResources)
	}
	if _, leaked := c.NeedParams["postgres"]["resources"]; leaked || c.NeedParams["postgres"]["version"] != "16" {
		t.Fatalf("resources must not be a catalogue parameter: %v", c.NeedParams["postgres"])
	}
	if got := c.ForProfile("production").NeedResources["postgres"]; got != (Resources{MemoryBytes: 2 << 30, CPUs: 1}) {
		t.Errorf("production postgres %+v: memory alone keeps the base cpu", got)
	}
	if got := c.ForProfile("preview").NeedResources; got["redis"] != (Resources{MemoryBytes: 64 << 20}) || got["postgres"] != c.NeedResources["postgres"] {
		t.Errorf("preview needs %+v", got)
	}
	if c.NeedResources["postgres"].MemoryBytes != 512<<20 || len(c.NeedResources) != 1 {
		t.Errorf("applying a profile changed the declaration: %+v", c.NeedResources)
	}
	over := c.WithResources(map[string]Resources{"redis": {MemoryBytes: 128 << 20}})
	if got := over.NeedResources["redis"]; got != (Resources{MemoryBytes: 128 << 20}) {
		t.Errorf("an override of a need: %+v", got)
	}
	if got := over.PartResources(); got["redis"].MemoryBytes != 128<<20 || got["web"].Declared() {
		t.Errorf("PartResources names parts and needs: %+v", got)
	}
}

// What a need's resources or a profile entry for a need cannot say is
// refused by name, and one name is a part or a need, never both.
func TestTheContractRefusesNeedSizesItCannotHonour(t *testing.T) {
	base := "app:\n  web:\n    build: .\n    public: true\n"
	for _, c := range []struct{ name, yaml, want string }{
		{"a need's memory without a unit", base + "needs:\n  postgres: { resources: { memory: 512 } }\n", "postgres: resources: memory"},
		{"a need's misspelt resources key", base + "needs:\n  postgres: { resources: { mem: 1G } }\n", `"mem" is not memory or cpu`},
		{"a need's resources as a word", base + "needs:\n  postgres: { resources: big }\n", "a map of memory and cpu"},
		{"a need's empty resources", base + "needs:\n  postgres: { resources: {} }\n", "neither memory nor cpu"},
		{"a part named as a need", "app:\n  postgres:\n    image: postgres:16\n    public: true\nneeds:\n  postgres: {}\n", `"postgres" is both a part`},
		{"a profile giving a need environment", base + "needs:\n  postgres: {}\nprofiles:\n  prod:\n    postgres: { environment: { A: b } }\n", "a need's environment is the catalogue's"},
		{"a profile naming neither", base + "needs:\n  postgres: {}\nprofiles:\n  prod:\n    redis: { resources: { memory: 1G } }\n", `"redis" is not a part under app: or a need`},
	} {
		_, err := Parse([]byte(c.yaml))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error naming %q", c.name, err, c.want)
		}
	}
}
