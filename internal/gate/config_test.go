package gate

import (
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func validEnv() map[string]string {
	return map[string]string{
		"GATE_UPSTREAM": "http://web:8080",
		"GATE_SECRET":   testSecret,
		"GATE_APPTOWN":  "https://forge.example",
	}
}

func TestConfigAcceptsTheDocumentedEnvironment(t *testing.T) {
	cfg, err := ConfigFromEnv(envOf(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != DefaultListen || cfg.Upstream.String() != "http://web:8080" ||
		cfg.Forge.String() != "https://forge.example" || cfg.Secret != testSecret {
		t.Fatalf("got %+v", cfg)
	}
	env := validEnv()
	env["GATE_LISTEN"] = ":9090"
	env["GATE_APPTOWN"] = "http://localhost:7777/"
	cfg, err = ConfigFromEnv(envOf(env))
	if err != nil || cfg.Listen != ":9090" || cfg.Forge.String() != "http://localhost:7777" {
		t.Fatalf("got %+v %v", cfg, err)
	}
}

func TestConfigRefusesMissingOrMalformedValuesWithoutEchoingTheSecret(t *testing.T) {
	cases := map[string]map[string]string{
		"no upstream":           {"GATE_UPSTREAM": ""},
		"https upstream":        {"GATE_UPSTREAM": "https://web:8080"},
		"upstream without host": {"GATE_UPSTREAM": "http://"},
		"no secret":             {"GATE_SECRET": ""},
		"short secret":          {"GATE_SECRET": testSecret[:62]},
		"uppercase secret":      {"GATE_SECRET": strings.ToUpper(testSecret)},
		"non-hex secret":        {"GATE_SECRET": strings.Repeat("g", 64)},
		"no AppTown":            {"GATE_APPTOWN": ""},
		"http forge":            {"GATE_APPTOWN": "http://forge.example"},
		"apptown with path":     {"GATE_APPTOWN": "https://forge.example/api"},
		"apptown not a url":     {"GATE_APPTOWN": "forge.example"},
	}
	for name, override := range cases {
		t.Run(name, func(t *testing.T) {
			env := validEnv()
			for k, v := range override {
				env[k] = v
			}
			_, err := ConfigFromEnv(envOf(env))
			if err == nil {
				t.Fatal("accepted")
			}
			if strings.Contains(err.Error(), testSecret[:16]) || strings.Contains(strings.ToLower(err.Error()), testSecret[:16]) {
				t.Fatalf("error echoes the secret: %v", err)
			}
		})
	}
}
