package gitexec

import (
	"fmt"
	"strings"
)

// CheckBranchName refuses a name that is not a branch name as `git
// check-ref-format --branch` judges one, without starting git. It applies
// git's reference-name rules to refs/heads/<name> (git-check-ref-format(1)):
// no component empty, starting with '.' or ending with ".lock"; no "..",
// "@{", control character, space, '~', '^', ':', '?', '*', '[' or '\';
// not ending with '.'. --branch adds: not starting with '-', and not HEAD.
//
// It never expands a name. In a repository, --branch reads "@" alone as
// HEAD and @{-N} as an earlier checkout; a name AppTown stores must mean
// itself, so "@" is refused here (git outside a repository accepts it), and
// any "@{" is refused by the rules.
func CheckBranchName(name string) error {
	if strings.HasPrefix(name, "-") || name == "HEAD" || name == "@" || !validRefName("refs/heads/"+name) {
		return fmt.Errorf("%q is not a valid branch name", name)
	}
	return nil
}

// validRefName is git's check_refname_format with no options.
func validRefName(ref string) bool {
	if strings.HasSuffix(ref, ".") {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") ||
			strings.Contains(part, "..") || strings.Contains(part, "@{") {
			return false
		}
		for i := 0; i < len(part); i++ {
			if c := part[i]; c < 0x20 || c == 0x7f || strings.IndexByte(" ~^:?*[\\", c) >= 0 {
				return false
			}
		}
	}
	return true
}
