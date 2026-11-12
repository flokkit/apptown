package verification

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/fix"
)

func TestUpdateCoverageRequiresCompleteFixDeclarationsForEveryToolPart(t *testing.T) {
	contract := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Tools: []string{"node"}}, {Name: "worker", Tools: []string{"go"}}, {Name: "static"}}}
	node := fix.Declaration{Kind: "update", AppliesTo: []string{"tool:node"}, Run: "update", RunFor: "update {packages}", List: "list", Ecosystem: "npm"}
	goFix := node
	goFix.AppliesTo = []string{"tool:go"}
	goFix.Ecosystem = "Go"
	for _, field := range []string{"complete", "run", "runFor", "list", "absent", "kind"} {
		t.Run(field, func(t *testing.T) {
			selected := goFix
			switch field {
			case "kind":
				selected.Kind = "unknown"
			case "run":
				selected.Run = ""
			case "runFor":
				selected.RunFor = ""
			case "list":
				selected.List = ""
			}
			declarations := []fix.Declaration{node, selected}
			if field == "absent" {
				declarations = declarations[:1]
			}
			missing, err := UpdateCoverage(contract, declarations)
			if err != nil {
				t.Fatal(err)
			}
			if field == "complete" {
				if len(missing) != 0 {
					t.Fatalf("complete declarations refused: %+v", missing)
				}
			} else if len(missing) != 1 || missing[0].Name != "worker" {
				t.Fatalf("incomplete declarations covered a tool part: %+v", missing)
			}
		})
	}
}

func TestPreviewNamesPartsWithoutMajorCommands(t *testing.T) {
	c, err := appcontract.Parse([]byte("app:\n  api:\n    build: api\n    public: true\n    tools: [go]\n  ui:\n    build: ui\n    tools: [node]\n"))
	if err != nil {
		t.Fatal(err)
	}
	ds := []fix.Declaration{{Kind: "update", AppliesTo: []string{"tool:go"}, Ecosystem: "Go", Run: "go get -u=patch ./..."}, {Kind: "update", AppliesTo: []string{"tool:node"}, Ecosystem: "npm", Run: "npm update", RunMajor: "npm install example@latest"}}
	got, err := PartsWithoutMajorCommand(c, ds)
	if err != nil || len(got) != 1 || !strings.Contains(got[0], "api (Go)") || !strings.Contains(got[0], "major versions not offered") {
		t.Fatalf("preview %+v %v", got, err)
	}
}
