package routinechange

import (
	"strings"
	"testing"
)

func TestImageComparisonExplainsOnlyThePinnedDigest(t *testing.T) {
	before := "FROM --platform=linux/amd64 docker.io/library/node:22.14.0-bookworm-slim@sha256:" + strings.Repeat("a", 64) + "\nUSER node\nCMD [\"node\",\"app.js\"]\n"
	after := strings.Replace(before, strings.Repeat("a", 64), strings.Repeat("b", 64), 1)
	c, err := CompareImage([]byte(before), []byte(after))
	if err != nil || c.Version != "22.14.0" || c.Platform != "linux/amd64" {
		t.Fatalf("digest update: %+v %v", c, err)
	}
	cases := map[string]string{
		"command":      strings.Replace(after, "app.js", "other.js", 1),
		"user":         strings.Replace(after, "USER node", "USER root", 1),
		"version":      strings.Replace(after, "22.14.0", "22.14.1", 1),
		"repository":   strings.Replace(after, "library/node", "attacker/node", 1),
		"variant":      strings.Replace(after, "bookworm-slim", "alpine", 1),
		"platform":     strings.Replace(after, "linux/amd64", "linux/arm64", 1),
		"no platform":  strings.Replace(after, "--platform=linux/amd64 ", "", 1),
		"tag":          strings.Replace(after, "@sha256:"+strings.Repeat("b", 64), "", 1),
		"extra stage":  after + "FROM scratch\n",
		"argument":     "ARG VERSION=22.14.0\n" + after,
		"directive":    "# syntax=some/frontend\n" + after,
		"continuation": after + "RUN echo \\\n  hello\n",
		"heredoc":      after + "RUN <<EOF\necho hi\nEOF\n",
		"unchanged":    before,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := CompareImage([]byte(before), []byte(input)); err == nil {
				t.Fatal("unexplained image change accepted")
			}
		})
	}
	// Unsupported syntax is refused even when preserved on both sides.
	for _, prefix := range []string{"# escape=`\n", "# syntax = custom/frontend\n", "ARG X=1\n"} {
		if _, err := CompareImage([]byte(prefix+before), []byte(prefix+after)); err == nil {
			t.Fatal("unsupported baseline accepted")
		}
	}
}
