package hostmem

import (
	"testing"
	"time"
)

// Missing or malformed cgroup values cannot turn into an idle host.
func TestCgroupMemoryExcludesInactiveFileAndChecksLimit(t *testing.T) {
	t.Parallel()
	for _, limit := range []string{"max\n", "10000\n"} {
		used, err := CgroupMemory([]byte("5000\n"), []byte("anon 3000\ninactive_file 500\nactive_file 100\n"), []byte(limit), 10000)
		if err != nil || used != 4500 {
			t.Fatalf("used %d, error %v", used, err)
		}
	}
	for _, tc := range []struct{ current, stat, limit string }{
		{"", "inactive_file 500", "max"},
		{"bad", "inactive_file 500", "max"},
		{"-1", "inactive_file 0", "max"},
		{"9223372036854775808", "inactive_file 0", "max"},
		{"5000", "anon 100", "max"},
		{"5000", "inactive_file bad", "max"},
		{"5000", "inactive_file -1", "max"},
		{"5000", "inactive_file 6000", "max"},
		{"5000", "inactive_file 0 extra", "max"},
		{"5000", "inactive_file 0\ninactive_file 0", "max"},
		{"5000", "inactive_file 0", "bad"},
		{"5000", "inactive_file 0", "9999"},
	} {
		if _, err := CgroupMemory([]byte(tc.current), []byte(tc.stat), []byte(tc.limit), 10000); err == nil {
			t.Errorf("accepted %+v", tc)
		}
	}
	used, err := CgroupMemory([]byte("0"), []byte("inactive_file 0"), []byte("max"), 10000)
	if err != nil || used != 0 {
		t.Fatalf("valid idle memory: %d, %v", used, err)
	}
}

func TestCgroupCPUUsesMicrosecondsOverElapsedTime(t *testing.T) {
	t.Parallel()
	first, err := ParseCgroupCPU([]byte("usage_usec 3000000\nuser_usec 2000000\nsystem_usec 1000000\n"))
	if err != nil || first != 3000000 {
		t.Fatalf("counter %d, %v", first, err)
	}
	used, err := CgroupUsedCores(first, 6000000, 2*time.Second)
	if err != nil || used != 1.5 {
		t.Fatalf("cores %v, %v", used, err)
	}
	for _, bad := range []string{"", "user_usec 10", "usage_usec bad", "usage_usec -1", "usage_usec 9223372036854775808", "usage_usec 1 extra", "usage_usec 1\nusage_usec 2"} {
		if _, err := ParseCgroupCPU([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, tc := range []struct {
		first, second int64
		elapsed       time.Duration
	}{{3, 2, time.Second}, {1, 2, 0}, {1, 2, -time.Second}, {-1, 2, time.Second}} {
		if _, err := CgroupUsedCores(tc.first, tc.second, tc.elapsed); err == nil {
			t.Errorf("accepted %+v", tc)
		}
	}
	if used, err := CgroupUsedCores(3, 3, time.Second); used != 0 || err != nil {
		t.Fatalf("idle: %v %v", used, err)
	}
}
