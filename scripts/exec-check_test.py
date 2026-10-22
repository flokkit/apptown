"""Regressions for scripts/exec-check.go, the process-start ratchet; no database required."""
import json
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path


class ExecCheckTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.work = tempfile.TemporaryDirectory(prefix="apptown-exec-check-")
        cls.addClassCleanup(cls.work.cleanup)
        cls.binary = Path(cls.work.name) / "exec-check"
        source = Path(__file__).with_name("exec-check.go")
        subprocess.run(["go", "build", "-o", str(cls.binary), str(source)], check=True)

    def setUp(self):
        self.tree = tempfile.TemporaryDirectory(prefix="apptown-exec-tree-")
        self.addCleanup(self.tree.cleanup)
        self.root = Path(self.tree.name)
        self.write("go.mod", "module example.com/m\n")
        # The owner's one git constructor, which the git-by-name rule allows.
        self.write("internal/owner/git.go", 'package owner\nimport "os/exec"\nfunc Git() *exec.Cmd { return exec.Command("git") }\n')
        self.baseline({"internal/owner": "runs processes for everyone"}, {})

    def write(self, rel, source):
        path = self.root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(textwrap.dedent(source))

    def go(self, rel, body, imports='import "os/exec"'):
        package = Path(rel).parent.name.replace("-", "")
        self.write(rel, f"package {package}\n{imports}\n{textwrap.dedent(body)}\n")

    def baseline(self, allowed, packages):
        self.write("scripts/exec-baseline.json", json.dumps({
            "allowed": allowed, "git": {"file": "internal/owner/git.go", "why": "the constructor"}, "packages": packages}))

    def stored(self):
        return json.loads((self.root / "scripts/exec-baseline.json").read_text())

    def run_check(self, *args):
        return subprocess.run([str(self.binary), "-root", str(self.root), *args],
                              capture_output=True, text=True, timeout=120)

    def counted(self, body, imports='import "os/exec"'):
        """The starts the checker finds in one file of a new package."""
        self.go("internal/probe/probe.go", body, imports)
        out = self.run_check("-v")
        self.assertNotEqual(out.returncode, 2, out.stderr)
        return [line.split(": ", 1)[1] for line in out.stdout.splitlines()
                if line.startswith("    internal/probe/")]

    def test_every_way_of_starting_a_process_counts(self):
        self.assertEqual(self.counted('''
            import (
                "context"
                "os"
                "syscall"
            )
            var start = exec.CommandContext
            func f(ctx context.Context) {
                _ = exec.Command("docker", "ps").Run()
                _ = start(ctx, "sh")
                _ = &exec.Cmd{Path: "/bin/sh"}
                _ = new(exec.Cmd)
                _, _ = os.StartProcess("/bin/sh", nil, nil)
                _ = syscall.Exec("/bin/sh", nil, nil)
                _, _ = syscall.ForkExec("/bin/sh", nil, nil)
            }
        '''), ["os/exec.CommandContext", "os/exec.Command", "exec.Cmd value", "exec.Cmd value",
               "os.StartProcess", "syscall.Exec", "syscall.ForkExec"])

    def test_recursive_types_are_walked_once(self):
        # Ordinary recursive containers, directly and mutually, hold no command.
        self.assertEqual(self.counted('''
            type Tree map[string]Tree
            type Branches []Leaves
            type Leaves map[string]Branches
            func f() { t := Tree{"child": {}}; b := Branches{Leaves{}}; _, _ = t, b }
        '''), [])
        # A recursive type that holds a command still counts, where it holds it.
        self.assertEqual(self.counted('''
            type Node struct {
                exec.Cmd
                Kids map[string][]Node
            }
            func f() { var n Node; _ = n }
        '''), ["exec.Cmd value"])

    def test_x_sys_unix_starts_count_without_its_source(self):
        # The checker stands in an empty package for x/sys/unix; its starters
        # are known by import path and name.
        self.assertEqual(self.counted('''
            func f() { _ = unix.Exec("/bin/sh", nil, nil); _ = unix.Getpid() }
        ''', 'import "golang.org/x/sys/unix"'), ["golang.org/x/sys/unix.Exec"])
        self.assertEqual(self.counted('''
            func f() { _ = Exec("/bin/sh", nil, nil) }
        ''', 'import . "golang.org/x/sys/unix"'), ["golang.org/x/sys/unix.Exec"])
        # A local of the same name, under the dot import, is not the starter.
        self.assertEqual(self.counted('''
            func f() {
                Exec := func(string, []string, []string) error { return nil }
                _ = Exec("/bin/sh", nil, nil)
            }
        ''', 'import . "golang.org/x/sys/unix"'), [])

    def test_renamed_and_dot_imports_count(self):
        self.assertEqual(self.counted('''
            func f() { _ = run.Command("git"); _ = run.Cmd{} }
        ''', 'import run "os/exec"'), ["os/exec.Command", "exec.Cmd value"])
        self.assertEqual(self.counted('''
            func f() { _ = Command("git"); _ = &Cmd{} }
        ''', 'import . "os/exec"'), ["os/exec.Command", "exec.Cmd value"])

    def test_types_comments_strings_and_other_packages_do_not_count(self):
        self.assertEqual(self.counted('''
            // exec.Command("git") in a comment
            var text = "exec.Command"
            func f(cmd *exec.Cmd) error { _, err := exec.LookPath("git"); return err }
            type runner struct{ cmd *exec.Cmd }
            type pointer = *exec.Cmd
            func g(cmds []*exec.Cmd) pointer { return (*exec.Cmd)(nil) }
        '''), [])
        self.assertEqual(self.counted('''
            type fake struct{}
            func (fake) Command(string) {}
            var exec fake
            func f() { exec.Command("git") }
        ''', ''), [])
        # os/exec imported for a value, then shadowed by a local: nothing starts.
        self.assertEqual(self.counted('''
            var missing = exec.ErrNotFound
            type fake struct{}
            func (fake) Command(string) {}
            func f() { exec := fake{}; exec.Command("git") }
        '''), [])

    def test_a_command_built_as_a_value_counts_and_runs(self):
        # Each is ordinary Go that starts a process: the fixture is run to show it.
        forms = {
            "variable": ("var cmd exec.Cmd\n\tcmd.Path, cmd.Args = sh, []string{\"sh\", \"-c\", \"echo started\"}", ""),
            "alias": ("cmd := Command{Path: sh, Args: []string{\"sh\", \"-c\", \"echo started\"}}", "type Command = exec.Cmd"),
            "defined": ("var mine Mine\n\tcmd := (*exec.Cmd)(&mine)\n\tcmd.Path, cmd.Args = sh, []string{\"sh\", \"-c\", \"echo started\"}", "type Mine exec.Cmd"),
            "elided": ("cmds := []*exec.Cmd{{Path: sh, Args: []string{\"sh\", \"-c\", \"echo started\"}}}\n\tcmd := cmds[0]", ""),
            "made": ("cmd := &make([]exec.Cmd, 1)[0]\n\tcmd.Path, cmd.Args = sh, []string{\"sh\", \"-c\", \"echo started\"}", ""),
            "named list": ("cmds := make(Commands, 1)\n\tcmd := &cmds[0]\n\tcmd.Path, cmd.Args = sh, []string{\"sh\", \"-c\", \"echo started\"}", "type Commands []exec.Cmd"),
            "recursive": ("var n Node\n\tcmd := &n.Cmd\n\tcmd.Path, cmd.Args = sh, []string{\"sh\", \"-c\", \"echo started\"}", "type Node struct {\n\texec.Cmd\n\tKids []Node\n}"),
            "embedded": ("var w wrapper\n\tcmd := &w.Cmd\n\tcmd.Path, cmd.Args = sh, []string{\"sh\", \"-c\", \"echo started\"}", "type wrapper struct{ exec.Cmd }"),
        }
        for form, (body, decl) in forms.items():
            with self.subTest(form=form):
                self.go("internal/probe/main.go", f'''
                    {decl}
                    func main() {{
                        sh, _ := exec.LookPath("sh")
                        {body}
                        out, err := cmd.Output()
                        if err != nil {{ panic(err) }}
                        os.Stdout.Write(out)
                    }}
                ''', 'import (\n"os"\n"os/exec"\n)')
                (self.root / "internal/probe/main.go").write_text(
                    (self.root / "internal/probe/main.go").read_text().replace("package probe", "package main", 1))
                ran = subprocess.run(["go", "run", "./internal/probe"], cwd=self.root, capture_output=True, text=True)
                self.assertEqual(ran.stdout, "started\n", ran.stderr)
                out = self.run_check("-v")
                self.assertEqual(out.returncode, 1, out.stdout)
                self.assertIn("internal/probe", out.stdout)
                self.assertIn("exec.Cmd value", out.stdout)

    def test_git_by_name_only_in_the_named_file_even_in_an_owner(self):
        self.go("internal/owner/other.go", 'func f() { _ = exec.Command("git", "status") }')
        out = self.run_check()
        self.assertEqual(out.returncode, 1, out.stdout)
        self.assertIn("internal/owner/other.go:3 starts git by name", out.stdout)
        (self.root / "internal/owner/other.go").unlink()
        self.go("internal/owner/other.go", 'import "context"\nfunc f(ctx context.Context) { _ = run.CommandContext(ctx, "git") }', 'import run "os/exec"')
        self.assertIn("starts git by name", self.run_check().stdout)
        (self.root / "internal/owner/other.go").unlink()
        self.go("internal/owner/other.go", 'const program = "git"\nfunc f() { _ = exec.Command(program) }')
        self.assertIn("starts git by name", self.run_check().stdout)
        (self.root / "internal/owner/other.go").unlink()
        self.assertEqual(self.run_check().returncode, 0)
        # The rule sees the start it allows, or it cannot be trusted to see others.
        (self.root / "internal/owner/git.go").write_text("package owner\n")
        out = self.run_check()
        self.assertEqual(out.returncode, 1, out.stdout)
        self.assertIn("starts no git by name", out.stdout)

    def test_allowed_and_held_packages_pass_and_a_rise_fails(self):
        self.go("internal/owner/run.go", 'func f() { _ = exec.Command("sh") }')
        self.go("internal/other/run_test.go", 'func f() { _ = exec.Command("docker"); _ = exec.Command("sh") }')
        self.baseline({"internal/owner": "runs processes"}, {"internal/other": {"count": 2, "why": "fixture"}})
        out = self.run_check()
        self.assertEqual(out.returncode, 0, out.stdout)
        self.go("internal/other/more_test.go", 'func g() { _ = exec.Command("docker") }')
        out = self.run_check()
        self.assertEqual(out.returncode, 1, out.stdout)
        self.assertIn("WORSE", out.stdout)
        self.assertIn("internal/other/more_test.go:3: os/exec.Command", out.stdout)

    def test_a_new_package_fails_even_beside_an_allowed_one(self):
        # A subdirectory is a package of its own: an owner's test helper is held like any other.
        self.go("internal/owner/ownertest/helper.go", 'func f() { _ = exec.Command("sh") }')
        out = self.run_check()
        self.assertEqual(out.returncode, 1, out.stdout)
        self.assertIn("internal/owner/ownertest", out.stdout)
        self.assertIn("NEW", out.stdout)

    def test_tighten_lowers_and_drops_but_never_raises(self):
        self.go("internal/other/run.go", 'func f() { _ = exec.Command("sh") }')
        self.go("internal/owner/run.go", 'func f() { _ = exec.Command("sh") }')
        self.baseline({"internal/owner": "runs processes"},
                      {"internal/other": {"count": 3, "why": "kept"}, "internal/gone": {"count": 1, "why": "moved"}})
        out = self.run_check()
        self.assertEqual(out.returncode, 0, out.stdout)
        self.assertIn("-tighten", out.stdout)
        self.assertEqual(self.stored()["packages"]["internal/other"]["count"], 3, "a plain run writes nothing")
        out = self.run_check("-tighten")
        self.assertEqual(out.returncode, 0, out.stdout)
        self.assertEqual(self.stored()["packages"], {"internal/other": {"count": 1, "why": "kept"}})
        self.go("internal/other/more.go", 'func g() { _ = exec.Command("sh") }')
        out = self.run_check("-tighten")
        self.assertEqual(out.returncode, 1, out.stdout)
        self.assertEqual(self.stored()["packages"], {"internal/other": {"count": 1, "why": "kept"}})

    def test_skipped_directories_and_other_modules(self):
        for rel in ("internal/x/testdata/a.go", "web/node_modules/m/a.go", ".hidden/a.go", "tools/sub/a.go"):
            self.go(rel, 'func f() { _ = exec.Command("sh") }')
        self.write("tools/go.mod", "module example.com/tools\n")
        out = self.run_check()
        self.assertEqual(out.returncode, 0, out.stdout)

    def test_a_missing_allowed_package_and_malformed_go_fail(self):
        self.baseline({"internal/owner": "runs", "internal/nowhere": "gone"}, {})
        out = self.run_check()
        self.assertEqual(out.returncode, 1, out.stdout)
        self.assertIn("internal/nowhere does not exist", out.stdout)
        self.baseline({"internal/owner": "runs"}, {})
        self.write("internal/owner/broken.go", "package owner\nfunc (")
        self.assertEqual(self.run_check().returncode, 2)


if __name__ == "__main__":
    unittest.main()
