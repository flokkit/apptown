package scm

import "github.com/flokkit/apptown/internal/strs"

func publishDiagnostic(s string) string {
	return strs.Truncate(s, 300)
}
