package appcontract

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Need-value wiring (contract version 2): an environment value may name a
// value a need gives the app, ${needs.<need>.<VAR>}, for an image that
// expects its own variable names (WORDPRESS_DB_HOST: ${needs.mariadb.DB_HOST}).
// VAR is one of the variables the need documents (its catalogue entry's
// provides); the need's fixed variables are still given as well.
// $${needs. writes a literal ${needs.; nothing else in a value is special, so
// any other text, a $${ elsewhere included, is delivered as written. A
// version-1 contract's values are delivered as written.

// needTokenRe finds the three things version 2 reads in an environment
// value, leftmost first: the escape $${needs. (the literal text ${needs.),
// a reference ${needs.<need>.<VAR>}, and any other ${needs. (malformed).
var needTokenRe = regexp.MustCompile(`\$\$\{needs\.|\$\{needs\.([a-z][a-z0-9-]*)\.([A-Z][A-Z0-9_]*)\}|\$\{needs\.`)

type needToken struct {
	start, end int
	escape     bool   // $${needs.
	need, name string // a reference; both empty on a malformed one
}

func needTokens(v string) []needToken {
	var out []needToken
	for _, m := range needTokenRe.FindAllStringSubmatchIndex(v, -1) {
		t := needToken{start: m[0], end: m[1]}
		switch {
		case strings.HasPrefix(v[m[0]:], "$${"):
			t.escape = true
		case m[2] >= 0:
			t.need, t.name = v[m[2]:m[3]], v[m[4]:m[5]]
		}
		out = append(out, t)
	}
	return out
}

// rewriteNeedTokens builds v with each escape written as its literal and
// each reference as ref(need, name) says.
func rewriteNeedTokens(v string, ref func(need, name string) string) string {
	var b strings.Builder
	last := 0
	for _, t := range needTokens(v) {
		b.WriteString(v[last:t.start])
		switch {
		case t.escape:
			b.WriteString("${needs.")
		case t.need != "":
			b.WriteString(ref(t.need, t.name))
		default:
			b.WriteString(v[t.start:t.end])
		}
		last = t.end
	}
	b.WriteString(v[last:])
	return b.String()
}

// NeedRef is one wiring reference: Part's environment Key names Need's Var.
// Part is a part's name, "init-<name>" for an init step, or
// "<profile>/<part>" for a profile's environment.
type NeedRef struct {
	Part, Key, Need, Var string
}

// NeedRefs lists the contract's wiring references in a stable order: none
// for a version-1 contract.
func (c *Contract) NeedRefs() []NeedRef {
	if c == nil || c.Version < 2 {
		return nil
	}
	var out []NeedRef
	add := func(owner string, env map[string]string) {
		for _, k := range sortedKeys(env) {
			for _, t := range needTokens(env[k]) {
				if t.need != "" {
					out = append(out, NeedRef{Part: owner, Key: k, Need: t.need, Var: t.name})
				}
			}
		}
	}
	for _, p := range c.Parts {
		add(p.Name, p.Env)
	}
	for _, in := range c.Init {
		add("init-"+in.Name, in.Env)
	}
	for _, name := range c.ProfileNames() {
		for _, part := range sortedKeys(c.Profiles[name]) {
			add(name+"/"+part, c.Profiles[name][part].Env)
		}
	}
	return out
}

// checkNeedRefs refuses, at parse, a reference that is malformed or names a
// need the contract does not declare; whether the need gives that variable is
// the catalogue's to say (the release preflight).
func checkNeedRefs(c *Contract) error {
	if c.Version < 2 {
		return nil
	}
	declared := map[string]bool{}
	for _, n := range c.Needs {
		declared[n] = true
	}
	check := func(owner string, env map[string]string) error {
		for _, k := range sortedKeys(env) {
			v := env[k]
			for _, t := range needTokens(v) {
				switch {
				case t.escape:
				case t.need == "":
					return fmt.Errorf("%s: environment: %s: %q starts a need reference that is not one; write ${needs.<need>.<VARIABLE>}, such as ${needs.postgres.PGHOST}, or $${needs. for the literal text", owner, k, v[t.start:])
				case !declared[t.need]:
					return fmt.Errorf("%s: environment: %s: %s names need %s, which this app does not declare under needs:", owner, k, v[t.start:t.end], t.need)
				}
			}
		}
		return nil
	}
	for _, p := range c.Parts {
		if err := check("app part "+p.Name, p.Env); err != nil {
			return err
		}
	}
	for _, in := range c.Init {
		if err := check("init step "+in.Name, in.Env); err != nil {
			return err
		}
	}
	for _, name := range c.ProfileNames() {
		for _, part := range sortedKeys(c.Profiles[name]) {
			if err := check("profiles: "+name+": "+part, c.Profiles[name][part].Env); err != nil {
				return err
			}
		}
	}
	return nil
}

// WireEnv is env with each need reference replaced by the value the need
// gives (values: the need variables the target has resolved, by name) and
// each $${needs. written as ${needs.. Version-1 environments are returned unchanged. A
// reference to a variable the target did not give is an error naming it:
// every target gives every need's documented variables.
func WireEnv(version int, owner string, env, values map[string]string) (map[string]string, error) {
	if version < 2 || len(env) == 0 {
		return env, nil
	}
	out := make(map[string]string, len(env))
	for _, k := range sortedKeys(env) {
		var missing []string
		v := rewriteNeedTokens(env[k], func(need, name string) string {
			got, ok := values[name]
			if !ok {
				missing = append(missing, "${needs."+need+"."+name+"}")
			}
			return got
		})
		if len(missing) > 0 {
			sort.Strings(missing)
			return nil, fmt.Errorf("%s: environment: %s: %s: the need gives no such value here", owner, k, strings.Join(missing, ", "))
		}
		out[k] = v
	}
	return out, nil
}

// WiresFrom says which of values' keys env's references read, so a target
// can deliver a wired value with the care its source needs (a credential
// stays a secret).
func WiresFrom(version int, env map[string]string) map[string][]string {
	if version < 2 {
		return nil
	}
	out := map[string][]string{}
	for _, k := range sortedKeys(env) {
		for _, t := range needTokens(env[k]) {
			if t.need != "" {
				out[k] = append(out[k], t.name)
			}
		}
	}
	return out
}

// WithoutNeedRefs is v with its need references removed and each escaped
// one written as the literal it delivers: what is left is what the app wrote literally, for a target that
// must check literal text (App Platform rewrites any ${…} it delivers).
func WithoutNeedRefs(version int, v string) string {
	if version < 2 {
		return v
	}
	return rewriteNeedTokens(v, func(string, string) string { return "" })
}
