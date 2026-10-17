package testreport

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// A recorded go test -json stream (go 1.24: a passing test, a skipped test,
// a skipped subtest in a passing parent, a failed test, a build failure
// and a package with no test files) is counted by its test-level results,
// and its log keeps what plain go test prints plus every test that did not
// pass, with the skip's reason, and leaves out the passing tests' lines.
func TestAGoStreamCountsItsTestsAndKeepsWhatDidNotPass(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/go-test.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Read(b)
	if err != nil {
		t.Fatal(err)
	}
	if r.Format != GoJSON || r.Counts != (Counts{Passed: 3, Failed: 1, Skipped: 2}) {
		t.Fatalf("read %s %+v, want go test -json, 3 passed, 1 failed, 2 skipped", r.Format, r.Counts)
	}
	if want := []string{"ex/a TestSkip", "ex/a TestSub/two"}; !slices.Equal(r.Skipped, want) {
		t.Fatalf("skipped %q, want %q", r.Skipped, want)
	}
	for _, kept := range []string{"cannot use \"x\"", "FAIL\tex/b [build failed]", "?   \tex/c\t[no test files]",
		"a_test.go:4: X not set", "--- SKIP: TestSub/two", "d_test.go:3: boom", "ok  \tex/a", "FAIL\tex/d"} {
		if !strings.Contains(r.Log, kept) {
			t.Errorf("the log lost %q:\n%s", kept, r.Log)
		}
	}
	for _, dropped := range []string{"--- PASS: TestRun", "=== RUN   TestSub/one"} {
		if strings.Contains(r.Log, dropped) {
			t.Errorf("the log keeps a passing test's %q:\n%s", dropped, r.Log)
		}
	}
	// Each package's lines stay together: a's skip reason comes before
	// a's result, and d's failure after a's.
	if i, j := strings.Index(r.Log, "X not set"), strings.Index(r.Log, "ok  \tex/a"); i > j {
		t.Errorf("package a's lines are split:\n%s", r.Log)
	}
}

// A JUnit document, nested suites and all, counts its cases: a skipped
// one by <skipped>, a failed one by <failure> or <error>.
func TestAJUnitReportCountsItsCases(t *testing.T) {
	t.Parallel()
	doc := `<?xml version="1.0"?>
<testsuites><testsuite name="s">
  <testcase classname="app.Store" name="saves"/>
  <testcase classname="app.Store" name="imports"><skipped message="no DATABASE_URL"/></testcase>
  <testcase classname="app.Store" name="breaks"><failure message="x"/></testcase>
  <testsuite name="inner"><testcase name="errs"><error/></testcase><testcase name="ok"/></testsuite>
</testsuite></testsuites>`
	r, err := Read([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if r.Format != JUnit || r.Counts != (Counts{Passed: 2, Failed: 2, Skipped: 1}) || !slices.Equal(r.Skipped, []string{"app.Store.imports"}) {
		t.Fatalf("read %s %+v %q", r.Format, r.Counts, r.Skipped)
	}
	if one, err := Read([]byte(`<testsuite><testcase name="a"/></testsuite>`)); err != nil || one.Counts.Passed != 1 {
		t.Fatalf("a lone testsuite: %+v %v", one, err)
	}
}

// A report that is empty, in neither format, or broken is an error, never
// zero tests.
func TestAnUnreadableReportIsNotZeroTests(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"empty":           " \n",
		"plain text":      "ok  \tex/a\t0.1s\n",
		"other XML":       "<html><body/></html>",
		"broken XML":      "<testsuites><testcase",
		"a stray line":    `{"Action":"pass","Package":"ex/a","Test":"T"}` + "\nok ex/a\n",
		"JSON, no action": `{"Package":"ex/a"}`,
	} {
		if r, err := Read([]byte(body)); err == nil {
			t.Errorf("%s: read as %+v", name, r)
		}
	}
	if _, err := Read(nil); !errors.Is(err, ErrEmpty) {
		t.Errorf("an empty report: %v", err)
	}
}
