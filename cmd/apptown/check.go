package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/ids"
)

// apptown check [-c changeID] [--published] — run the change's declared gate
// checks against a commit and report the verdicts. Inside an agent step
// $APPTOWN_CHANGE is set, so no flag is needed: the agent verifies against its
// own change before handing the turn back, instead of discovering a red gate
// after the run ends. Exits nonzero when a check fails at/above the gating
// severity.
//
// The commit checked is this folder's HEAD, sent with the request: the forge
// checks it only when it has it as the change's work, and otherwise refuses,
// naming it (an agent on a worker commits where the forge cannot see until
// the step's work is collected). --published sends {"published": true}: the
// forge checks the tip of the change's branch in its repository, what has
// been published, and saves or publishes nothing to do it; with no branch
// there it refuses. Outside a Git repository nothing is named ({}) and the
// forge checks the change's latest commit it has. Every answer names the
// commit it is for.
//
// In an agent step on a worker ($APPTOWN_SCRATCH_REF set), the forge does not
// have the agent's commits until the step's work is collected. So the CLI
// first pushes HEAD to that ref, the one its step's token may write, and
// the forge checks it there. That result is feedback only: nothing is
// published, and the landing checks the published commit again.
func cmdCheck(args []string) error {
	fs := newFlags("check")
	change := fs.String("c", "", "the change: its id or <project>#<number> (default: $APPTOWN_CHANGE, set in agent steps)")
	published := fs.Bool("published", false, "check the tip of the change's published branch on AppTown, not this folder's HEAD")
	parseArgs(fs, args)
	var cid ids.ID
	if *change != "" {
		var err error
		if cid, err = changeRef(*change, ""); err != nil {
			return err
		}
	} else {
		cid, _ = ids.Parse(os.Getenv("APPTOWN_CHANGE"))
	}
	if cid.IsZero() {
		return fmt.Errorf("no change: pass -c <id> (or run inside an agent step, where $APPTOWN_CHANGE is set)")
	}

	body := map[string]any{}
	var asked string
	switch {
	case *published:
		body["published"] = true
		fmt.Println(dim(fmt.Sprintf("--published: checking the published tip of change %s's branch on AppTown, not this folder's work", cid)))
	default:
		head, dirty, err := folderHead()
		if err != nil {
			return err
		}
		if head == "" {
			fmt.Println(dim(fmt.Sprintf("not in a Git repository: checking change %s's latest commit on AppTown", cid)))
			break
		}
		asked = head
		body["commit"] = head
		fmt.Println(dim("checking commit " + head + " (this folder's HEAD)"))
		if dirty {
			fmt.Println(yellow("  uncommitted changes here are not checked — commit them first"))
		}
		if ref := os.Getenv("APPTOWN_SCRATCH_REF"); ref != "" {
			if err := sendScratch(head, ref); err != nil {
				// The forge may have the commit anyway; if not, it says so.
				fmt.Println(yellow("  could not send this commit to AppTown for checking: " + err.Error()))
			}
		}
	}

	raw, err := api.request("POST", fmt.Sprintf("/changes/%d/check", cid), body)
	if err != nil {
		return err
	}
	var out struct {
		Commit      string `json:"commit"`
		Scratch     bool   `json:"scratch"`
		OK          bool   `json:"ok"`
		Failing     int    `json:"failing"`
		CheckErrors int    `json:"checkErrors"`
		FailOn      string `json:"failOn"`
		Checks      []struct {
			Block     string `json:"block"`
			Version   int    `json:"version"`
			Check     string `json:"check"`
			Verdict   string `json:"verdict"`
			Output    string `json:"output"`
			Tool      string `json:"tool"`
			Scanner   string `json:"scanner"`
			Evaluator string `json:"evaluator"`
			Note      string `json:"note"`
			// How a failing check read against main: already failing there,
			// or not decidable (the note says why). Neither blocks.
			Preexisting bool `json:"preexisting"`
			Undecided   bool `json:"undecided"`
		} `json:"checks"`
		Note string `json:"note"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	// A result is shown only for the commit asked about: an answer for any
	// other commit (an older one, say) is not this work's result.
	if asked != "" && out.Commit != asked {
		return fmt.Errorf("AppTown answered for commit %q, not %s which was asked about; its result is not shown", out.Commit, asked)
	}
	if out.Commit == "" {
		return fmt.Errorf("AppTown's answer names no commit; its result is not shown")
	}
	if *published {
		fmt.Println("commit " + out.Commit + " (the published tip of change " + cid.String() + "'s branch)")
	} else if out.Scratch {
		fmt.Println("commit " + out.Commit + " (your work in progress, sent for checking: this result is feedback only, and the landing checks the published commit again)")
	} else {
		fmt.Println("commit " + out.Commit)
	}

	if len(out.Checks) == 0 {
		fmt.Println(dim(fmt.Sprintf("no checks apply to commit %s", out.Commit)))
		return nil
	}
	for _, c := range out.Checks {
		mark := yellow("!")
		switch c.Verdict {
		case "pass":
			mark = green("✓")
		case "fail":
			mark = red("✗")
		case "skipped":
			mark = dim("–")
		}
		var suffix string
		switch {
		case c.Evaluator == "scanner":
			suffix = "(scan)"
		case c.Tool != "":
			suffix = fmt.Sprintf("(%s v%d · tool: %s)", c.Block, c.Version, c.Tool)
		default:
			suffix = fmt.Sprintf("(%s v%d)", c.Block, c.Version)
		}
		switch {
		case c.Verdict == "fail" && c.Undecided:
			mark, suffix = yellow("?"), suffix+" "+yellow("undecided")
		case c.Verdict == "fail" && c.Preexisting:
			mark, suffix = dim("✗"), suffix+" "+dim("already on main")
		}
		fmt.Printf("  %s %s %s\n", mark, c.Check, dim(suffix))
		if c.Verdict == "fail" && c.Output != "" {
			fmt.Println("      " + dim(c.Output))
		}
		if c.Verdict == "fail" && (c.Undecided || c.Preexisting) && c.Note != "" {
			fmt.Println("      " + dim(c.Note))
		}
		if c.Verdict == "error" && c.Output != "" {
			fmt.Println("      " + dim(c.Output))
		}
		if c.Verdict == "skipped" && c.Note != "" {
			fmt.Println("      " + dim(c.Note))
		}
	}
	fmt.Println()
	if out.Note != "" {
		fmt.Println(dim(out.Note))
	}
	if out.OK {
		fmt.Println(green(fmt.Sprintf("✓ gate clear for commit %s — %d check(s) pass", out.Commit, len(out.Checks))))
		if out.CheckErrors > 0 {
			fmt.Println(yellow(fmt.Sprintf("  %d check(s) errored — they don't gate, but may be hiding a real result", out.CheckErrors)))
		}
		return nil
	}
	fmt.Println(red(fmt.Sprintf("✗ commit %s: %d check(s) fail at/above %s — the landing gate blocks on these", out.Commit, out.Failing, out.FailOn)))
	os.Exit(1)
	return nil
}

// folderHead is the commit at HEAD of the Git repository the CLI runs in,
// and whether its working tree has uncommitted changes. Outside a
// repository it is "" with no error; a repository with no commit yet is an
// error, since there is nothing to check.
func folderHead() (head string, dirty bool, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false, fmt.Errorf("reading the current folder: %w", err)
	}
	// Git here is the person's: it sees their whole environment, GIT_*
	// included (a hook's GIT_DIR, a worker's GIT_CONFIG_* granting
	// safe.directory), and their own git config, neither of which the
	// forge's own git reads.
	theirs := os.Environ()
	err = interruptible(root, func(ctx context.Context) error {
		if inside, err := theirGit(ctx, cwd, theirs, "rev-parse", "--is-inside-work-tree"); err != nil || inside != "true" {
			return nil
		}
		head, err = theirGit(ctx, cwd, theirs, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil || !gitexec.IsObjectID(head) {
			head = ""
			return fmt.Errorf("this folder's repository has no commit to check: commit your work, or pass --published to check the change's latest commit on AppTown")
		}
		status, _ := theirGit(ctx, cwd, theirs, "status", "--porcelain")
		dirty = status != ""
		return nil
	})
	return head, dirty, err
}

// theirGit runs git in dir as the person who runs the CLI, in their own
// repository: env is git's whole environment, so their GIT_* variables and
// their own system and global git config apply (safe.directory, a proxy,
// an includeIf), which the forge's own git (gitexec.Run) never reads. Like
// the forge's git it takes no optional locks, so a status read never
// refuses their commit. It returns stdout and stderr together, trimmed.
func theirGit(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := gitexec.Command(ctx, dir, append(slices.Clone(env), "GIT_OPTIONAL_LOCKS=0"), args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// sendScratch pushes commit to ref, the step's scratch ref, on the forge's
// Git endpoint ($APPTOWN_URL/git/$APPTOWN_PROJECT), with the step's token as a
// header git sends to that URL only: never in the URL or the arguments.
// The forge accepts only that ref from that token, and replaces what an
// earlier check sent.
func sendScratch(commit, ref string) error {
	base, slug, token := os.Getenv("APPTOWN_URL"), os.Getenv("APPTOWN_PROJECT"), os.Getenv("APPTOWN_SESSION_TOKEN")
	if base == "" || slug == "" || token == "" {
		return fmt.Errorf("this step has no forge address, project or token to send it with")
	}
	remote := strings.TrimRight(base, "/") + "/git/" + slug
	// Added to the git configuration the environment already carries (a
	// worker sets safe.directory this way), not in place of it: the
	// person's whole environment, then this.
	n, _ := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT"))
	env := append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		fmt.Sprintf("GIT_CONFIG_COUNT=%d", n+1),
		fmt.Sprintf("GIT_CONFIG_KEY_%d=http.%s.extraHeader", n, remote),
		fmt.Sprintf("GIT_CONFIG_VALUE_%d=Authorization: Bearer %s", n, token),
	)
	ctx, cancel := context.WithTimeout(root, 5*time.Minute)
	defer cancel()
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return interruptible(ctx, func(ctx context.Context) error {
		if out, err := theirGit(ctx, cwd, env, "push", "--quiet", "--force", "--no-verify", remote, commit+":"+ref); err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		return nil
	})
}
