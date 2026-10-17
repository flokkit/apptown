//go:build ignore

// sqlnow-check refuses SQL that reads the database's clock. The time rule
// (design/architecture/layout.md): the forge writes every stored time from
// its owner's clock and passes it to SQL, so both sides of any comparison
// are the forge's and a test can move them. The database's clock is read
// nowhere: not in a query, not as a column's DEFAULT, not in a trigger,
// function or view. Any read fails the check; there is no count to hold.
//
// A clock read is any of, in any case: now(), transaction_timestamp(),
// statement_timestamp(), clock_timestamp(), timeofday(); the keywords
// current_timestamp, current_date, current_time, localtimestamp and
// localtime; and a special time value ('now', 'today', 'tomorrow',
// 'yesterday', alone or with a time of day: 'tomorrow 08:00') given a time
// type by any of PostgreSQL's spellings ('now'::timestamptz,
// CAST('now' AS date), timestamp with time zone 'now', timestamptz('now'))
// anywhere in its casts: once read as a time it has read the clock, and a
// later cast to text (timestamptz 'now'::text) does not undo that. An
// uncast 'now' (exactly a special value) counts too: PostgreSQL gives an
// untyped literal its context's type, a column's or a comparison's, which
// this check cannot see. Only a value that is text throughout
// ('now'::text, text 'now') is not a read.
//
// The SQL is split by PostgreSQL's lexical rules for what can hide or carry
// a read: -- and nested /* */ comments; string constants (a doubled quote inside),
// E'…' escape strings (backslash escapes), U&'…', B'…', X'…', N'…'; quoted
// identifiers; and dollar quotes ($$…$$, $tag$…$tag$), whose body is both a
// string constant and code, read again as SQL.
//
// It catches clock reads written in this module's SQL in the ordinary ways,
// as honest drift; it is not a defence against deliberate evasion. Out of
// scope, and review's job: SQL assembled in Go at run time (concatenated
// strings, fmt.Sprintf, a builder), a clock read in another module or an
// extension's function, a function or view defined outside the scanned
// files, and SQL read with standard_conforming_strings off (backslashes in
// plain '…' strings are taken literally, as PostgreSQL does by default).
//
// What is scanned, the module's files as the clock rule sees them:
//   - every .sql file but a generated one (the assembled
//     dbschema/schema.sql repeats its owners' fragments, which are scanned);
//   - every string literal in a Go file but a generated one (sqlc's .sql.go
//     repeats its queries.sql, which is scanned). A Go literal is not known
//     to be SQL, so any string that reads the clock as SQL would counts; one
//     that is prose and happens to match is reworded.
//
// Tests are not held to it, as the clock rule does not hold them: _test.go
// files and the test helper packages (a directory ending in test or fake),
// and testdata. SQL comments (-- and /* */) and other string literals in SQL
// are not read as SQL; a dollar-quoted body is, since it is code.
//
// Frozen migrations are history and are not counted. A database upgrades
// through the numbered migrations in the baseline's frozen file
// (internal/forge/dbschema/schema.go, its var migrations) once; a migration
// already applied never runs again, so rewriting it changes nothing that
// runs. The baseline records the number they are frozen through and a
// digest of what they say: a migration numbered at or below it is exempt,
// and the check fails if any of them changed, if the number names a
// migration that does not exist, or if the file declares no migrations (it
// would then exempt nothing it could see; an empty list, after a baseline
// collapse, freezes nothing). A migration above it is new and
// counted like any other SQL. A fresh database is made from the current
// schema, which is counted in full.
//
// Usage: go run scripts/sqlnow-check.go [-root dir] [-baseline file] [-v] [-freeze n]
//
// The baseline file holds only the frozen migrations; a count of reads in
// it is refused.
//
// -freeze n records migrations up to n as frozen, with their digest. It is
// for after they have been applied in production, in a commit of its own;
// the rest of the check runs as usual.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// read is one place SQL reads the database's clock.
type read struct{ pos, what string }

// frozen is the migrations already applied: those in File numbered at or
// below Through, which say what Digest records.
type frozen struct {
	File    string `json:"file"`
	Through int    `json:"through"`
	Digest  string `json:"digest"`
}

