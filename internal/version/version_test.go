package version

import (
	"runtime/debug"
	"testing"
)

// A binary names its build: a release name when one was stamped, else the
// commit it was built from, marked when the tree had local changes, and
// "dev-unknown" rather than an empty or made-up name without one.
func TestAVersionNamesItsBuild(t *testing.T) {
	t.Parallel()
	built := func(kv ...string) *debug.BuildInfo {
		info := &debug.BuildInfo{}
		for i := 0; i+1 < len(kv); i += 2 {
			info.Settings = append(info.Settings, debug.BuildSetting{Key: kv[i], Value: kv[i+1]})
		}
		return info
	}
	rev := "0123456789abcdef0123456789abcdef01234567"
	for _, c := range []struct {
		name, release, stamped string
		info                   *debug.BuildInfo
		ok                     bool
		want                   string
	}{
		{"a release name wins", "v1.0.0", "", built("vcs.revision", rev), true, "v1.0.0"},
		{"a clean tree", "", "", built("vcs.revision", rev, "vcs.modified", "false"), true, "dev-0123456789ab"},
		{"a tree with local changes", "", "", built("vcs.revision", rev, "vcs.modified", "true"), true, "dev-0123456789ab-modified"},
		{"the build information wins over a stamp", "", "fedcba987654", built("vcs.revision", rev), true, "dev-0123456789ab"},
		{"a worktree build, stamped", "", "fedcba987654-modified", built(), true, "dev-fedcba987654-modified"},
		{"no revision", "", "", built(), true, "dev-unknown"},
		{"no build information", "", "", nil, false, "dev-unknown"},
	} {
		if got := of(c.release, c.stamped, c.info, c.ok); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
