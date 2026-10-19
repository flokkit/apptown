package main

import (
	"fmt"
	"strings"
)

// command is one verb of the CLI: its names, its lines in `forge help`, and
// what it runs. The table below is the only list of commands; dispatch and
// help both read it, so a command cannot exist without being documented.
type command struct {
	names []string // the first is the name; the rest are aliases
	help  string   // its lines in `forge help`, verbatim
	run   func(args []string) error
}

// section is a group of commands in `forge help`.
type section struct {
	heading, footer string
	commands        []command
}

func verb(name string) func([]string) error {
	return func(args []string) error { return cmdWorkVerb(name, args) }
}

func noArgs(f func() error) func([]string) error {
	return func([]string) error { return f() }
}

var sections = []section{
	{
		heading: "promised through 1.x (stock/docs/guides/compatibility.md); the loop stays with the work (-y auto-approves, --no-follow fires and returns):",
		footer: "  (project defaults: -p, else $FORGE_PROJECT, else cwd basename)\n" +
			"  (--json on new, fix, quick, plan, build, land, show, changes, wait, approve, release, complain and\n" +
			"   version prints one JSON document and never follows or asks; stock/docs/guides/cli-json.md)",
		commands: []command{
			{[]string{"new"}, `  new <slug> [--name N] [--pack P] [--toolchain T] [--remote]
                                        create project: repo + toolchain + remote + pack
  new <slug> --from <https-url|bundle> [--secret-for-git NAME] [--branch B]
                                        start it from existing code: its default branch and tags`, cmdNew},
			{[]string{"fix"}, `  fix    [-p proj] "<bug report>"       reproduce first, then fix (fix-bug workflow)`, verb("fix")},
			{[]string{"quick"}, `  quick  [-p proj] "<small task>"       small change (quick-change workflow)`, verb("quick")},
			{[]string{"plan"}, `  plan   [-p proj] "<broader task>"     plan → you approve the plan → execute (plan-execute workflow)`, verb("plan")},
			{[]string{"build"}, `  build  [-p proj] "<what to build>"    build an app or a new capability from a description (implement workflow)`, verb("build")},
			{[]string{"land"}, `  land   <change-id> [-y]               gates → approval → merge → deploy → release`, cmdLand},
			{[]string{"show"}, `  show   <change-id>                    the cockpit in the terminal`, cmdShow},
			{[]string{"changes"}, `  changes [-p proj] [--all]             what's in flight and what each waits on (--all: shipped too)`, cmdChanges},
			{[]string{"wait"}, `  wait [-c] <run-id|change-id> [-i secs]
  wait -p <slug> -r <release-number>    (a release: follow its log until released or failed)
  wait operation <id> [-i secs]         (an operation: a copy, a check, a take-down; exit 0 succeeded,
                                        1 failed or cancelled, 4 unknown: look before asking again)
                                        block until it finishes, fails, or needs a person (exit 0/1/2);
                                        a number that names no run but a change waits on the change;
                                        a run that ended without finishing is named with why and what next`, cmdWait},
			{[]string{"approve"}, `  approve <step-id> [-m comment] [-y]   show what approving means, then approve`, func(args []string) error { return cmdDecide(args, "approve") }},
			{[]string{"release"}, `  release <project-slug> <deployment>   release what landed on main to that deployment and follow its log` + "\n" +
				`                                        (--accept-uncopied-data: release though AppTown cannot copy the data it holds now;` + "\n" +
				`                                        --accept-unchecked-copy: fill a deployment that starts from a copy not shown to come back whole)` + "\n" +
				`  release <project-slug> [-domain host] release what landed on main to the intranet and follow its log`, cmdRelease},
			{[]string{"complain"}, `  complain "<what happened>" [-why W] [-wish W] [-about forge|way-of-working|project]
                                        file a complaint (default: about AppTown itself, platform friction)`, cmdComplain},
			{[]string{"version", "-version", "--version"}, `  version                               this CLI's version and AppTown's`, cmdVersion},
		},
	},
	{
		heading: "not promised in 1.x (a 1.x release may change these):",
		commands: []command{
			{[]string{"uplift"}, `  uplift [-p proj]                      dependency uplift (dependency-uplift workflow)`, verb("uplift")},
			{[]string{"excavate"}, `  excavate [-p proj] ["<focus>"]        reverse-engineer the codebase into REQUIREMENTS.md (excavate workflow)`, verb("excavate")},
			{[]string{"adopt"}, `  adopt  <capability> [-p proj]         attach an integration and wire it in`, cmdAdopt},
			{[]string{"retire"}, `  retire <capability> [-p proj]         unwire an integration, then detach it`, cmdRetire},
			{[]string{"follow"}, `  follow <change-id> [-y]               re-attach the loop to a change in flight`, cmdFollow},
			{[]string{"retry"}, `  retry  <change-id> [-y]               blocked change: another round (fresh agent + feedback)
    retry -c / --continue               …same agent resumes its session, memory intact
    retry --restart                     …wind the work back to the run's base and start over`, cmdRetry},
			{[]string{"sendback", "send-back"}, `  sendback <change-id> "<note>"         blocked change: close the landing, rework from your note`, cmdSendBack},
			{[]string{"override"}, `  override <change-id> "<why>"          blocked change: overrule the machine verdict (recorded)`, cmdOverride},
			{[]string{"abandon"}, `  abandon <change-id> ["<why>"]         close a change (its record is kept)`, cmdAbandon},
			{[]string{"import"}, `  import <branch> [-p proj]             import an outside branch as a change (commits only, no run history)`, cmdImport},
			{[]string{"preview"}, `  preview <change-id>                   boot or reuse a change's preview and print its address
                                        (inside a change chat, bare: that chat's change)`, cmdPreview},
			{[]string{"projects"}, `  projects                              list projects`, noArgs(cmdProjects)},
			{[]string{"bundle"}, `  bundle -p <slug> > <slug>.bundle      download a project's code (default branch and tags) as a Git bundle`, cmdBundle},
			{[]string{"delete"}, `  delete <slug> [--cancel]              delete a project (type its slug to confirm) and follow it until it is
                                        deleted and kept; --cancel before its first take-down`, cmdDelete},
			{[]string{"restore"}, `  restore <slug>                        bring a deleted project back, with no deployments, until its purge starts`, cmdRestore},
			{[]string{"purge"}, `  purge <slug>                          admin: purge a deleted project now (its backups, repository and records)`, cmdPurge},
			{[]string{"pack"}, `  pack -p <slug> <pack-name>            apply a pack to an existing project
  pack versions <pack>                  a pack's versions: names, what each was made from, which is recommended
  pack recommend <pack> <version> [--what-if <id>]
                                        make a version the one new projects get and others are offered (a person's act),
                                        naming the what-if it was decided on`, cmdPack},
			{[]string{"workflows"}, `  workflows -p <slug>                   list a project's workflows`, cmdWorkflows},
			{[]string{"schedules"}, `  schedules -p <slug>                   a project's schedules: cron, next and last firing (UTC)`, cmdSchedules},
			{[]string{"catalog"}, `  catalog                               list the change catalog (canned, versioned change definitions)
  catalog <name>                        show one catalog change: params, workflow, prompt
  catalog promote <name> -m "<why>"     admin: admit the latest draft, naming its evaluation receipts`, cmdCatalog},
			{[]string{"apply"}, `  apply  <name> [-p proj] [--param k=v] run a catalog change on a project (drafts run as evaluations)`, cmdApply},
			{[]string{"start"}, `  start -p <slug> -t <title> [--type T] [--body B] [--ref R]
                                        open a change with its branch and no run yet`, cmdStart},
			{[]string{"invoke"}, `  invoke -p <slug> -w <workflow> -t <title> [--type T] [--body B]
                                        open a change and start its run
  invoke -c <change-id> -w <workflow>   start a run on an existing change`, cmdInvoke},
			{[]string{"runs"}, `  runs [-p <slug>] [--status all|done|running]
                                        list runs (default: running)`, cmdRuns},
			{[]string{"executing"}, `  executing                             steps and operations executing right now, in every project (admin):
                                        what an AppTown restart would interrupt; exit 0 none, 3 some`, cmdExecuting},
			{[]string{"status"}, `  status                                your home: what is waiting on you and what is running
  status <run-id>                       run state: stages, current checks, step ids`, cmdStatusOrHome},
			{[]string{"timing"}, `  timing [<workflow>] [-days N] [-r <run-id>]
                                        where the time goes: per step across a workflow's runs, or one run's timeline`, cmdTiming},
			{[]string{"advance"}, `  advance <run-id>                      request advance through the current gate`, cmdAdvance},
			{[]string{"recheck-stage"}, `  recheck-stage <run-id>                run the current stage's checks again (when it is missing evidence)`, cmdRecheckStage},
			{[]string{"abandon-run"}, `  abandon-run <run-id> [-m reason]      abandon a single run (terminal; child runs follow)`, cmdAbandonRun},
			{[]string{"invite"}, `  invite <username> [--role member|admin] [--label text] [--days N]
                                        print a one-time link that lets a person set their own password (admin)`, cmdInvite},
			{[]string{"password-link"}, `  password-link <username> [--days N]  print a one-time password reset link for an account that has a password (admin)`, cmdPasswordLink},
			{[]string{"token"}, `  token create <name> --purpose cli|agent [-u user] [-r role] · token list · token revoke <name>
                                        personal access tokens (admin); cli = your own command line (FORGE_TOKEN),
                                        agent = a coding agent's MCP connection (may not approve, land or administer)`, cmdToken},
			{[]string{"export-project"}, `  export-project -p <proj> > <file>     a project with its history and chats, to move it to another forge (admin)`, cmdExportProject},
			{[]string{"secret"}, `  secret set <NAME> < file · secret remove <NAME>
                                        put a credential AppTown uses (a target's token) in its secrets file (admin);
                                        the value comes from standard input, never the command line`, cmdSecret},
			{[]string{"reject"}, `  reject  <step-id> [-m comment]        reject an approval step`, func(args []string) error { return cmdDecide(args, "reject") }},
			{[]string{"run-step"}, `  run-step <step-id>                    (re)run a script step or re-check an assertion`, cmdRunStep},
			{[]string{"attach"}, `  attach <run-id> -k <kind> [-n name] [-r ref]
                                        attach an artifact to a run`, cmdAttach},
			{[]string{"trace"}, `  trace <run-id> [--json]               full event trace of a run`, cmdTrace},
			{[]string{"diff"}, `  diff <run-id>                         run branch diff vs the default branch`, cmdDiff},
			{[]string{"watch"}, `  watch <run-id>                        follow a run live: step transitions + agent activity`, cmdWatch},
			{[]string{"log"}, `  log [-p <slug>] [-k kind] [-n N]      project-state audit log (state changes outside runs)`, cmdLog},
			{[]string{"findings"}, `  findings <run-id>                     a run's scan/review findings`, cmdFindings},
			{[]string{"review"}, `  review [-p proj] [--no-follow]        check every project (or one) against its adopted ways of working`, cmdReview},
			{[]string{"whatif"}, `  whatif <pack> <version> [-p p1[,p2]] [--rubrics <dollars>] [--no-follow]
                                        judge a published pack version's checks on projects' main beside what they
                                        adopted: every project on an earlier version, or those -p names; one line
                                        per project (new failures, could not assess and why). Never changes
                                        standing; model rubrics only with --rubrics and a spend limit. Run it
                                        before forge pack recommend
  whatif show <id>                      a what-if's answer again
  whatif adopt <id> -p p1[,p2]          open one adoption Change per named project, taking what the what-if
                                        assessed for it (exit 0 only when every one opened)`, cmdWhatIf},
			{[]string{"integrations"}, `  integrations [-p proj]                what the project is connected to, and whether it is wired in`, cmdIntegrations},
			{[]string{"data"}, `  data -p <slug> <deployment>           how a cloud deployment's data is protected: copies, provider backups, last check
  data -p <slug> [-no-wait] copy|check|rehearse|test <deployment>
                                        copy its data to the backup store now, check the newest copy comes back,
                                        rehearse a restore, or test one on its platform; follows the operation
                                        to its end (exit as forge wait operation), or with -no-wait prints it
  data -p <slug> take-down <deployment> [--without-final-copy]
                                        take it down; refused when no final copy of its data can be taken,
                                        unless you choose to go without one (its data then stays at the provider)`, cmdData},
			{[]string{"eval"}, `  eval start --changes 12,15 (--model <profile> | --pack <name@version>) --budget <dollars>
                                        re-run landed Changes in throwaway copies, as their projects run now
                                        and on another model profile or pack version (shows what starting means first)
  eval show <id>                        an eval's arms: ready to land, checks found new and fixed, deploy, cost, complaints
  eval continue <id>                    let a held eval start its next arm (unknown spend; the starter's call)
  eval delete <id>                      stop it and delete its copies (its starter, or an administrator)`, cmdEval},
			{[]string{"complaints"}, `  complaints                            list complaints
  complaints answer <id> -m "<what>"    put the resolution on the record`, cmdComplaints},
		},
	},
	{
		heading: "not promised in 1.x, chats (AppTown's own helper):",
		commands: []command{
			{[]string{"chat"}, `  chat -p <slug> [-c <change-id> [-step <id>]]  start a chat: read-only on the project, working on a change, or taking a stopped step to chat`, cmdChat},
			{[]string{"say"}, `  say <session-id> "<message>"          send the helper one message`, cmdSay},
			{[]string{"wait-chat"}, `  wait-chat <session-id>                block until the helper hands the turn back`, cmdWaitChat},
			{[]string{"transcript"}, `  transcript <session-id> [-from N]     print the chat from a message offset`, cmdTranscript},
			{[]string{"interrupt"}, `  interrupt <chat-id> | -step <step-id> stop the helper's current turn, or a running agent step`, cmdInterrupt},
		},
	},
	{
		heading: "not promised in 1.x, inside a change chat or agent step:",
		commands: []command{
			{[]string{"check"}, `  check [-c <change-id>] [--published]  run the change's declared checks against this folder's HEAD commit`, cmdCheck},
			{[]string{"recheck"}, `  recheck                               re-run this change's failed landing gates`, cmdRecheck},
		},
	},
	{
		heading: "not promised in 1.x, local development (reads ./.forge/app.yaml):",
		commands: []command{
			{[]string{"config"}, `  config schema | config validate [path]
                                        the app contract's JSON Schema, or check a contract`, cmdConfig},
			{[]string{"dev"}, `  dev [up | down]                       bring up the app's needs (or the whole app) locally`, cmdDev},
		},
	},
}