type baseline struct {
	Frozen frozen `json:"frozen"`
	// Packages is refused when it holds a count: the rule admits none.
	Packages map[string]int `json:"packages,omitempty"`
}

// exempt is what the clock rule exempts (.golangci.yml): tests and the
// helper packages only tests import.
var exempt = regexp.MustCompile(`(_test\.go$|(test|fake)/[^/]+\.(go|sql)$)`)

// self is this checker, whose strings name what it looks for.
const self = "scripts/sqlnow-check.go"

var generatedSQL = regexp.MustCompile(`(?m)^-- Code generated .* DO NOT EDIT\.$`)

func main() {
	root := flag.String("root", ".", "repository root")
	path := flag.String("baseline", "", "baseline file (default <root>/scripts/sqlnow-baseline.json)")
	flag.Bool("v", false, "list every read (each is listed anyway; kept for older callers)")
	freeze := flag.Int("freeze", 0, "record the migrations up to this number as frozen")
	flag.Parse()
	if *path == "" {
		*path = filepath.Join(*root, "scripts", "sqlnow-baseline.json")
	}
	data, err := os.ReadFile(*path)
	if err != nil {
		fail(err)
	}
	var b baseline
	if err := json.Unmarshal(data, &b); err != nil {
		fail(fmt.Errorf("%s: %w", *path, err))
	}
	if b.Frozen.File == "" {
		fail(fmt.Errorf("%s names no file of migrations", *path))
	}
	if len(b.Packages) > 0 {
		fail(fmt.Errorf("%s: the time rule admits no counts of reads; remove its packages", *path))
	}
	if *freeze > 0 {
		b.Frozen.Through = *freeze
	}
	ms, err := migrations(*root, b.Frozen.File)
	if err != nil {
		fail(err)
	}
	if *freeze > 0 {
		b.Frozen.Digest = ms.digest(b.Frozen.Through)
	}
	ok := ms.verify(b.Frozen)
	found, err := scan(*root, b.Frozen, ms)
	if err != nil {
		fail(err)
	}
	if !compare(found) || !ok {
		os.Exit(1)
	}
	if *freeze > 0 {
		out, err := json.MarshalIndent(b, "", "  ")
		if err != nil {
			fail(err)
		}
		if err := os.WriteFile(*path, append(out, '\n'), 0o644); err != nil {
			fail(err)
		}
		fmt.Println("sqlnow baseline written:", *path)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "sqlnow-check:", err)
	os.Exit(2)
}

// migration is one element of the migrations list: its number, where it
// lies in the file, and the strings it says.
type migration struct {
	n          int
	pos, end   token.Pos
	strings    []string
	numberLine int
}

type migrationList []migration

// migrations reads the frozen file's var migrations: a list of composite
// literals, each with a literal number n.
func migrations(root, rel string) (migrationList, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var list migrationList
	declared := false
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if name.Name != "migrations" || i >= len(vs.Values) {
					continue
				}
				declared = true
				lit, ok := vs.Values[i].(*ast.CompositeLit)
				if !ok {
					continue
				}
				for _, elt := range lit.Elts {
					m, err := readMigration(fset, rel, elt)
					if err != nil {
						return nil, err
					}
					list = append(list, m)
				}
			}
		}
	}
	if !declared {
		return nil, fmt.Errorf("%s declares no var migrations: name the file that does in the baseline", rel)
	}
	return list, nil
}

func readMigration(fset *token.FileSet, rel string, elt ast.Expr) (migration, error) {
	m := migration{n: -1, pos: elt.Pos(), end: elt.End(), numberLine: fset.Position(elt.Pos()).Line}
	lit, ok := elt.(*ast.CompositeLit)
	if !ok {
		return m, fmt.Errorf("%s:%d: a migration that is not a literal", rel, m.numberLine)
	}
	for _, field := range lit.Elts {
		if kv, ok := field.(*ast.KeyValueExpr); ok {
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "n" {
				if num, ok := kv.Value.(*ast.BasicLit); ok && num.Kind == token.INT {
					m.n, _ = strconv.Atoi(num.Value)
				}
			}
		}
	}
	if m.n < 0 {
		return m, fmt.Errorf("%s:%d: a migration without a literal number n", rel, m.numberLine)
	}
	ast.Inspect(lit, func(n ast.Node) bool {
		if s, ok := n.(*ast.BasicLit); ok && s.Kind == token.STRING {
			if v, err := strconv.Unquote(s.Value); err == nil {
				m.strings = append(m.strings, v)
			}
		}
		return true
	})
	return m, nil
}

