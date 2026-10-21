package appcontract

import (
	"strings"
	"testing"
)

func TestNeedSizeIsASelectorAndProfilesDoNotChangeSource(t *testing.T) {
	c, err := Parse([]byte("app:\n  web: {image: nginx, public: true}\nneeds:\n  mariadb: {version: '11.4'}\nprofiles:\n  preview:\n    mariadb: {size: small}\n"))
	if err != nil {
		t.Fatal(err)
	}
	preview := c.ForProfile("preview")
	if preview.NeedSizes["mariadb"].Name != "small" || len(c.NeedSizes) != 0 || len(preview.NeedSizes["mariadb"].Command) != 0 {
		t.Fatalf("source/profile selectors: %+v %+v", c.NeedSizes, preview.NeedSizes)
	}
	if _, ok := preview.NeedParams["mariadb"]["size"]; ok {
		t.Fatal("selector leaked into parameters")
	}
}
func TestNeedSizeRefusesAmbiguousOrInvalidDeclarations(t *testing.T) {
	base := "app:\n  web: {image: nginx, public: true}\n"
	for name, spec := range map[string]string{
		"number": "needs: {mariadb: {size: 2}}", "boolean": "needs: {mariadb: {size: true}}", "empty": "needs: {mariadb: {size: ''}}", "shape": "needs: {mariadb: {size: {name: small}}}",
		"source conflict":     "needs: {mariadb: {size: small, resources: {memory: 128M}}}",
		"inherited resources": "needs: {mariadb: {resources: {memory: 128M}}}\nprofiles: {preview: {mariadb: {size: small}}}",
		"inherited size":      "needs: {mariadb: {size: small}}\nprofiles: {preview: {mariadb: {resources: {memory: 128M}}}}",
		"part":                "profiles: {preview: {web: {size: small}}}", "profile scalar": "needs: {mariadb: {}}\nprofiles: {preview: {mariadb: {size: true}}}",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(base + spec + "\n")); err == nil || !strings.Contains(err.Error(), "size") {
				t.Fatalf("invalid selector accepted: %v", err)
			}
		})
	}
}
