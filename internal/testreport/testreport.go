// Package testreport reads the machine-readable report a test runner
// writes: how many tests passed, failed and were skipped, and which were
// skipped. Two formats are read, both maintained by their tools rather
// than readings of human output: JUnit XML, the interchange format most
// runners write, and Go's test2json stream (go test -json), since Go writes
// no JUnit without an extra tool. The format is recognised from the
// content, never from the project's language.
package testreport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
)

// The formats, as the evidence names them.
const (
	GoJSON = "go test -json"
	JUnit  = "JUnit XML"
)

// Counts are the tests a report says passed, failed and were skipped.
type Counts struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// Report is what a report says.
type Report struct {
	Format string
	Counts Counts
	// Skipped names the skipped tests in report order: "package TestName"
	// for Go, "classname.name" for JUnit.
	Skipped []string
	// Log is the readable output of a Go stream: what plain go test prints
	// (build output, each package's result line) and the whole output of
	// every test that did not pass, which carries a skip's reason. Empty
	// for JUnit, whose runner prints its own.
	Log string
}

// ErrEmpty is a report with nothing in it: no tests are known, which is
// not zero tests.
var ErrEmpty = errors.New("the report is empty")

// Read reads a report in either format.
func Read(b []byte) (Report, error) {
	t := bytes.TrimSpace(b)
	switch {
	case len(t) == 0:
		return Report{}, ErrEmpty
	case t[0] == '<':
		return readJUnit(t)
	case t[0] == '{':
		return readGo(t)
	}
	return Report{}, errors.New("the report is neither JUnit XML nor a go test -json stream")
}

// event is one test2json line (cmd/test2json). ImportPath names a
// build-output event's package, as "pkg [pkg.test]".
type event struct {
	Action     string
	Package    string
	Test       string
	Output     string
	ImportPath string
}

func readGo(b []byte) (Report, error) {
	var events []event
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e event
		if err := json.Unmarshal(line, &e); err != nil || e.Action == "" {
			return Report{}, fmt.Errorf("line %d of the report is not a go test -json event", n)
		}
		if e.Package == "" && e.ImportPath != "" {
			e.Package, _, _ = strings.Cut(e.ImportPath, " ")
		}
		events = append(events, e)
	}
	if err := sc.Err(); err != nil {
		return Report{}, fmt.Errorf("reading the report: %w", err)
	}
	r := Report{Format: GoJSON}
	// A test's last action is its result. A package-level skip is a
	// package with no test files, not a skipped test.
	final := map[[2]string]string{}
	for _, e := range events {
		if e.Test == "" {
			continue
		}
		switch e.Action {
		case "pass", "fail", "skip":
			final[[2]string{e.Package, e.Test}] = e.Action
			switch e.Action {
			case "pass":
				r.Counts.Passed++
			case "fail":
				r.Counts.Failed++
			case "skip":
				r.Counts.Skipped++
				r.Skipped = append(r.Skipped, e.Package+" "+e.Test)
			}
		}
	}
	// The log, per package in the order packages first appear: a passing
	// test's lines are left out, as plain go test leaves them out; a test
	// with no result (a panic, a timeout) keeps its lines.
	var order []string
	logs := map[string]*strings.Builder{}
	for _, e := range events {
		if e.Output == "" || (e.Action != "output" && e.Action != "build-output") {
			continue
		}
		if e.Test != "" && final[[2]string{e.Package, e.Test}] == "pass" {
			continue
		}
		w := logs[e.Package]
		if w == nil {
			w = &strings.Builder{}
			logs[e.Package] = w
			order = append(order, e.Package)
		}
		w.WriteString(e.Output)
	}
	var log strings.Builder
	for _, p := range order {
		log.WriteString(logs[p].String())
	}
	r.Log = log.String()
	return r, nil
}

// suite is a JUnit <testsuites> or <testsuite>: either may hold suites
// and cases.
type suite struct {
	XMLName xml.Name
	Suites  []suite    `xml:"testsuite"`
	Cases   []testcase `xml:"testcase"`
}

type testcase struct {
	Name      string     `xml:"name,attr"`
	ClassName string     `xml:"classname,attr"`
	Skipped   *struct{}  `xml:"skipped"`
	Failures  []struct{} `xml:"failure"`
	Errors    []struct{} `xml:"error"`
}

func readJUnit(b []byte) (Report, error) {
	var root suite
	if err := xml.Unmarshal(b, &root); err != nil {
		return Report{}, fmt.Errorf("the report is not readable XML: %w", err)
	}
	if n := root.XMLName.Local; n != "testsuites" && n != "testsuite" {
		return Report{}, fmt.Errorf("the report's root is <%s>, not JUnit's <testsuites> or <testsuite>", n)
	}
	r := Report{Format: JUnit}
	var walk func(s suite)
	walk = func(s suite) {
		for _, c := range s.Cases {
			switch {
			case len(c.Failures) > 0 || len(c.Errors) > 0:
				r.Counts.Failed++
			case c.Skipped != nil:
				r.Counts.Skipped++
				name := c.Name
				if c.ClassName != "" {
					name = c.ClassName + "." + c.Name
				}
				r.Skipped = append(r.Skipped, name)
			default:
				r.Counts.Passed++
			}
		}
		for _, inner := range s.Suites {
			walk(inner)
		}
	}
	walk(root)
	return r, nil
}
