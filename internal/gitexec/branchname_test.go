package gitexec

import (
	"math/rand"
	"strings"
	"testing"
)

// branchNameEdges are the names most likely to split the two judges: every
// rule of git-check-ref-format, the two --branch adds, and what --branch
// would expand in a repository.
var branchNameEdges = []string{
	"main", "feature/x", "a/b/c", "a-b", "a_b", "a+b", "a=b", "a,b", "a;b", "a!b", "a#b", "a'b", `a"b`, "a}b", "a{b",
	"x.y", "x..y", ".x", "x/.y", "x.", "x/y.", "x./y", "x.lock", "x.lock/y", "x/y.lock", "x.locky", "lock", ".lock",
	"x/", "/x", "x//y", "x///y", "/", "//",
	"-x", "x/-y", "--", "-", "x-",
	"HEAD", "x/HEAD", "HEAD/x", "head", "refs/heads/x", "heads/x",
	"@", "x@", "@x", "x/@", "@/x", "x@y", "@{-1}", "@{u}", "x@{u}", "x@{push}", "@@{-1}", "@{", "x{y", "x@}y", "@@",
	"x y", "x\ty", "x\ny", "x\x01y", "x\x1fy", "x\x7fy", "x~y", "x^y", "x:y", "x?y", "x*y", "x[y", "x]y", `x\y`,
	"é", "x/日本", "ñ.lock", "\u00a0", "\u200b", "\xff", "x\xc3", "\xc3\x28", "\xed\xa0\x80",
	"x..", "..", ".", "x/.", "x/..", "x/./y", "x/../y",
}

// branchNamePieces are glued at random into names: legal text, separators
// and every character a rule mentions.
var branchNamePieces = []string{
	"a", "b9", "Z", "_", "-", "+", "=", ",", "!", "#", "%", "&", "'", `"`, "}", "{", "@", "@{", ".", "..", "/", "//",
	".lock", "lock", "HEAD", "~", "^", ":", "?", "*", "[", "]", `\`, " ", "\t", "\x01", "\x1f", "\x7f",
	"é", "日本", "\xff", "\xc3", "\u2028",
}

func generatedBranchNames(n int) []string {
	r := rand.New(rand.NewSource(20261009))
	names := make([]string, 0, n)
	for len(names) < n {
		var b strings.Builder
		for k := 1 + r.Intn(6); k > 0; k-- {
			b.WriteString(branchNamePieces[r.Intn(len(branchNamePieces))])
		}
		names = append(names, b.String())
	}
	return names
}

// branchNameTestNames is every name the two judges are compared on: the
// edge cases and 2,500 generated names. gitBranchNameVerdicts records git's
// answer for exactly these, in this order.
func branchNameTestNames() []string {
	return append(append([]string{}, branchNameEdges...), generatedBranchNames(2500)...)
}

// agreesWithGit reports whether CheckBranchName's verdict on name matches
// git's. The one admitted difference is stricter, never looser: "@" alone,
// which git accepts outside a repository and inside one reads as HEAD.
func agreesWithGit(name string, git bool) bool {
	own := CheckBranchName(name) == nil
	return git == own || (git && !own && name == "@")
}

// The forge decides whether a name is a branch name without starting git.
// The decision must be git's: every edge case and thousands of generated
// names are judged by both, and any disagreement fails. Git's answers are
// the recorded table in branchname_verdicts_test.go, so the unit tier starts
// no git; the integration tier asks the installed git again
// (TestIntegrationCheckBranchNameAgreesWithGit) and fails if the table is
// stale.
func TestCheckBranchNameAgreesWithGitsRecordedAnswers(t *testing.T) {
	names := branchNameTestNames()
	if len(gitBranchNameVerdicts) != len(names) {
		t.Fatalf("the recorded table has %d names, the test names are %d; regenerate it (see branchname_verdicts_test.go)", len(gitBranchNameVerdicts), len(names))
	}
	accepted := 0
	for i, v := range gitBranchNameVerdicts {
		if v.name != names[i] {
			t.Fatalf("recorded name %d is %q, the test names say %q; regenerate the table (see branchname_verdicts_test.go)", i, v.name, names[i])
		}
		if v.git {
			accepted++
		}
		if !agreesWithGit(v.name, v.git) {
			t.Errorf("%q: git accepts=%v, CheckBranchName accepts=%v", v.name, v.git, !v.git)
		}
	}
	// Generated names that git nearly always refuses would test nothing.
	if accepted < len(names)/5 {
		t.Fatalf("only %d of %d names were branch names; the generator tests too little", accepted, len(names))
	}
}

// In a repository, --branch expands "@" to HEAD and @{-N} to an earlier
// checkout. A name the forge stores must mean itself: none is accepted.
func TestCheckBranchNameRefusesWhatGitWouldExpand(t *testing.T) {
	for _, name := range []string{"@", "@{-1}", "x@{u}", "@{push}"} {
		if CheckBranchName(name) == nil {
			t.Errorf("%q was accepted", name)
		}
	}
}
