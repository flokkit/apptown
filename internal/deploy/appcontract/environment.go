package appcontract

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// reservedEnvPrefix begins the names AppTown keeps for itself: a variable
// AppTown adds in a 1.x release starts with it, so no app that followed
// the contract already uses that name.
const reservedEnvPrefix = "APPTOWN_"

// ReservedEnvName says why an app may not set name in a part's environment
// or declare it as a secret, or "" when it may: AppTown sets it on every
// part (AppTownSetEnv), or it starts with ReservedEnvPrefix.
func ReservedEnvName(name string) string {
	switch {
	case slices.Contains(appTownSetEnv, name):
		return "only AppTown sets it"
	case strings.HasPrefix(name, reservedEnvPrefix):
		return "names starting " + reservedEnvPrefix + " are AppTown's"
	}
	return ""
}

// environment is an environment: block as written. Values are kept as
// nodes so that what reaches the app is the text in the file, never a
// reading of it: 3.10 is not 3.1, and an empty value is not "<nil>".
type environment map[string]yaml.Node

// read gives the block's values, refusing a name a shell cannot set, a
// name AppTown keeps (every reserved name when forPart, only the
// ReservedEnvPrefix ones for an init step, which AppTown gives no
// variables), and any value that is not text, a whole number or a
// boolean written the way the app receives it.
func (e environment) read(forPart bool) (map[string]string, error) {
	if len(e) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(e))
	for _, name := range sortedKeys(e) {
		if !envNameRe.MatchString(name) {
			return nil, fmt.Errorf("environment: %q is not an environment variable name (letters, digits and _, not starting with a digit)", name)
		}
		if why := ReservedEnvName(name); why != "" && (forPart || strings.HasPrefix(name, reservedEnvPrefix)) {
			return nil, fmt.Errorf("environment: %s is AppTown's (%s); name it something else", name, why)
		}
		n := e[name]
		v, ok := envValue(&n)
		if !ok {
			return nil, fmt.Errorf("environment: %s: %s; write the value in quotes, as the app should receive it", name, envShape(&n))
		}
		out[name] = v
	}
	return out, nil
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// envValue is a value as the app receives it: text as written, or a whole
// number or boolean whose written form is the one it has always been
// delivered as (8080, true). Any other spelling (0x10, True, 1_000) would
// arrive differently from how it reads, so it is not a value.
func envValue(n *yaml.Node) (string, bool) {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	if n.Kind != yaml.ScalarNode {
		return "", false
	}
	switch n.ShortTag() {
	case "!!str":
		return n.Value, true
	case "!!int", "!!bool":
		var v any
		if err := n.Decode(&v); err != nil {
			return "", false
		}
		s := fmt.Sprint(v)
		return s, s == n.Value
	}
	return "", false
}

// envShape says what a refused value is.
func envShape(n *yaml.Node) string {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	switch {
	case n.Kind == yaml.MappingNode:
		return "a map is not a value"
	case n.Kind == yaml.SequenceNode:
		return "a list is not a value"
	case n.ShortTag() == "!!null":
		return "it has no value (write \"\" for an empty one)"
	case n.ShortTag() == "!!float":
		return fmt.Sprintf("%s reads as a number, which would not reach the app as written", n.Value)
	case n.ShortTag() == "!!timestamp":
		return fmt.Sprintf("%s reads as a date, which would not reach the app as written", n.Value)
	}
	return fmt.Sprintf("%s would not reach the app as written", n.Value)
}
