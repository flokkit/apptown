package agentenv

// The generated sections of an agent step's steering, as one inventory
// so they count in the totals. buildSteering (agent.go)
// lays the steering out as the complaints rule, the project's pinned
// guidance, then machine-written sections — some in every step, some only
// in steps of a certain shape. Guidance sizes alone understate what an
// agent's context carries; this inventory sizes the rest, in the order it
// is written, so the totals on the guidance surfaces are honest.

// WorkspaceHygiene keeps agents out of .forge/sessions (see buildSteering),
// for an agent working in the run's folder.
var WorkspaceHygiene = WorkspaceHygieneAt(".forge/sessions", ".forge/attachments/")

// WorkspaceHygieneAt is the hygiene section for an agent whose transcripts
// and attachments are where it names (a step on a worker has them outside
// its tree).
func WorkspaceHygieneAt(sessions, attachments string) string {
	return "# Workspace hygiene\n\nNEVER list, read, or cat anything under " + sessions + " — it holds\nhuge machine transcripts that will destroy your context. The .forge\npaths for you are .forge/app.yaml, " + attachments + ", and the\nreport/output file your task names (e.g. .forge/REPORT.md) — these are\ngitignored, so they stay out of the repo while the forge still records\nthem. Everything else the app needs lives at the repo root.\n\n"
}
