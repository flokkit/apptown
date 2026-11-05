package wow

import (
	"strings"
	"testing"
)

func TestContextDeclarationIsBoundedRepositoryData(t *testing.T) {
	body := `<!-- context: [{"path":"PROJECT-NOTES.md","maxBytes":6144}] -->` + "\nKeep durable facts."
	rules, files, err := SplitContext(body)
	if err != nil || len(files) != 1 || files[0].Path != "PROJECT-NOTES.md" || files[0].MaxBytes != 6144 || rules != "Keep durable facts." {
		t.Fatalf("%s %+v %v", rules, files, err)
	}
	for _, bad := range []string{
		`<!-- context: [{"path":"../secret","maxBytes":100}] -->`,
		`<!-- context: [{"path":"/secret","maxBytes":100}] -->`,
		`<!-- context: [{"path":".git/config","maxBytes":100}] -->`,
		`<!-- context: [{"path":"notes","maxBytes":0}] -->`,
		`<!-- context: [{"path":"notes","maxBytes":65537}] -->`,
		`<!-- context: [{"path":"notes","maxBytes":10,"typo":true}] -->`,
		body + "\n" + body,
	} {
		if _, _, err := SplitContext(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if rules, files, err := SplitContext("Existing prose."); err != nil || rules != "Existing prose." || len(files) > 0 {
		t.Fatal("existing guidance changed")
	}
	if strings.Contains(rules, "<!--") {
		t.Fatal("declaration leaked into rules")
	}
}