// digest is what the migrations up to through say: their numbers and
// strings, in order.
func (ms migrationList) digest(through int) string {
	h := sha256.New()
	for _, m := range ms {
		if m.n > through {
			continue
		}
		fmt.Fprintf(h, "%d\x00", m.n)
		for _, s := range m.strings {
			fmt.Fprintf(h, "%d:%s\x00", len(s), s)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// verify reports whether the frozen migrations exist and are as recorded.
func (ms migrationList) verify(f frozen) bool {
	latest := 0
	for _, m := range ms {
		latest = max(latest, m.n)
	}
	ok := true
	if f.Through > latest {
		fmt.Printf("sqlnow frozen through %d, but %s's last migration is %d: freeze only migrations that exist\n", f.Through, f.File, latest)
		ok = false
	}
	if ms.digest(f.Through) != f.Digest {
		fmt.Printf("sqlnow a frozen migration in %s (numbered %d or below) changed: one already applied is history, so add a new migration instead\n", f.File, f.Through)
		ok = false
	}
	return ok
}

// frozenAt reports whether pos lies in a frozen migration.
func (ms migrationList) frozenAt(pos token.Pos, through int) bool {
	for _, m := range ms {
		if m.n <= through && pos >= m.pos && pos < m.end {
			return true
		}
	}
	return false
}

// scan returns every clock read under root by package directory. Like the
// go tool it skips hidden and underscore directories, testdata, vendor and
// node_modules. A Go file that does not parse fails the scan.
func scan(root string, f frozen, ms migrationList) (map[string][]read, error) {
	out := map[string][]read{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if exempt.MatchString(rel) || rel == self {
			return nil
		}
		var reads []read
		switch {
		case strings.HasSuffix(path, ".sql"):
			reads, err = sqlFile(path, rel)
		case strings.HasSuffix(path, ".go"):
			skip := func(token.Pos) bool { return false }
			if rel == f.File {
				skip = func(p token.Pos) bool { return ms.frozenAt(p, f.Through) }
			}
			reads, err = goFile(path, rel, skip)
		}
		if err != nil {
			return err
		}
		if len(reads) > 0 {
			pkg := filepath.ToSlash(filepath.Dir(rel))
			out[pkg] = append(out[pkg], reads...)
		}
		return nil
	})
	return out, err
}

func sqlFile(path, rel string) ([]read, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if generatedSQL.Match(src) {
		return nil, nil
	}
	var out []read
	for _, c := range clockReads(string(src)) {
		out = append(out, read{pos: fmt.Sprintf("%s:%d", rel, 1+c.line), what: c.what})
	}
	return out, nil
}

// goFile finds the clock reads in a Go file's string literals, but those
// skip names (a frozen migration's). Positions are the file's own, as the
// frozen file is parsed the same way in migrations.
func goFile(path, rel string, skip func(token.Pos) bool) ([]read, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	if ast.IsGenerated(file) {
		return nil, nil
	}
	var out []read
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || skip(lit.Pos()) {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		line := fset.Position(lit.Pos()).Line
		for _, c := range clockReads(value) {
			out = append(out, read{pos: fmt.Sprintf("%s:%d", rel, line+c.line), what: c.what})
		}
		return true
	})
	return out, nil
}

// sqlToken is one SQL token: a word (lower-cased; a quoted identifier keeps
// its quotes), a punctuation character, or a string constant (text is its
// value); line counts from 0.
type sqlToken struct {
	kind byte // 'w' word, 'p' punctuation, 's' string
	text string
	line int
}

// clockRead is one clock read in a piece of SQL; line counts from 0.
type clockRead struct {
	line int
	what string
}

// clockReads finds the clock reads in SQL. A dollar-quoted body is both a
// string constant (it may be a value cast to a time) and code (a function's
// body), so it is read again as SQL of its own.
func clockReads(src string) []clockRead {
	toks, bodies := lex(src)
	out := tokenReads(toks)
	for _, b := range bodies {
		for _, r := range clockReads(b.text) {
			out = append(out, clockRead{b.line + r.line, r.what})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].line < out[j].line })
	return out
}

