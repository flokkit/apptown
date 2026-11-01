// Package version says what build a binary is. A release build sets Release
// with -ldflags "-X github.com/flokkit/apptown/internal/version.Release=v1.0.0";
// every other build names its commit from Go's build information.
//
// Which binaries work together is not decided by this string but by the
// protocol numbers each side states (worker.Protocol*, CLIProtocol here).
package version

import "runtime/debug"

// release is the release name stamped at build, empty for other builds.
var release string

// revision is the commit stamped by make and the deploy scripts, for a
// build whose Go build information names none: Go does not stamp a build
// made in a git worktree, which is where deploys are built.
var revision string

// String is the version this binary reports.
func String() string {
	info, ok := debug.ReadBuildInfo()
	return of(release, revision, info, ok)
}

// of is String's rule over its inputs: a release name wins; otherwise
// dev-<revision>, -modified when the tree had local changes, from the build
// information or else the stamped revision; dev-unknown with neither.
func of(release, stamped string, info *debug.BuildInfo, ok bool) string {
	if release != "" {
		return release
	}
	rev, modified := "", false
	if ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
	}
	if rev == "" {
		if stamped != "" {
			return "dev-" + stamped
		}
		return "dev-unknown"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if modified {
		return "dev-" + rev + "-modified"
	}
	return "dev-" + rev
}

// The CLI protocol says which CLIs an AppTown accepts (design/operations,
// stock/docs/guides/upgrading.md, "Versions"). The CLI states the number it speaks on
// every request; an AppTown accepts a range and refuses another by name. A CLI
// an AppTown hands its agents is built from AppTown's own tree, so it always
// matches; a person's own CLI can lag or lead.
const (
	CLIProtocolHeader = "X-AppTown-CLI-Protocol"
	// CLIProtocol is what this build's CLI speaks.
	CLIProtocol = 1
	// CLIProtocolMin and CLIProtocolMax are what this build's AppTown accepts.
	CLIProtocolMin = 1
	CLIProtocolMax = 1
	// Header names AppTown's version on every API response.
	Header = "X-AppTown-Version"
)
