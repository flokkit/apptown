package stock

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A pack's description is a short first paragraph (what it is) and a longer
// one. The second must not start by repeating the first, which a card that
// shows both would print twice.
func TestPackDescriptionsDoNotRepeatTheirFirstParagraph(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("packs.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Packs []struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		} `yaml:"packs"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Packs) == 0 {
		t.Fatal("no packs read from packs.yaml")
	}
	for _, p := range file.Packs {
		paras := strings.Split(strings.TrimSpace(p.Description), "\n\n")
		if len(paras) < 2 {
			continue
		}
		first := strings.TrimRight(strings.TrimSpace(paras[0]), ".:")
		if strings.HasPrefix(strings.TrimSpace(paras[1]), first) {
			t.Errorf("pack %s: its second paragraph repeats the first (%q)", p.Name, first)
		}
	}
}
