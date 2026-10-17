package gittest

import (
	"testing"
)

type recorder struct {
	testing.TB
	fatal string
}

func (r *recorder) Helper() {}
func (r *recorder) Fatal(args ...any) {
	r.fatal = "fatal"
	panic(r)
}
func (r *recorder) Fatalf(string, ...any) {
	r.fatal = "fatalf"
	panic(r)
}

func refused(f func(t testing.TB)) (r *recorder) {
	r = &recorder{}
	defer func() { _ = recover() }()
	f(r)
	return r
}

func TestRunRefusesAnEmptyDirectory(t *testing.T) {
	if r := refused(func(tb testing.TB) { Run(tb, "", "status") }); r.fatal == "" {
		t.Fatal("git with no directory would run in the repository under test")
	}
}

func TestRunAsTesterCommitsWithTheTesterIdentity(t *testing.T) {
	dir := t.TempDir()
	Run(t, dir, "init", "-q", "-b", "main")
	RunAsTester(t, dir, "commit", "-q", "--allow-empty", "-m", "first")
	if got := Run(t, dir, "log", "-1", "--format=%an <%ae>"); got != "test <test@example.com>" {
		t.Fatalf("author %q", got)
	}
	if r := refused(func(tb testing.TB) { Run(tb, dir, "rev-parse", "no-such-ref") }); r.fatal != "fatalf" {
		t.Fatal("a failing git command must fail the test")
	}
}