// helpCommand prints forge help; it is not listed in its own output.
var helpCommand = command{names: []string{"help", "-h", "--help"}, run: func([]string) error { usage(); return nil }}

// commandNamed says whether a word names a command. The loop verbs ask it,
// and they are in the table lookup reads, so it is bound at init.
var commandNamed func(string) bool

func init() { commandNamed = func(n string) bool { return lookup(n) != nil } }

// lookup finds the command a name or alias selects, or nil.
func lookup(name string) *command {
	for _, n := range helpCommand.names {
		if n == name {
			return &helpCommand
		}
	}
	for i := range sections {
		for j := range sections[i].commands {
			for _, n := range sections[i].commands[j].names {
				if n == name {
					return &sections[i].commands[j]
				}
			}
		}
	}
	return nil
}

// invoke runs the command, unless its arguments ask for help: then it prints
// the command's lines from forge help and does nothing else. A command that
// reads no flags of its own would otherwise act on "--help" as if it were
// absent (`forge dev up --help` once started a local boot). "--" ends the
// flags, so a literal "--help" after it is an argument.
func (c *command) invoke(args []string) error {
	for _, a := range args {
		if a == "--" {
			break
		}
		if c.help != "" && (a == "-h" || a == "--help" || a == "-help") {
			fmt.Println(c.help)
			return nil
		}
	}
	return c.run(args)
}

// cmdStatusOrHome: bare `forge status` is the home screen; with an id it is
// one run's state.
func cmdStatusOrHome(args []string) error {
	if len(args) == 0 {
		return cmdHome()
	}
	return cmdStatus(args)
}

func usage() { fmt.Print(helpText()) }

func helpText() string {
	var b strings.Builder
	b.WriteString("forge — command-line client for an AppTown instance\n")
	for _, s := range sections {
		b.WriteString("\n")
		if s.heading != "" {
			b.WriteString(s.heading + "\n")
		}
		for _, c := range s.commands {
			b.WriteString(c.help + "\n")
		}
		if s.footer != "" {
			b.WriteString(s.footer + "\n")
		}
	}
	b.WriteString("\nenvironment: FORGE_URL (default http://localhost:7777), FORGE_USER (default $USER)\n")
	b.WriteString("exit 4: a change was sent but its outcome is unknown (the answer was lost); look before trying again\n")
	return b.String()
}