// specialTimes are the input values PostgreSQL reads as the current time or
// a day relative to it: alone ('now'), or a day with a time of day
// ('tomorrow 08:00').
var specialTimes = map[string]bool{"now": true, "today": true, "tomorrow": true, "yesterday": true}

// timeTypes are the types a special value is read as a time in.
var timeTypes = map[string]bool{"timestamp": true, "timestamptz": true, "date": true, "time": true, "timetz": true, "abstime": true, "smalldatetime": true}

func tokenReads(toks []sqlToken) []clockRead {
	var out []clockRead
	punct := func(i int, s string) bool {
		return i >= 0 && i < len(toks) && toks[i].kind == 'p' && toks[i].text == s
	}
	for i, t := range toks {
		what := ""
		switch t.kind {
		case 'w':
			switch t.text {
			case "now", "transaction_timestamp", "statement_timestamp", "clock_timestamp", "timeofday":
				// Each takes no argument; "now (" is prose.
				if punct(i+1, "(") && punct(i+2, ")") {
					what = t.text + "()"
				}
			case "current_timestamp", "current_date", "current_time", "localtimestamp", "localtime":
				if !punct(i-1, ".") {
					what = t.text
				}
			}
		case 's':
			if special(t.text) {
				types := castTypes(toks, i)
				timed := false
				for _, ty := range types {
					timed = timed || timeTypes[ty]
				}
				switch {
				case timed:
					// Read as a time once given a time type; a later
					// cast to text does not undo it.
					what = "'" + t.text + "'"
				case len(types) == 0 && specialTimes[strings.ToLower(strings.TrimSpace(t.text))]:
					// Uncast, PostgreSQL gives the value its context's type
					// (a column's, a comparison's): only the exact value is
					// taken for the clock; a sentence is not.
					what = "'" + t.text + "'"
				}
			}
		}
		if what != "" {
			out = append(out, clockRead{t.line, what})
		}
	}
	return out
}

// special reports whether a string's value names the current time or a
// day relative to it, in any case and with surrounding space:
// 'now', ' Today ', 'tomorrow 08:00'.
func special(v string) bool {
	f := strings.Fields(strings.ToLower(v))
	return len(f) > 0 && specialTimes[f[0]]
}

// castTypes are the types the string at toks[i] is given, in the order
// PostgreSQL applies them, by any of its spellings: a typed literal
// (timestamptz 'now', timestamp(3) with time zone 'now', pg_catalog.date
// 'today') or a type's function form (timestamptz('now')) first, then each
// :: cast ('now'::date, 'now'::timestamp(0) without time zone) and each
// CAST(… AS type) around it, in turn, through grouping parentheses. None
// is an uncast value.
func castTypes(toks []sqlToken, i int) []string {
	tok := func(j int) sqlToken {
		if j < 0 || j >= len(toks) {
			return sqlToken{}
		}
		return toks[j]
	}
	isP := func(j int, s string) bool { t := tok(j); return t.kind == 'p' && t.text == s }
	isW := func(j int, s string) bool { t := tok(j); return t.kind == 'w' && t.text == s }
	var types []string
	// The expression so far is toks[start:end].
	start, end := i, i+1
	// Typed literal: type '…', where type may carry a precision and a
	// "with[out] time zone" after its name, and a schema before it.
	j := i - 1
	if isW(j, "zone") && isW(j-1, "time") && (isW(j-2, "with") || isW(j-2, "without")) {
		j -= 3
	}
	if isP(j, ")") {
		for j >= 0 && !isP(j, "(") {
			j--
		}
		j--
	}
	if t := tok(j); t.kind == 'w' && (timeTypes[t.text] || otherTypes[t.text]) {
		types = append(types, t.text)
		for isP(j-1, ".") && tok(j-2).kind == 'w' {
			j -= 2
		}
		start = j
	}
	for {
		switch {
		case isP(end, ":") && isP(end+1, ":"):
			name, next := typeAt(toks, end+2)
			if name == "" {
				return types
			}
			types = append(types, name)
			end = next
		case isW(end, "as") && isP(start-1, "(") && isW(start-2, "cast"):
			name, next := typeAt(toks, end+1)
			if name == "" || !isP(next, ")") {
				return types
			}
			types = append(types, name)
			start, end = start-2, next+1
		case isP(start-1, "(") && isP(end, ")") && tok(start-2).kind == 'w' && timeTypes[tok(start-2).text]:
			// Function form: type('…').
			types = append(types, tok(start-2).text)
			start, end = start-2, end+1
		case isP(start-1, "(") && isP(end, ")"):
			// Grouping: ('now')::timestamptz is 'now' cast.
			start, end = start-1, end+1
		default:
			return types
		}
	}
}

