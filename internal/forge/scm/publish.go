package scm

import "flokkit.io/forge/internal/strs"

func publishDiagnostic(s string) string {
	return strs.Truncate(s, 300)
}
