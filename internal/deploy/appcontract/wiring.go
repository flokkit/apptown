package appcontract

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
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

var v1MemoryTokenRe = regexp.MustCompile(`^(["']?)(\d+)[ \t]*([MG])(["']?)`)
var versionLineRe = regexp.MustCompile(`(?m)^version:[ \t]*1[ \t]*$`)

// ConvertToV2 rewrites a version-1 contract's text so it means exactly the
// same as version 2: each resource memory in M or G becomes Mi or Gi, and
// each ${needs. in an environment value (the one sequence version 2 reads)
// is escaped, so every value is still delivered as written. Only those
// fields are edited, at the positions the YAML parser gives; comments,
// layout and every other value are kept. A version-2 file, or one that does
// not parse, is returned unchanged. A value it cannot edit in place (a
// multi-line string holding ${needs.) is an error, for a person.
func ConvertToV2(text string) (string, bool, error) {
	c, err := Parse([]byte(text))
	if err != nil || c.Version >= 2 {
		// A contract that does not parse is not this conversion's to fix: it
		// stays as written (version 1), and its release says what is wrong.
		return text, false, nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(text), &root); err != nil || len(root.Content) == 0 {
		return text, false, nil
	}
	type edit struct {
		line, col int
		memory    bool
		count     int // ${needs. occurrences to escape
	}
	var edits []edit
	get := func(m *yaml.Node, key string) *yaml.Node {
		if m == nil || m.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == key {
				return m.Content[i+1]
			}
		}
		return nil
	}
	values := func(m *yaml.Node) []*yaml.Node {
		if m == nil || m.Kind != yaml.MappingNode {
			return nil
		}
		var out []*yaml.Node
		for i := 1; i < len(m.Content); i += 2 {
			out = append(out, m.Content[i])
		}
		return out
	}
	memory := func(owner *yaml.Node) {
		if n := get(get(owner, "resources"), "memory"); n != nil && n.Kind == yaml.ScalarNode {
			edits = append(edits, edit{line: n.Line, col: n.Column, memory: true})
		}
	}
	var blocked string
	environment := func(owner *yaml.Node) {
		for _, n := range values(get(owner, "environment")) {
			if k := strings.Count(n.Value, "${needs."); k > 0 && n.Kind == yaml.ScalarNode {
				if n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
					blocked = fmt.Sprintf("line %d: a multi-line value holds ${needs.; write it as $${needs. by hand", n.Line)
				}
				edits = append(edits, edit{line: n.Line, col: n.Column, count: k})
			}
		}
	}
	doc := root.Content[0]
	for _, part := range values(get(doc, "app")) {
		memory(part)
		environment(part)
	}
	if init := get(doc, "init"); init != nil && init.Kind == yaml.SequenceNode {
		for _, step := range init.Content {
			environment(step)
		}
	}
	for _, profile := range values(get(doc, "profiles")) {
		for _, part := range values(profile) {
			memory(part)
			environment(part)
		}
	}
	for _, need := range values(get(doc, "needs")) {
		memory(need)
	}
	// An anchor or alias can share one value between a field this edits and
	// one it must not (or the reverse), so an edit in place could change a
	// value nobody meant to change: such a contract is left for a person.
	if (len(edits) > 0 || strings.Contains(text, "${needs.")) && usesAnchors(&root) {
		blocked = "it uses YAML anchors or aliases (& or *); write the shared values out, then run the upgrade again"
	}
	if blocked != "" {
		return text, false, fmt.Errorf("the contract does not convert to version 2 by itself: %s", blocked)
	}
	lines := strings.Split(text, "\n")
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].line != edits[j].line {
			return edits[i].line > edits[j].line
		}
		return edits[i].col > edits[j].col
	})
	for _, e := range edits {
		line := lines[e.line-1]
		at := min(len(line), e.col-1)
		head, tail := line[:at], line[at:]
		if e.memory {
			if m := v1MemoryTokenRe.FindStringSubmatchIndex(tail); m != nil && (m[7] >= len(tail) || tail[m[7]] != 'i') {
				tail = tail[:m[7]] + "i" + tail[m[7]:]
			}
		} else {
			for i, from := 0, 0; i < e.count; i++ {
				j := strings.Index(tail[from:], "${needs.")
				if j < 0 {
					return text, false, fmt.Errorf("the contract does not convert to version 2 by itself: line %d: a value holding ${needs. spans lines; write it as $${needs. by hand", e.line)
				}
				tail = tail[:from+j] + "$" + tail[from+j:]
				from += j + len("$${needs.")
			}
		}
		lines[e.line-1] = head + tail
	}
	out := strings.Join(lines, "\n")
	if versionLineRe.MatchString(out) {
		out = versionLineRe.ReplaceAllString(out, "version: 2")
	} else {
		out = "version: 2\n" + out
	}
	after, err := Parse([]byte(out))
	if err != nil {
		return text, false, fmt.Errorf("the contract does not convert to version 2 by itself: %w", err)
	}
	// The text edits are positional; the proof is the meaning. Unless the
	// version-2 contract reads exactly as the version-1 one did (no need
	// references, every value delivered the same, the same limits), the
	// file is left for a person rather than committed as an upgrade.
	if !sameMeaning(c, after) {
		return text, false, fmt.Errorf("the contract does not convert to version 2 by itself: a value would change (a ${needs. or a memory written in a way the upgrade cannot edit); write memory as Mi or Gi and ${needs. as $${needs. by hand, with version: 2")
	}
	return out, true, nil
}

// sameMeaning says whether after, a version-2 contract, means what before,
// a version-1 one, meant: no need references, each environment value
// delivering what before's delivered, and everything else equal.
func sameMeaning(before, after *Contract) bool {
	if len(after.NeedRefs()) > 0 {
		return false
	}
	literal := func(env map[string]string) map[string]string {
		if env == nil {
			return nil
		}
		out := make(map[string]string, len(env))
		for k, v := range env {
			out[k] = WithoutNeedRefs(after.Version, v)
		}
		return out
	}
	b, a := *before, *after
	a.Version = b.Version
	a.Parts = slices.Clone(after.Parts)
	for i := range a.Parts {
		a.Parts[i].Env = literal(a.Parts[i].Env)
	}
	a.Init = slices.Clone(after.Init)
	for i := range a.Init {
		a.Init[i].Env = literal(a.Init[i].Env)
	}
	a.Profiles = map[string]map[string]ProfilePart{}
	for name, parts := range after.Profiles {
		a.Profiles[name] = map[string]ProfilePart{}
		for part, pp := range parts {
			pp.Env = literal(pp.Env)
			a.Profiles[name][part] = pp
		}
	}
	if after.Profiles == nil {
		a.Profiles = nil
	}
	return reflect.DeepEqual(&b, &a)
}

// usesAnchors says whether any node of n is an anchor or an alias.
func usesAnchors(n *yaml.Node) bool {
	if n.Anchor != "" || n.Kind == yaml.AliasNode {
		return true
	}
	for _, c := range n.Content {
		if usesAnchors(c) {
			return true
		}
	}
	return false
}