// otherTypes are types a special value is not read as a time in, when it
// is given one as a typed literal (text 'now').
var otherTypes = map[string]bool{"text": true, "varchar": true, "char": true, "character": true, "varying": true, "bpchar": true, "name": true, "citext": true, "json": true, "jsonb": true}

// typeAt is the base name of the type written at toks[j] (its last name
// when qualified: pg_catalog.timestamptz), and where the type ends, past a
// second word (character varying), a precision and "with[out] time zone".
// The name is "" when no type is there.
func typeAt(toks []sqlToken, j int) (string, int) {
	isP := func(k int, s string) bool { return k < len(toks) && toks[k].kind == 'p' && toks[k].text == s }
	isW := func(k int, s string) bool { return k < len(toks) && toks[k].kind == 'w' && toks[k].text == s }
	if j >= len(toks) || toks[j].kind != 'w' {
		return "", j
	}
	name := toks[j].text
	for j+2 < len(toks) && isP(j+1, ".") && toks[j+2].kind == 'w' {
		j += 2
		name = toks[j].text
	}
	j++
	if (name == "character" || name == "double") && (isW(j, "varying") || isW(j, "precision")) {
		j++
	}
	if isP(j, "(") {
		for j < len(toks) && !isP(j, ")") {
			j++
		}
		j++
	}
	if (isW(j, "with") || isW(j, "without")) && isW(j+1, "time") && isW(j+2, "zone") {
		j += 3
	}
	return strings.Trim(name, "\""), j
}

// body is a dollar-quoted body, read again as SQL; line is where it begins.
type body struct {
	text string
	line int
}

