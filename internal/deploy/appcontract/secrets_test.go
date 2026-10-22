package appcontract_test

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

func TestSecretsContractMetadata(t *testing.T) {
	c, err := appcontract.Parse([]byte("app:\n  web: { image: x, public: true }\nsecrets:\n  generate:\n    KEY: project\n"))
	if err != nil || c.SecretGenerate["KEY"] != "project" {
		t.Fatalf("generate domain: %v %v", c, err)
	}
	if _, err := appcontract.Parse([]byte("app:\n  web: { image: x, public: true }\nsecrets:\n  generate:\n    KEY: weekly\n")); err == nil {
		t.Fatal("an unknown stability domain must be refused")
	}
	// The domain is called a deployment, as everything else is; the older
	// word is refused with the new one, so a v1 contract has one word for it.
	if c, err := appcontract.Parse([]byte("app:\n  web: { image: x, public: true }\nsecrets:\n  generate:\n    KEY: deployment\n")); err != nil || c.SecretGenerate["KEY"] != "deployment" {
		t.Fatalf("deployment domain: %v %v", c, err)
	}
	if _, err := appcontract.Parse([]byte("app:\n  web: { image: x, public: true }\nsecrets:\n  generate:\n    KEY: environment\n")); err == nil || !strings.Contains(err.Error(), "write KEY: deployment") {
		t.Fatalf("the old word must be refused naming the new one: %v", err)
	}
	if _, err := appcontract.Parse([]byte("app:\n  web: { image: x, public: true }\nsecrets:\n  deliver:\n    KEY: file\n")); err == nil || !strings.Contains(err.Error(), "not built yet") {
		t.Fatalf("file delivery must say it is not built: %v", err)
	}
}
