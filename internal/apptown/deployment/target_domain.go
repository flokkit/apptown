package deployment

import (
	"net"
	"strings"
)

// normalizeTargetDomain validates an ingress suffix for generated project
// hostnames. Empty input clears the suffix; one leading wildcard is optional.
// This accepts DNS ASCII names, not URLs, ports or IP addresses.
func normalizeTargetDomain(input string) (string, error) {
	domain := strings.TrimSpace(input)
	if domain == "" {
		return "", nil
	}
	domain = strings.TrimPrefix(domain, "*.")
	name := strings.TrimSuffix(domain, ".")
	if name == "" || len(name) > 253 || net.ParseIP(name) != nil {
		return "", errTargetDomain
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errTargetDomain
		}
		for _, c := range label {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
				continue
			}
			return "", errTargetDomain
		}
	}
	return domain, nil
}