// lex splits SQL into tokens by PostgreSQL's lexical rules for what can
// hide or carry a clock read: -- and nested /* */ comments; string
// constants (a doubled quote inside), E'…' escape strings (a backslash escapes the
// next character), U&'…', B'…', X'…' and N'…'; quoted identifiers ("…",
// "" doubling); and dollar quotes, $$…$$ or $tag$…$tag$, closed only by
// the same delimiter, whose body is a string token and is also returned to
// be read as code. $1 is a parameter, and $ inside a word is part of it.
func lex(src string) ([]sqlToken, []body) {
	var toks []sqlToken
	var bodies []body
	line := 0
	isStart := func(c byte) bool {
		return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
	}
	isWord := func(c byte) bool { return isStart(c) || c >= '0' && c <= '9' || c == '$' }
	// quoted reads a quoted run from src[i] (the opening quote q); with
	// escapes, a backslash takes the next character as it is.
	quoted := func(i int, q byte, escapes bool) (string, int) {
		var b strings.Builder
		for i++; i < len(src); i++ {
			c := src[i]
			if c == '\n' {
				line++
			}
			if escapes && c == '\\' && i+1 < len(src) {
				i++
				if src[i] == '\n' {
					line++
				}
				b.WriteByte(src[i])
				continue
			}
			if c == q {
				if i+1 < len(src) && src[i+1] == q {
					b.WriteByte(q)
					i++
					continue
				}
				return b.String(), i + 1
			}
			b.WriteByte(c)
		}
		return b.String(), len(src)
	}
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f':
			i++
		case c == '-' && i+1 < len(src) && src[i+1] == '-':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			// Block comments nest in PostgreSQL.
			depth := 0
			for i < len(src) {
				if strings.HasPrefix(src[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(src[i:], "*/") {
					depth--
					i += 2
					if depth == 0 {
						break
					}
				} else {
					if src[i] == '\n' {
						line++
					}
					i++
				}
			}
		case c == '\'':
			at := line
			v, next := quoted(i, '\'', false)
			toks = append(toks, sqlToken{kind: 's', text: v, line: at})
			i = next
		case c == '"':
			at := line
			v, next := quoted(i, '"', false)
			toks = append(toks, sqlToken{kind: 'w', text: "\"" + v + "\"", line: at})
			i = next
		case (c == 'e' || c == 'E') && i+1 < len(src) && src[i+1] == '\'':
			at := line
			v, next := quoted(i+1, '\'', true)
			toks = append(toks, sqlToken{kind: 's', text: v, line: at})
			i = next
		case (c == 'b' || c == 'B' || c == 'x' || c == 'X' || c == 'n' || c == 'N') && i+1 < len(src) && src[i+1] == '\'':
			at := line
			v, next := quoted(i+1, '\'', false)
			toks = append(toks, sqlToken{kind: 's', text: v, line: at})
			i = next
		case (c == 'u' || c == 'U') && strings.HasPrefix(src[i+1:], "&'"):
			at := line
			v, next := quoted(i+2, '\'', false)
			toks = append(toks, sqlToken{kind: 's', text: v, line: at})
			i = next
		case (c == 'u' || c == 'U') && strings.HasPrefix(src[i+1:], "&\""):
			at := line
			v, next := quoted(i+2, '"', false)
			toks = append(toks, sqlToken{kind: 'w', text: "\"" + v + "\"", line: at})
			i = next
		case c == '$':
			// A dollar quote: $$ or $tag$, the tag an identifier without $
			// (so $1, a parameter, is none).
			j := i + 1
			if j < len(src) && isStart(src[j]) {
				for j < len(src) && isWord(src[j]) && src[j] != '$' {
					j++
				}
			}
			if j >= len(src) || src[j] != '$' {
				toks = append(toks, sqlToken{kind: 'p', text: "$", line: line})
				i++
				continue
			}
			delim := src[i : j+1]
			start, at := j+1, line
			end := strings.Index(src[start:], delim)
			if end < 0 {
				end = len(src) - start
			}
			v := src[start : start+end]
			toks = append(toks, sqlToken{kind: 's', text: v, line: at})
			bodies = append(bodies, body{text: v, line: at})
			line += strings.Count(v, "\n")
			i = min(start+end+len(delim), len(src))
		case isStart(c):
			j := i
			for j < len(src) && isWord(src[j]) {
				j++
			}
			toks = append(toks, sqlToken{kind: 'w', text: strings.ToLower(src[i:j]), line: line})
			i = j
		default:
			toks = append(toks, sqlToken{kind: 'p', text: string(c), line: line})
			i++
		}
	}
	return toks, bodies
}

// compare fails on any read, naming each.
func compare(found map[string][]read) bool {
	ordered := make([]string, 0, len(found))
	for p := range found {
		ordered = append(ordered, p)
	}
	sort.Strings(ordered)
	total := 0
	for _, p := range ordered {
		total += len(found[p])
		fmt.Printf("sqlnow %-48s %4d  READS THE DATABASE'S CLOCK: write the time from the owner's clock and pass it to SQL\n", p, len(found[p]))
		list(found[p])
	}
	fmt.Printf("sqlnow %4d reads\n", total)
	return total == 0
}

func list(reads []read) {
	for _, r := range reads {
		fmt.Printf("    %s: %s\n", r.pos, r.what)
	}
}
