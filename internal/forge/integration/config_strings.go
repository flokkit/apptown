package integration

import "strings"

// ConfigStrings reads a native configuration field accepting a string or a list.
func ConfigStrings(v any) []string {
	var out []string
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	case []string:
		out = t
	case string:
		out = append(out, strings.Fields(t)...)
	}
	return out
}
