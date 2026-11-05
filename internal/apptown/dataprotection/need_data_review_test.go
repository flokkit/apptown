package dataprotection

import (
	"errors"
	"strings"
	"testing"
)

// Declared data that could not be found is not a completed pre-release copy.
func TestDataNamedButAbsentDoesNotPassTheCopyGate(t *testing.T) {
	s, _, client, _, pid := backupWorld(t, "reviewabsent", 40127)
	releaseMain(t, s, "reviewabsent", "app:\n  web:\n    image: x:1\n    public: true\n    dataDir: /data\n")
	hostMounts(client, nil, func(line string) ([]byte, error, bool) {
		if strings.HasPrefix(line, "volume inspect ") {
			return []byte("Error: No such volume"), errors.New("exit 1"), true
		}
		return nil, nil, false
	})
	w, err := s.Service.BeforeRelease(t.Context(), pid, "main", false)
	if err == nil && w.Complete() {
		t.Fatalf("uncopied declared data passed: %s", w.CopyWords())
	}
}

func TestAReleaseWithNoDataRecordsNothingToBackUp(t *testing.T) {
	t.Parallel()
	s, _, _, _, pid := backupWorld(t, "nodata", 40128)
	releaseMain(t, s, "nodata", "app:\n  web:\n    image: x:1\n    public: true\n")
	w, err := s.Service.BeforeRelease(t.Context(), pid, "main", false)
	if err != nil {
		t.Fatal(err)
	}
	out := w.CopyOutcome()
	if !w.Complete() || out.Outcome != "no-data" || out.Set != nil || !strings.Contains(out.Reason, "nothing to back up") {
		t.Fatalf("no data: %+v", out)
	}
}
