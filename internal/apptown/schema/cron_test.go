package schema

import "testing"

// Cron validation: five fields or bust.
func TestParseCron(t *testing.T) {
	t.Parallel()
	if _, err := ParseCron("0 3 * * *"); err != nil {
		t.Fatalf("valid cron rejected: %v", err)
	}
	for _, bad := range []string{"", "* * *", "not a cron at all ok", "99 3 * * *"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
