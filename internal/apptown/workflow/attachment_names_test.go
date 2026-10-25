package workflow

import "testing"

func TestSafeAttachmentName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"screenshot.png":   "screenshot.png",
		"  spec.md  ":      "spec.md",
		"a/b/c/report.csv": "report.csv", // base name only — no path traversal
		"":                 "",
		".":                "",
		"..":               "",
		".hidden":          "", // leading dot rejected (dotfile escape)
		"../../etc/passwd": "passwd",
	}
	for in, want := range cases {
		if got, err := attachmentName(in); got != want || (err != nil) != (want == "") {
			t.Errorf("safeAttachmentName(%q) = %q, want %q", in, got, want)
		}
	}
}
