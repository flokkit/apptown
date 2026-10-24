"""Regressions for scripts/container-output-check.go, which refuses reading what a container printed; no database required."""
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path


class ContainerOutputCheckTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.work = tempfile.TemporaryDirectory(prefix="apptown-container-output-check-")
        cls.addClassCleanup(cls.work.cleanup)
        cls.binary = Path(cls.work.name) / "container-output-check"
        source = Path(__file__).with_name("container-output-check.go")
        subprocess.run(["go", "build", "-o", str(cls.binary), str(source)], check=True)

    def setUp(self):
        self.tree = tempfile.TemporaryDirectory(prefix="apptown-container-output-tree-")
        self.addCleanup(self.tree.cleanup)
        self.root = Path(self.tree.name)
        self.write("go.mod", "module example.com/m\n")

    def write(self, rel, source):
        path = self.root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(textwrap.dedent(source))

    def found(self, body, rel="internal/probe/probe.go"):
        """What the checker refuses in one file holding body."""
        package = Path(rel).parent.name.replace("-", "")
        self.write(rel, f'''package {package}
import (
    "context"
    "errors"
    "fmt"
    "strings"
)
var _, _, _ = errors.New, fmt.Sprint, strings.TrimSpace
type Command struct {{ Args []string; Stdout any }}
type Scope struct{{}}
func (Scope) Run(context.Context, ...string) ([]byte, error) {{ return nil, nil }}
func (Scope) OK(context.Context, ...string) bool {{ return true }}
func (Scope) Command(context.Context, Command) ([]byte, error) {{ return nil, nil }}
func parse([]byte) {{}}
''' + textwrap.dedent(body))
        out = subprocess.run([str(self.binary), "-root", str(self.root)], capture_output=True, text=True, timeout=120)
        self.assertNotEqual(out.returncode, 2, out.stderr)
        lines = [line.strip() for line in out.stderr.splitlines() if line.startswith("  ")]
        self.assertEqual(out.returncode != 0, bool(lines), out.stderr)
        return lines

    def test_a_result_read_from_what_a_container_printed_is_refused(self):
        self.assertEqual(len(self.found('''
            func a(ctx context.Context, dk Scope) string {
                out, _ := dk.Run(ctx, "run", "--rm", "busybox", "dockerd", "--version")
                return string(out)
            }
            func b(ctx context.Context, dk Scope) {
                out, err := dk.Run(ctx, "exec", "db", "sh", "-c", "measure")
                if err == nil {
                    parse(out)
                }
            }
            func c(ctx context.Context, dk Scope) {
                args := []string{"run", "--rm", "--network", "none"}
                args = append(args, "busybox", "ls", "/cache")
                out, _ := dk.Run(ctx, args...)
                for range strings.Fields(string(out)) {
                }
            }
            func d(ctx context.Context, dk Scope) bool {
                return strings.TrimSpace(func() string { out, _ := dk.Run(ctx, "run", "--rm", "img", "id", "-u"); return string(out) }()) == "0"
            }
        ''')), 4)

    def test_a_formatted_result_is_followed_to_where_it_goes(self):
        # Review round 1: a Sprintf of a result returned is a read.
        self.assertEqual(len(self.found('''
            func a(ctx context.Context, dk Scope) string {
                out, _ := dk.Run(ctx, "run", "--rm", "image", "version")
                return fmt.Sprintf("%s", out)
            }
        ''')), 1)

    def test_a_stream_out_of_a_container_is_refused(self):
        self.assertEqual(len(self.found('''
            func a(ctx context.Context, dk Scope, w any) {
                _, _ = dk.Command(ctx, Command{Args: []string{"exec", "db", "pg_dump"}, Stdout: w})
            }
        ''')), 1)

    def test_error_text_status_detached_and_terminals_are_not_reads(self):
        self.assertEqual(self.found('''
            func a(ctx context.Context, dk Scope) error {
                if out, err := dk.Run(ctx, "run", "--rm", "img", "chown", "-R", "1:1", "/mnt"); err != nil {
                    return fmt.Errorf("chown: %v: %s", err, strings.TrimSpace(string(out)))
                }
                if out, err := dk.Run(ctx, "exec", "c", "rm", "-f", "/x"); err != nil {
                    return errors.New("rm: " + string(out))
                }
                _, err := dk.Run(ctx, "exec", "c", "true", "now")
                id, _ := dk.Run(ctx, "run", "-d", "--name", "web", "img")
                parse(id)
                term, _ := dk.Run(ctx, "exec", "-it", "c", "tmux")
                parse(term)
                if dk.OK(ctx, "exec", "c", "docker", "info") {
                    return nil
                }
                return err
            }
        '''), [])

    def test_the_owner_tests_and_test_helpers_are_not_read(self):
        body = '''
            func a(ctx context.Context, dk Scope) string {
                out, _ := dk.Run(ctx, "run", "--rm", "busybox", "ls")
                return string(out)
            }
        '''
        for rel in ["internal/containerresult/result.go", "internal/probe/probe_test.go", "internal/worker/workertest/w.go", "internal/deploy/docker/dockerfake/d.go", "scripts/tool/t.go"]:
            with self.subTest(rel=rel):
                self.setUp()
                self.assertEqual(self.found(body, rel), [])


if __name__ == "__main__":
    unittest.main()
