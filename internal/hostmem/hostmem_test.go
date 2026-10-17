package hostmem

import "testing"

// The total and what is available are read as the kernel lists them, in
// bytes; a listing without either is an error, never zero.
//
// Kills: a missing MemAvailable read as nothing available; kB not scaled.
func TestTheHostsMemoryIsReadAsMeminfoListsIt(t *testing.T) {
	t.Parallel()
	m, err := ParseMeminfo([]byte("MemTotal:        8388608 kB\nMemFree:         2002984 kB\nMemAvailable:    5041354 kB\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m.TotalBytes != 8388608*1024 || m.AvailableBytes != 5041354*1024 || m.UsedBytes() != (8388608-5041354)*1024 {
		t.Errorf("read as %+v", m)
	}
	for _, bad := range []string{
		"MemTotal:        8388608 kB\nMemFree:         2002984 kB\n",
		"MemAvailable:    5041354 kB\n",
		"MemTotal:        100 kB\nMemAvailable:    200 kB\n",
		"MemTotal:        lots kB\nMemAvailable:    5 kB\n",
	} {
		if m, err := ParseMeminfo([]byte(bad)); err == nil {
			t.Errorf("%q read as %+v", bad, m)
		}
	}
}
