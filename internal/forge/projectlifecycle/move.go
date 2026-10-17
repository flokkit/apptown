package projectlifecycle

import "sort"

// What moves with a project to another forge, table by table. Every project-scoped
// table is classified here, so a new one is decided, never silently left
// behind (TestEveryProjectTableIsClassifiedForAMove):
//
//   - exported: its rows are in the file, and the receiving forge imports
//     them as history;
//   - listed: reported in the file (owners/listed.json) for an operator to
//     set up again, or kept by this forge; not imported;
//   - excluded: neither, with why.
const (
	MoveExported = "exported"
	MoveListed   = "listed"
	MoveExcluded = "excluded"
)

// MoveClass is one table's classification and the reason for it.
type MoveClass struct {
	Table  string `json:"table"`
	Class  string `json:"class"`
	Reason string `json:"reason"`
}

var moveRegistry = map[string]MoveClass{
	"projects":               {Class: MoveExported, Reason: "the project's settings; not its id, group or approvers"},
	"adoptions":              {Class: MoveExported, Reason: "the current selection, by revision"},
	"project_adoptions":      {Class: MoveExported, Reason: "revision history, verbatim; never resolved on the receiving forge"},
	"project_packs":          {Class: MoveExported, Reason: "pack placement; the pack versions' content travels in the library document"},
	"policies":               {Class: MoveExported, Reason: "installed policies"},
	"guidance_imports":       {Class: MoveExported, Reason: "guidance selection; block content travels in the library document"},
	"project_lanes":          {Class: MoveExported, Reason: "lane selection"},
	"project_numbers":        {Class: MoveExcluded, Reason: "the next change and run numbers: derived from the moved changes and runs, which carry their numbers"},
	"project_pi_extensions":  {Class: MoveExported, Reason: "enabled extensions"},
	"adrs":                   {Class: MoveExported, Reason: "decision records"},
	"audit_log":              {Class: MoveExported, Reason: "the project's ledger lines, witnessed on this forge"},
	"token_usage":            {Class: MoveExported, Reason: "real spend; counts once on the receiving forge"},
	"changes":                {Class: MoveExported, Reason: "landed changes; a change not landed stays and is listed"},
	"runs":                   {Class: MoveExported, Reason: "runs not running, on a landed change or none, with their definition snapshots"},
	"workflow_continuations": {Class: MoveExported, Reason: "which run continued which"},
	"workflow_landings":      {Class: MoveExported, Reason: "which run landed which"},
	"steps":                  {Class: MoveExported, Reason: "steps with their configuration and evidence"},
	"run_events":             {Class: MoveExported, Reason: "run history"},
	"artifacts":              {Class: MoveExported, Reason: "run artifacts, bodies included"},
	"run_inference_owners":   {Class: MoveExported, Reason: "whose inference a run spent"},
	"agent_transcripts":      {Class: MoveExported, Reason: "agent step receipts; their transcript files travel beside them"},

	"secret_bindings":       {Class: MoveListed, Reason: "names and stores only; values and store refs never move"},
	"deployments":           {Class: MoveListed, Reason: "standing on this forge's targets; set up again on the receiving forge"},
	"releases":              {Class: MoveListed, Reason: "release history stays with the deployments it served"},
	"release_changes":       {Class: MoveListed, Reason: "part of release history, which stays"},
	"release_events":        {Class: MoveListed, Reason: "part of release history, which stays"},
	"workflow_schedules":    {Class: MoveListed, Reason: "a schedule starts runs on its own, so it waits for a person"},
	"project_integrations":  {Class: MoveListed, Reason: "integration bindings are this forge's own"},
	"model_slots":           {Class: MoveListed, Reason: "model names may not exist on the receiving forge"},
	"backups":               {Class: MoveListed, Reason: "data backups belong to this forge's targets and stores"},
	"backup_drills":         {Class: MoveListed, Reason: "restore drills of backups that stay"},
	"restore_rehearsals":    {Class: MoveListed, Reason: "restore tests of backups that stay"},
	"restored_copies":       {Class: MoveListed, Reason: "restored copies run on this forge's targets"},
	"restored_copy_tickets": {Class: MoveListed, Reason: "tickets for restored copies that stay"},
	"retained_resources":    {Class: MoveListed, Reason: "resources on this forge's targets"},

	"run_starts":                    {Class: MoveExcluded, Reason: "live dispatch state; every run moved has ended"},
	"run_merges":                    {Class: MoveExcluded, Reason: "landing coordination state, recomputed"},
	"branch_authority":              {Class: MoveExcluded, Reason: "live branch coordination"},
	"branch_publications":           {Class: MoveExcluded, Reason: "live publication coordination"},
	"task_attempts":                 {Class: MoveExcluded, Reason: "live task coordination"},
	"contract_snapshots":            {Class: MoveExcluded, Reason: "derived from the repository and recomputed"},
	"project_deletions":             {Class: MoveExcluded, Reason: "this forge's deletion records"},
	"project_lifecycle":             {Class: MoveExcluded, Reason: "where the project stands on this forge (being imported, deleted); a moved project arrives active"},
	"system_bindings":               {Class: MoveExcluded, Reason: "authority on this forge; nobody gains authority from a move"},
	"inference_requests":            {Class: MoveExcluded, Reason: "per-request diagnostics; spend moves as token_usage"},
	"git_pull_tokens":               {Class: MoveExcluded, Reason: "credentials never move"},
	"authorization_decisions":       {Class: MoveExcluded, Reason: "authority on this forge; nobody gains authority from a move"},
	"complaints":                    {Class: MoveExcluded, Reason: "about this forge"},
	"conformance_adjudications":     {Class: MoveExcluded, Reason: "this forge's judgments; the receiving forge judges fresh"},
	"conformance_fixes":             {Class: MoveExcluded, Reason: "this forge's judgments"},
	"conformance_review_exceptions": {Class: MoveExcluded, Reason: "an exception is a decision with authority on this forge"},
	"conformance_verdicts":          {Class: MoveExcluded, Reason: "this forge's judgments"},
	"dispositions":                  {Class: MoveExcluded, Reason: "this forge's judgments"},
	"what_if_rows":                  {Class: MoveExcluded, Reason: "this forge's previews of a pack change"},
	"remediation_items":             {Class: MoveExcluded, Reason: "this forge's remediation of its own judgments"},
	"remediation_attempts":          {Class: MoveExcluded, Reason: "this forge's remediation of its own judgments"},
	"eval_arms":                     {Class: MoveExcluded, Reason: "evals and their copies are throwaways that never move"},
	"eval_copies":                   {Class: MoveExcluded, Reason: "evals and their copies are throwaways that never move"},
	"secret_profiles":               {Class: MoveExcluded, Reason: "secret delivery settings on this forge"},
	"owned_repositories":            {Class: MoveExcluded, Reason: "repositories this forge created on its targets"},
	"preview_owned":                 {Class: MoveExcluded, Reason: "previews this forge created"},
	"operations":                    {Class: MoveExcluded, Reason: "this forge's long actions and their receipts"},
	"rehearsal_places":              {Class: MoveExcluded, Reason: "restore rehearsals on this forge's targets"},
	"project_main":                  {Class: MoveExcluded, Reason: "the receiving forge records its own main when the import places the repository; refs.json carries the commit"},
	"deployment_attempts":           {Class: MoveExcluded, Reason: "deploys to this forge's targets"},
	"deployment_attempt_inputs":     {Class: MoveExcluded, Reason: "deploys to this forge's targets"},
	"deployment_owned":              {Class: MoveExcluded, Reason: "resources this forge created on its targets"},
	"deployment_overrides":          {Class: MoveExcluded, Reason: "operators' overrides of deployments on this forge's targets"},
	"deployment_limit_events":       {Class: MoveExcluded, Reason: "what this forge's hosts reported of its deployments' containers"},
	"need_instances":                {Class: MoveExcluded, Reason: "provisioned on this forge's targets"},
	"reconciliations":               {Class: MoveExcluded, Reason: "observations of this forge's targets"},
	"release_bundles":               {Class: MoveExcluded, Reason: "built images and bundles in this forge's registry"},
	"release_requests":              {Class: MoveExcluded, Reason: "release admission state on this forge"},
}

// notMoved is every table the file does not import, and why, for the
// operator's report.
func notMoved() []MoveClass {
	var out []MoveClass
	for table, c := range moveRegistry {
		if c.Class != MoveExported {
			c.Table = table
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Table < out[j].Table })
	return out
}
