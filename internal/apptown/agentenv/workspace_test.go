package agentenv

import "testing"

// Each workspace gets its own mise install/shim dir key — projects can't poison
// each other — and the key is filesystem-safe.
func TestMiseWorkspaceKeyIsolatesAndSanitizes(t *testing.T) {
	t.Parallel()
	if a, b := miseWorkspaceKey("/data/workspaces/change-164"), miseWorkspaceKey("/data/workspaces/project-office-poll"); a == b {
		t.Fatalf("distinct workspaces share a mise key: %q == %q", a, b)
	}
	if k := miseWorkspaceKey("/data/workspaces/change-164"); k != "change-164" {
		t.Fatalf("clean base mangled: %q", k)
	}
	if k := miseWorkspaceKey("/x/weird name/../evil$"); k == "" || k == "." || k == ".." ||
		containsAny(k, "/ $") {
		t.Fatalf("unsafe key: %q", k)
	}
	if miseWorkspaceKey("") != "default" {
		t.Fatal("empty ws should fall back to a safe default")
	}
}

func containsAny(s, chars string) bool {
	for _, c := range chars {
		for _, r := range s {
			if r == c {
				return true
			}
		}
	}
	return false
}
