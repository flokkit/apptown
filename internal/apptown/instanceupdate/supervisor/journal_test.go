package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	short, err := os.MkdirTemp("/tmp", "u2-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(short); err != nil {
			t.Error(err)
		}
	})
	dir, err := filepath.EvalSymlinks(short)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	r, err := SealRegistration(Registration{Format: Protocol, Instance: ids.New(now), DataDir: "/var/lib/apptown", StateDir: dir, DatabaseIdentity: strings.Repeat("a", 64), Supervisor: "/usr/libexec/apptown-update", SupervisorSHA256: strings.Repeat("b", 64), Adapter: "systemd", Service: "apptown-managed.service", UnitSHA256: strings.Repeat("c", 64), EnvironmentFile: "/etc/apptown/managed.env", EnvironmentSHA256: strings.Repeat("f", 64), ReleaseDir: "/opt/apptown/releases/old", ExecutableSHA256: strings.Repeat("d", 64), EnrolledAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRecord(dir, "journal.json", journal{Format: Protocol, ConfigurationDigest: r.ConfigurationDigest}, true); err != nil {
		t.Fatal(err)
	}
	if err := writeRecord(dir, "registration.json", r, true); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}
func intention(s *Store) Intention {
	return Intention{Request: ids.New(time.Now()), Instance: s.registration.Instance, ConfigurationDigest: s.registration.ConfigurationDigest, CandidateSHA256: strings.Repeat("e", 64), Principal: ids.New(time.Now()), CredentialScope: "interactive"}
}
func TestStageIsDurableIdempotentAndNotReplacementPermission(t *testing.T) {
	s := testStore(t)
	in := intention(s)
	one, err := s.Stage(in, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.Stage(in, time.Now().Add(time.Hour))
	if err != nil || one != two {
		t.Fatalf("replay: %+v %v", two, err)
	}
	changed := in
	changed.CandidateSHA256 = strings.Repeat("f", 64)
	if _, err := s.Stage(changed, time.Now()); err == nil {
		t.Fatal("accepted changed request identity")
	}
	if _, err := s.Stage(intention(s), time.Now()); err == nil {
		t.Fatal("accepted overlapping request")
	}
	if err := s.OrdinaryStart(); err != nil {
		t.Fatal("staging must not stop original", err)
	}
	if _, err := Open(s.registration.StateDir); err == nil {
		t.Fatal("second supervisor acquired exclusion")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.registration.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Transactions()
	if err != nil || len(got) != 1 || got[0] != one {
		t.Fatalf("lost durable intention: %v %v", got, err)
	}
	if _, err := reopened.Cancel(in.Request); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Stage(intention(reopened), time.Now()); err != nil {
		t.Fatal(err)
	}
}
func TestEveryMaintenancePhaseFencesFreshStartupAndCancellation(t *testing.T) {
	for _, phase := range []string{"draining", "stopped", "snapshot-verified", "candidate-installed", "verifying", "opening", "running", "recovering", "recovered"} {
		t.Run(phase, func(t *testing.T) {
			s := testStore(t)
			in := intention(s)
			// No U2 operation enters these phases. Represent durable later-coordinator
			// evidence to prove an older supervisor cannot restart across their fence.
			j := journal{Format: Protocol, ConfigurationDigest: s.registration.ConfigurationDigest, Transactions: []Transaction{{Intention: in, RequestedAt: time.Now(), Phase: phase}}}
			if err := writeRecord(s.registration.StateDir, "journal.json", j, false); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			fresh, err := Open(s.registration.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			if err := fresh.OrdinaryStart(); err == nil {
				t.Fatal("started ordinary daemon through maintenance fence")
			}
			if _, err := fresh.Cancel(in.Request); err == nil {
				t.Fatal("cleared maintenance fence as ordinary cancellation")
			}
		})
	}
}
func TestMissingCorruptAndLinkedJournalsNeverMeanIdle(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			s := testStore(t)
			path := filepath.Join(s.registration.StateDir, "journal.json")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "corrupt":
				if err := os.WriteFile(path, []byte(`{"format":1}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("registration.json", path); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.OrdinaryStart(); err == nil {
				t.Fatal("missing evidence allowed startup")
			}
		})
	}
}
func TestRegistrationRefusesHostilePathsAndServices(t *testing.T) {
	s := testStore(t)
	for _, edit := range []func(*Registration){func(r *Registration) { r.Service = "--user" }, func(r *Registration) { r.StateDir = r.DataDir + "/control" }, func(r *Registration) { r.Supervisor = r.ReleaseDir + "/bin/apptown-update" }, func(r *Registration) { r.DataDir = "/var/lib/../apptown" }} {
		r := s.registration
		edit(&r)
		if _, err := SealRegistration(r); err == nil {
			t.Fatal("accepted unsafe registration")
		}
	}
}
