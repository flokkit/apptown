package appcontract_test

import (
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/appcontract"
)

func TestContractVersionAndStrictDecode(t *testing.T) {
	base := "app:\n  web:\n    static: .\n    public: true\n"
	// Absent version → v1, valid.
	if _, err := appcontract.Parse([]byte(base)); err != nil {
		t.Fatalf("no version must parse as v1: %v", err)
	}
	// Explicit version: 1 valid.
	if _, err := appcontract.Parse([]byte("version: 1\n" + base)); err != nil {
		t.Fatalf("version 1: %v", err)
	}
	// version: 0 is invalid.
	if _, err := appcontract.Parse([]byte("version: 0\n" + base)); err == nil {
		t.Fatal("version 0 must be rejected")
	}
	// A future version fails with a clear message.
	_, err := appcontract.Parse([]byte("version: 99\n" + base))
	if err == nil || !strings.Contains(err.Error(), "requires a newer forge") {
		t.Fatalf("unsupported version must name the need: %v", err)
	}
	// An unknown/misspelled field is rejected, not silently dropped.
	if _, err := appcontract.Parse([]byte("app:\n  web:\n    static: .\n    public: true\n    healthcheque: x\n")); err == nil {
		t.Fatal("a misspelled field must be rejected (strict decode)")
	}
	// A known contract with init + needs still round-trips through the reader.
	full := "version: 1\napp:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\ninit:\n  - {name: migrate, build: ., command: [\"x\"]}\nneeds:\n  postgres: {version: \"16\"}\n"
	c, err := appcontract.Parse([]byte(full))
	if err != nil {
		t.Fatalf("full contract: %v", err)
	}
	if len(c.Init) != 1 || len(c.Needs) != 1 || c.Needs[0] != "postgres" {
		t.Fatalf("init/needs lost: %+v", c)
	}
}

// v1 semantics are part of the contract version, not free to drift: the
// default ports and the one-public-part rule are pinned so a change to them
// is caught as the breaking change the migration strategy says it is
// (design/subsystems/migration-strategy.md §1).
func TestContractV1SemanticsArePinned(t *testing.T) {
	// A static part with no port defaults to 80.
	c, err := appcontract.Parse([]byte("app:\n  web:\n    static: .\n    public: true\n"))
	if err != nil || c.PublicPart().Port != 80 {
		t.Fatalf("v1: a static app defaults to port 80, got %d (%v)", c.PublicPart().Port, err)
	}
	// A server part with no port defaults to 8080.
	c, err = appcontract.Parse([]byte("app:\n  web:\n    build: .\n    public: true\n"))
	if err != nil || c.PublicPart().Port != 8080 {
		t.Fatalf("v1: a server app defaults to port 8080, got %d (%v)", c.PublicPart().Port, err)
	}
	// An explicit port is kept, not defaulted.
	c, err = appcontract.Parse([]byte("app:\n  web:\n    build: .\n    ports: [\"3000\"]\n    public: true\n"))
	if err != nil || c.PublicPart().Port != 3000 {
		t.Fatalf("v1: an explicit port survives, got %d (%v)", c.PublicPart().Port, err)
	}
	// Exactly one public part is required.
	if _, err := appcontract.Parse([]byte("app:\n  a:\n    build: .\n  b:\n    build: .\n")); err == nil {
		t.Fatal("v1: zero public parts must be rejected")
	}
	if _, err := appcontract.Parse([]byte("app:\n  a:\n    build: .\n    public: true\n  b:\n    build: .\n    public: true\n")); err == nil {
		t.Fatal("v1: two public parts must be rejected")
	}
}
