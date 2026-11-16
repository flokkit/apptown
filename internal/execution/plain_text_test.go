package execution

import "testing"

func TestPlainText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, input, want string }{
		{"CSI colour", "\x1b[32mok\x1b[0m\n", "ok\n"},
		{"cursor moves", "a\x1b[2K\x1b[1Gb", "ab"},
		{"OSC BEL", "\x1b]8;;https://example.com\alink\x1b]8;;\a", "link"},
		{"OSC ST", "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\", "link"},
		{"redraw", "first\rsecond\rfinal\nnext\rkept", "final\nkept"},
		{"CRLF", "one\r\ntwo\r\n", "one\ntwo\n"},
		{"plain", "hello\n世界\n", "hello\n世界\n"},
		{"trailing ESC", "ok\x1b", "ok"},
		{"two byte escape", "a\x1b7b\x1b8", "ab"},
		{"invalid UTF-8", "\xff\xfe", "\xff\xfe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(PlainText([]byte(tc.input))); got != tc.want {
				t.Fatalf("PlainText(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
