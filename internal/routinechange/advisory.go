package routinechange

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// AdvisoryFix is a fixed-version claim from a parsed advisory, not permission
// to change a dependency or proof that software has no other vulnerabilities.
type AdvisoryFix struct{ ID, Modified, Package, Before, After string }

// NPMAdvisoryFix interprets only complete npm SEMVER introduced/fixed ranges.
// Every matching affected entry participates, including explicit versions.
func NPMAdvisoryFix(raw []byte, pkg, before, after string) (AdvisoryFix, error) {
	var out AdvisoryFix
	if _, err := object(raw); err != nil {
		return out, err
	}
	var doc struct {
		ID, Modified, Withdrawn string
		Affected                []struct {
			Package  struct{ Name, Ecosystem string }
			Versions []string
			Ranges   []struct {
				Type, Repo string
				Events     []map[string]string
			}
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return out, err
	}
	if doc.ID == "" || doc.Withdrawn != "" {
		return out, errors.New("advisory identity missing or withdrawn")
	}
	if _, err := time.Parse(time.RFC3339, doc.Modified); err != nil {
		return out, errors.New("advisory has no valid modified timestamp")
	}
	b, err := advisoryVersion(before)
	if err != nil {
		return out, err
	}
	a, err := advisoryVersion(after)
	if err != nil {
		return out, err
	}
	if slices.Compare(a, b) <= 0 {
		return out, errors.New("advisory attribution needs a forward version change")
	}
	oldAffected, newAffected, fixed, matched := false, false, false, false
	for _, affected := range doc.Affected {
		if affected.Package.Ecosystem != "npm" || affected.Package.Name != pkg {
			continue
		}
		matched = true
		if len(affected.Versions) == 0 && len(affected.Ranges) == 0 {
			return out, errors.New("matching advisory has no affected-version evidence")
		}
		for _, version := range affected.Versions {
			v, err := advisoryVersion(version)
			if err != nil {
				return out, err
			}
			oldAffected = oldAffected || slices.Equal(v, b)
			newAffected = newAffected || slices.Equal(v, a)
		}
		for _, r := range affected.Ranges {
			if r.Type != "SEMVER" || r.Repo != "" || len(r.Events) == 0 {
				return out, errors.New("matching advisory range is unsupported")
			}
			var lower []uint64
			open := false
			for _, event := range r.Events {
				if len(event) != 1 {
					return out, errors.New("ambiguous advisory event")
				}
				if introduced, ok := event["introduced"]; ok {
					if open {
						return out, errors.New("overlapping advisory introductions")
					}
					if introduced == "0" {
						lower = []uint64{0, 0, 0}
					} else {
						lower, err = advisoryVersion(introduced)
						if err != nil {
							return out, err
						}
					}
					open = true
					continue
				}
				upperText, ok := event["fixed"]
				if !ok || !open {
					return out, errors.New("unsupported or unordered advisory boundary")
				}
				upper, err := advisoryVersion(upperText)
				if err != nil {
					return out, err
				}
				if slices.Compare(lower, upper) >= 0 {
					return out, errors.New("invalid advisory interval")
				}
				oldHere := slices.Compare(b, lower) >= 0 && slices.Compare(b, upper) < 0
				newHere := slices.Compare(a, lower) >= 0 && slices.Compare(a, upper) < 0
				oldAffected = oldAffected || oldHere
				newAffected = newAffected || newHere
				fixed = fixed || oldHere && slices.Compare(a, upper) >= 0
				open = false
			}
			if open {
				return out, errors.New("open-ended matching advisory ranges are unsupported")
			}
		}
	}
	if !matched || !oldAffected || newAffected || !fixed {
		return out, errors.New("advisory does not prove this exact affected-to-fixed transition")
	}
	return AdvisoryFix{ID: doc.ID, Modified: doc.Modified, Package: pkg, Before: before, After: after}, nil
}
func advisoryVersion(s string) ([]uint64, error) {
	if !stableVersion.MatchString(s) {
		return nil, fmt.Errorf("unsupported advisory version %q", s)
	}
	var out []uint64
	for _, part := range strings.Split(s, ".") {
		n, e := strconv.ParseUint(part, 10, 64)
		if e != nil {
			return nil, e
		}
		out = append(out, n)
	}
	return out, nil
}
