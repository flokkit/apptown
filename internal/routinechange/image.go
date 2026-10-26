package routinechange

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
)

// ImageCandidate explains the sole changed operand of a supported Dockerfile.
// Registry/config/runtime observations are still required; tags are not proof.
type ImageCandidate struct {
	Before, After     string
	Version, Platform string
}

var dockerDirective = regexp.MustCompile(`(?i)^#\s*(syntax|escape)\s*=`)

var nodeFrom = regexp.MustCompile(`^FROM --platform=(linux/(?:amd64|arm64)) (docker\.io/library/node:((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))-[a-z0-9][a-z0-9.-]*@sha256:[a-f0-9]{64})$`)

// CompareImage supports one explicit-platform, digest-pinned Node FROM. All
// other bytes must remain identical; unsupported Dockerfile syntax refuses.
func CompareImage(before, after []byte) (ImageCandidate, error) {
	var out ImageCandidate
	parse := func(raw []byte) (reference, version, platform, masked string, err error) {
		if bytes.ContainsAny(raw, "\\\x00\r") || bytes.Contains(raw, []byte("<<")) {
			err = errors.New("Dockerfile continuation, heredoc or control syntax requires a person")
			return
		}
		found := false
		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if strings.HasPrefix(trimmed, "#") {
				if dockerDirective.MatchString(trimmed) {
					err = errors.New("Dockerfile parser directives require a person")
					return
				}
				continue
			}
			fields := strings.Fields(trimmed)
			if strings.EqualFold(fields[0], "FROM") {
				if found {
					err = errors.New("multiple Dockerfile stages require a person")
					return
				}
				match := nodeFrom.FindStringSubmatch(line)
				if match == nil {
					err = errors.New("image needs one explicit-platform, exact-version official Node digest")
					return
				}
				platform, reference, version = match[1], match[2], match[3]
				found = true
			} else if !found {
				err = errors.New("Dockerfile instructions before FROM require a person")
				return
			}
		}
		if !found {
			err = errors.New("Dockerfile has no supported base image")
			return
		}
		_, digest, _ := strings.Cut(reference, "@")
		// Only replace the FROM operand, never a matching digest elsewhere.
		full := "FROM --platform=" + platform + " " + reference
		masked = strings.Replace(string(raw), full, strings.TrimSuffix(full, digest)+"<digest>", 1)
		return
	}
	br, bv, bp, bm, err := parse(before)
	if err != nil {
		return out, err
	}
	ar, av, ap, am, err := parse(after)
	if err != nil {
		return out, err
	}
	if bm != am || bv != av || bp != ap {
		return out, errors.New("Dockerfile changes more than a same-version base image digest")
	}
	if br == ar {
		return out, errors.New("the base image digest did not change")
	}
	return ImageCandidate{Before: br, After: ar, Version: bv, Platform: bp}, nil
}
