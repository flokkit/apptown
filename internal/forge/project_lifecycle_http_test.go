package forge

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/consoletest"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/operations"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/usage"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
)

// Project lifecycle over HTTP: slug law, name defaulting, conflict, patch,
// and the audit trail behind each act.
func TestProjectLifecycleHTTP(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{DockerClient: newFakeDocker()})
	testAdministrator(t, a, "bls")

	h := a.Handler()

	if w := postJSON(h, "/api/v1/projects", map[string]string{"slug": "Bad Slug"}); w.Code != 400 {
		t.Fatalf("bad slug accepted: %d", w.Code)
	}
	w := postJSON(h, "/api/v1/projects", map[string]string{"slug": "egg-catch"})
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"name":"egg-catch"`) {
		t.Fatalf("create (name defaults to slug): %d %s", w.Code, w.Body.String())
	}
	if w := postJSON(h, "/api/v1/projects", map[string]string{"slug": "egg-catch"}); w.Code != 409 {
		t.Fatalf("duplicate slug must 409: %d", w.Code)
	}
	req := httptest.NewRequest("GET", "/api/v1/projects/egg-catch", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("get: %d", rec.Code)
	}
	if audits := auditCount(t, a.DB, "project.created"); audits != 1 {
		t.Fatalf("project creation must be audited: %d", audits)
	}
	if st := deleteProjectAs(t, a, "bls", "egg-catch"); st.State != project.Deleted {
		t.Fatalf("delete: %+v", st)
	}
	if op := purgeProjectAs(t, a, "bls", "egg-catch"); op.State != operations.Succeeded {
		t.Fatalf("purge: %s %q", op.State, op.Detail)
	}
	if _, err := a.Projects.BySlug(context.Background(), "egg-catch"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted project is still found: %v", err)
	}
	deleted := 0
	for _, entry := range auditEntries(t, a.DB, "project.deleted") {
		if entry.Subject == "egg-catch" {
			deleted++
		}
	}
	if deleted != 1 {
		t.Fatalf("project deletion receipts: %d", deleted)
	}
}

// exportWorld is a project with history to move: the stock workflows, a
// change shipped by a person who may land, chats, and an ADR.
type exportWorld struct {
	a     *App
	agent *consoletest.Agent
	boss  string // an administrator's command-line token
}

func newExportWorld(t *testing.T) exportWorld {
	t.Helper()
	agent := consoletest.NewAgent()
	a, _ := mcpWriteFixture(t, Config{DockerClient: newFakeDocker(), ConsoleCLI: fixtureCLI(t), ConsoleAgent: agent,
		InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
	configureConsoleModel(t, a)
	a.Ways.SyncStockWorkflows(t.Context())
	p, err := a.Projects.BySlug(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.recordAdoption(&p, "edit", "stock fixture", "fixture"); err != nil {
		t.Fatal(err)
	}
	return exportWorld{a: a, agent: agent, boss: tokenFor(t, a, "boss-cli", "boss", "admin", access.TokenCLI)}
}

func (w exportWorld) call(t *testing.T, method, path, body string, want int) []byte {
	t.Helper()
	res := bearerCall(w.a, w.boss, method, path, body)
	if res.Code != want {
		t.Fatalf("%s %s: %d %s", method, path, res.Code, res.Body.String())
	}
	return res.Body.Bytes()
}

// shipChange makes a change with one commit and ships it as a person who
// may land.
func (w exportWorld) shipChange(t *testing.T) *workflow.Change {
	t.Helper()
	p, _ := w.a.Projects.BySlug(context.Background(), "app")
	ch := engineChange(t, w.a, &p)
	ws := filepath.Join(w.a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	if err := os.WriteFile(filepath.Join(ws, "agent-work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, ws, "add", "agent-work.txt")
	gittest.Run(t, ws, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "agent work")
	w.call(t, "POST", fmt.Sprintf("/api/v1/changes/%d/runs", ch.ID), `{"workflow":"ship"}`, 201)
	w.a.Work.Wait()
	got, err := w.a.Work.Change(w.a.asForge(context.Background(), p.ID, "test"), ch.ID)
	if err != nil || got.State != workflow.ChangeShipped {
		t.Fatalf("the shipped change: %+v %v", got, err)
	}
	return got
}

// chat opens a chat (on a change when changeID is not 0), says text when
// given, and returns its id; end ends it.
func (w exportWorld) chat(t *testing.T, changeID ids.ID, text string) string {
	t.Helper()
	var out struct{ Session struct{ ID string } }
	if err := json.Unmarshal(w.call(t, "POST", "/api/v1/projects/app/console", fmt.Sprintf(`{"model":"fake/model","changeId":%q}`, changeID), 201), &out); err != nil {
		t.Fatal(err)
	}
	if text != "" {
		w.call(t, "POST", "/api/v1/console/"+out.Session.ID+"/say", fmt.Sprintf(`{"text":%q}`, text), 200)
		w.agent.Reply(out.Session.ID, "done: "+text)
	}
	return out.Session.ID
}

func (w exportWorld) end(t *testing.T, id string) {
	t.Helper()
	w.call(t, "DELETE", "/api/v1/console/"+id, "", 200)
}

// readExport reads a move file's entries in order.
func readExport(t *testing.T, raw []byte) ([]string, map[string][]byte) {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(raw))
	var order []string
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		order = append(order, h.Name)
		files[h.Name] = b
	}
	return order, files
}

// plantCommit writes a commit holding path=body on top of base straight into
// the project's repository and returns it: history no owner operation would
// make, which the export must still never carry.
func plantCommit(t *testing.T, hub, base, path, body string) string {
	t.Helper()
	git := func(env []string, stdin string, args ...string) string {
		cmd := gittest.Command(append([]string{"-C", hub}, args...)...)
		cmd.Env = append(cmd.Env, env...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	index := []string{"GIT_INDEX_FILE=" + filepath.Join(t.TempDir(), "index"), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}
	blob := git(nil, body, "hash-object", "-w", "--stdin")
	git(index, "", "read-tree", base)
	git(index, "", "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path)
	tree := git(index, "", "write-tree")
	return git(index, "", "commit-tree", tree, "-p", base, "-m", "planted")
}

// heldRun starts a run on a change that waits on a person's approval: a run
// still running.
func heldRun(t *testing.T, a *App, projectID, changeID ids.ID) ids.ID {
	t.Helper()
	held := `
apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: { name: held }
spec:
  kind: pipeline
  role: working
  stages:
    - name: wait
      steps:
        - name: accept
          uses: approval
          with: { means: "I accept this work" }
`
	if _, _, err := a.Ways.PublishWorkflow(t.Context(), held, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := a.lifecycle.SelectLane(t.Context(), projectID, "held", true, "fixture"); err != nil {
		t.Fatal(err)
	}
	run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: changeID, Workflow: "held"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	return run.ID
}

// A project moves as one file: its repository's refs in a bundle, every
// owner's rows, its kept transcripts and its chats, with a manifest last
// that names every other entry's hash. Chats move as their record (without
// the agent's credential or this host's paths), words and pictures; a chat
// with no words is counted, not moved; no credential is in the file; and
// the export is recorded with the file's hash once it has been sent.
func TestAProjectExportsWithItsHistoryAndChats(t *testing.T) {
	t.Parallel()
	w := newExportWorld(t)
	a := w.a
	p, _ := a.Projects.BySlug(context.Background(), "app")
	shipped := w.shipChange(t)
	// A change with a chat, abandoned, and an open change with a run still
	// running: they stay, listed, and do not refuse the move.
	abandoned := engineChange(t, a, &p)
	changeChat := w.chat(t, abandoned.ID, "try the other colour")
	w.end(t, changeChat)
	// It was previewed before it was abandoned. The preview needs a running
	// app this fixture has none of, so its receipt is recorded through the
	// ledger's owner as the preview door records it: a bare change id.
	if err := a.DB.InTx(context.Background(), func(tx *database.Tx) error {
		return audit.At(time.Now()).RecordIn(context.Background(), tx, "boss", p.ID, "change.preview", fmt.Sprintf("%d", abandoned.ID), map[string]any{"url": "http://private-skipped-preview"})
	}); err != nil {
		t.Fatal(err)
	}
	w.call(t, "POST", fmt.Sprintf("/api/v1/changes/%d/abandon", abandoned.ID), `{"reason":"private-abandonment-detail"}`, 200)
	projectChat := w.chat(t, ids.Nil, "what does this do")
	picture := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	w.call(t, "POST", "/api/v1/console/"+projectChat+"/upload", fmt.Sprintf(`{"data":%q}`, picture), 200)
	chatMeta, err := a.Console.Recorded(projectChat)
	if err != nil || chatMeta.Token == "" {
		t.Fatalf("the chat's record: %+v %v", chatMeta, err)
	}
	w.end(t, projectChat)
	empty := w.chat(t, ids.Nil, "")
	w.end(t, empty)
	open := engineChange(t, a, &p)
	running := heldRun(t, a, p.ID, open.ID)
	live := w.chat(t, ids.Nil, "still talking")
	// A refused save's work holding an agent's credential, kept for its
	// download, as repositoryedit.SaveChat keeps one; the chat's save record
	// names it.
	hub := a.projectRepoPath("app")
	credential := plantCommit(t, hub, "main", ".forge/pi-home/auth.json", "planted-credential-7f3a")
	gittest.Run(t, hub, "update-ref", "refs/forge/refused/chat-"+projectChat, credential)
	if err := repositoryedit.RecordChatSave(filepath.Join(a.cfg.dataDir, "console", projectChat), repositoryedit.ChatSave{Outcome: "refused", Reason: "a .forge path", Kept: "refs/forge/refused/chat-" + projectChat}); err != nil {
		t.Fatal(err)
	}
	w.call(t, "POST", "/api/v1/projects/app/adrs", `{"title":"Use a canvas"}`, 201)
	// Spend of a chat that moves and of one that stays.
	for _, session := range []string{projectChat, changeChat} {
		if err := a.Usage.Ledger.Record(context.Background(), usage.Entry{Kind: "console", ProjectID: p.ID, SessionID: session, Actor: "boss", Provider: "fake", Model: "m"}, usage.Totals{Total: 10, CostMicros: 5}, ""); err != nil {
			t.Fatal(err)
		}
	}
	pullToken, err := a.Access.RuntimePullToken(context.Background(), p.ID)
	if err != nil || pullToken == "" {
		t.Fatalf("pull token: %v", err)
	}

	if res := bearerCall(a, tokenFor(t, a, "viewer-cli", "viewer", "member", access.TokenCLI), "GET", "/api/v1/projects/app/export", ""); res.Code != 403 {
		t.Fatalf("a non-administrator exported: %d", res.Code)
	}
	res := bearerCall(a, w.boss, "GET", "/api/v1/projects/app/export", "")
	if res.Code != 200 {
		t.Fatalf("export: %d %s", res.Code, res.Body.String())
	}
	raw := res.Body.Bytes()
	sum := sha256.Sum256(raw)
	if hash := hex.EncodeToString(sum[:]); res.Header().Get("X-Forge-Export-Sha256") != hash {
		t.Fatalf("the header's hash %s is not the file's %s", res.Header().Get("X-Forge-Export-Sha256"), hash)
	}
	order, files := readExport(t, raw)
	if order[len(order)-1] != "manifest.json" {
		t.Fatalf("the manifest is not last: %v", order)
	}
	var manifest projectlifecycle.Manifest
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Format != projectlifecycle.ExportFormat || manifest.Version != projectlifecycle.ExportVersion || manifest.Slug != "app" || manifest.ExportedBy == "" || manifest.Source.Schema == 0 || manifest.Source.Forge.IsZero() {
		t.Fatalf("manifest: %+v", manifest)
	}
	if len(manifest.Entries) != len(order)-1 {
		t.Fatalf("the manifest names %d entries, the file holds %d", len(manifest.Entries), len(order)-1)
	}
	for i, e := range manifest.Entries {
		got := sha256.Sum256(files[e.Path])
		if e.Path != order[i] || e.Size != int64(len(files[e.Path])) || e.SHA256 != hex.EncodeToString(got[:]) {
			t.Fatalf("entry %d: manifest %+v, file %s", i, e, order[i])
		}
	}

	// The bundle carries every ref the repository holds but the transient
	// ones, at the same commits.
	bundle := filepath.Join(t.TempDir(), "repository.bundle")
	if err := os.WriteFile(bundle, files["repository.bundle"], 0o600); err != nil {
		t.Fatal(err)
	}
	heads := strings.Fields(gittest.Run(t, filepath.Dir(bundle), "bundle", "list-heads", bundle))
	// The runs that move are the work document's; their branches move with
	// them, and the running run's does not.
	var moved struct{ Runs []struct{ ID ids.ID } }
	if err := json.Unmarshal(files["owners/work.json"], &moved); err != nil {
		t.Fatal(err)
	}
	runBranches := map[string]bool{}
	for _, r := range moved.Runs {
		if r.ID == running {
			t.Fatalf("the running run %d moved", running)
		}
		runBranches[fmt.Sprintf("refs/heads/forge/run-%d", r.ID)] = true
	}
	var want []string
	for _, line := range strings.Split(gittest.Run(t, hub, "for-each-ref", "--format=%(objectname) %(refname)"), "\n") {
		ref := strings.Fields(line)[1]
		if ref == "refs/heads/"+p.DefaultBranch || ref == "refs/heads/"+shipped.Branch || runBranches[ref] {
			want = append(want, strings.Fields(line)...)
		}
	}
	slices.Sort(heads)
	slices.Sort(want)
	if !slices.Equal(heads, want) {
		t.Fatalf("bundle heads %v, repository refs %v", heads, want)
	}
	// It stands alone: a repository made from it alone holds every commit.
	clone := filepath.Join(t.TempDir(), "clone.git")
	if out, err := gittest.Command("clone", "-q", "--mirror", bundle, clone).CombinedOutput(); err != nil {
		t.Fatalf("the bundle does not make a repository: %v %s", err, out)
	}
	gittest.Run(t, clone, "fsck", "--strict")
	// Refused work never leaves: neither its ref nor what it held.
	if err := gittest.Command("-C", clone, "cat-file", "-e", credential).Run(); err == nil {
		t.Fatal("the refused work holding a credential is in the bundle")
	}

	// Work: both changes, closed.
	var work workflow.ProjectExport
	if err := json.Unmarshal(files["owners/work.json"], &work); err != nil {
		t.Fatal(err)
	}
	states := map[ids.ID]string{}
	for _, row := range work.Changes {
		var c struct {
			ID    ids.ID
			State string
		}
		_ = json.Unmarshal(row, &c)
		states[c.ID] = c.State
	}
	if states[shipped.ID] != "shipped" || len(states) != 1 || manifest.Counts["changes"] != 1 || len(work.Runs) == 0 || len(work.Steps) == 0 {
		t.Fatalf("work: changes %v, runs %d, steps %d", states, len(work.Runs), len(work.Steps))
	}
	// The library versions the current selection uses travel with their
	// content: numbering is each forge's own.
	var library []struct {
		Kind    string
		Content struct {
			Item    struct{ Name string }
			Version struct {
				Version int64
				YAML    string `json:"yaml"`
			}
		}
	}
	if err := json.Unmarshal(files["owners/library.json"], &library); err != nil {
		t.Fatal(err)
	}
	carried := map[string]bool{}
	for _, v := range library {
		if v.Kind == "workflow" && v.Content.Version.YAML != "" {
			carried[fmt.Sprintf("%s@%d", v.Content.Item.Name, v.Content.Version.Version)] = true
		}
	}
	var adoptionDoc struct {
		Revisions []struct {
			Receipt struct {
				Workflows []struct {
					Name    string
					Version int64
				}
			} `json:"receipt_json"`
		}
	}
	if err := json.Unmarshal(files["owners/adoption.json"], &adoptionDoc); err != nil || len(adoptionDoc.Revisions) == 0 {
		t.Fatalf("adoption: %v", err)
	}
	current := adoptionDoc.Revisions[len(adoptionDoc.Revisions)-1].Receipt.Workflows
	if len(current) == 0 {
		t.Fatal("the fixture's selection has no workflows")
	}
	for _, wf := range current {
		if !carried[fmt.Sprintf("%s@%d", wf.Name, wf.Version)] {
			t.Fatalf("workflow %s@%d is selected but its content did not travel: %v", wf.Name, wf.Version, carried)
		}
	}
	// What stays is listed, not refused.
	skipped := map[string]bool{}
	for _, sk := range manifest.Skipped {
		skipped[fmt.Sprintf("%s %d", sk.Kind, sk.ID)] = true
	}
	if !skipped[fmt.Sprintf("change %d", abandoned.ID)] || !skipped[fmt.Sprintf("change %d", open.ID)] || !skipped[fmt.Sprintf("run %d", running)] || len(manifest.Skipped) != 3 {
		t.Fatalf("skipped: %+v", manifest.Skipped)
	}
	stayedChats := map[string]string{}
	for _, c := range manifest.SkippedChats {
		stayedChats[c.ID] = c.Why
	}
	if stayedChats[changeChat] != "change-not-moved" || stayedChats[live] != "live" || len(stayedChats) != 2 {
		t.Fatalf("chats that stay: %+v", manifest.SkippedChats)
	}
	if manifest.Counts["adrs"] != 1 || manifest.Counts["ledger"] == 0 || manifest.Counts["adoptionRevisions"] == 0 {
		t.Fatalf("counts: %v", manifest.Counts)
	}

	// Chats: the two with words, as their record, words and pictures; the
	// empty one counted.
	if manifest.EmptyChats != 1 || manifest.Counts["chats"] != 1 {
		t.Fatalf("chats: %d moved, %d empty", manifest.Counts["chats"], manifest.EmptyChats)
	}
	for _, id := range []string{projectChat} {
		var meta console.Meta
		if err := json.Unmarshal(files["chats/"+id+"/"+console.EndedFile], &meta); err != nil {
			t.Fatalf("chat %s's record: %v", id, err)
		}
		if meta.Token != "" || meta.Checkout != "" || meta.Actor != "boss" || meta.ID != id {
			t.Fatalf("chat %s's record carries a credential or a host path, or lost its speaker: %+v", id, meta)
		}
		var words bool
		for name := range files {
			words = words || (strings.HasPrefix(name, "chats/"+id+"/sessions/") && strings.HasSuffix(name, ".jsonl"))
		}
		if !words {
			t.Fatalf("chat %s moved without its words", id)
		}
	}
	var saved map[string]any
	if err := json.Unmarshal(files["chats/"+projectChat+"/"+console.SavedFile], &saved); err != nil || saved["kept"] != nil || saved["keptOnSource"] != "refs/forge/refused/chat-"+projectChat {
		t.Fatalf("the save record still points at refused work as carried: %v %v", saved, err)
	}
	if _, ok := files["chats/"+projectChat+"/uploads/picture-1.png"]; !ok {
		t.Fatalf("the pasted picture did not move: %v", order)
	}
	for name := range files {
		if strings.HasPrefix(name, "chats/"+empty+"/") || strings.HasPrefix(name, "chats/"+changeChat+"/") || strings.HasPrefix(name, "chats/"+live+"/") {
			t.Fatalf("the empty chat moved: %s", name)
		}
		if strings.Contains(name, "/scratch/") || strings.HasSuffix(name, "/steering.md") {
			t.Fatalf("a running chat's file moved: %s", name)
		}
	}
	for what, secret := range map[string]string{"the planted credential": "planted-credential-7f3a", "the chat's credential": chatMeta.Token, "the pull token": pullToken, "the administrator's token": w.boss} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("%s is in the file", what)
		}
	}

	// The ledger and spend move with what they are about: the project's own
	// lines and the shipped change's move; the abandoned change's, the
	// skipped chats' and the running run's stay, counted.
	var ledger []struct {
		Kind, Subject, Actor string
		Payload              json.RawMessage `json:"payload_json"`
	}
	if err := json.Unmarshal(files["owners/audit.json"], &ledger); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, l := range ledger {
		kinds[l.Kind+" "+l.Subject] = true
		for _, stays := range []string{changeChat, live, empty, fmt.Sprintf("change %d", abandoned.ID), fmt.Sprintf("change %d", open.ID), fmt.Sprintf("run %d", running)} {
			if strings.HasPrefix(l.Subject, stays) || strings.Contains(l.Actor, stays) || strings.Contains(string(l.Payload), fmt.Sprintf(`"changeId": %q`, abandoned.ID)) {
				t.Fatalf("a ledger line about what stays moved: %+v", l)
			}
		}
	}
	if !kinds[fmt.Sprintf("change.started change %d", shipped.ID)] || !kinds["adr.created ADR-001 Use a canvas"] || !kinds["console.say "+projectChat] || manifest.Counts["ledgerStayed"] == 0 {
		t.Fatalf("the ledger lost what moves: %v, %d stayed", kinds, manifest.Counts["ledgerStayed"])
	}
	if bytes.Contains(raw, []byte("private-abandonment-detail")) || bytes.Contains(raw, []byte("private-skipped-preview")) {
		t.Fatal("the abandoned change's reason is in the file")
	}
	var spend []struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(files["owners/usage.json"], &spend); err != nil {
		t.Fatal(err)
	}
	movedSpend := 0
	for _, row := range spend {
		if row.SessionID == changeChat || row.SessionID == live {
			t.Fatalf("spend of a chat that stays moved: %+v", spend)
		}
		if row.SessionID == projectChat {
			movedSpend++
		}
	}
	if movedSpend == 0 || manifest.Counts["usageStayed"] == 0 {
		t.Fatalf("spend: %+v, %d stayed", spend, manifest.Counts["usageStayed"])
	}
	// The receipt names the file the operator received.
	lines, err := audit.Recent(context.Background(), a.DB, audit.Filter{ProjectID: p.ID, KindPrefix: "project.exported"})
	if err != nil || len(lines) != 1 || !strings.Contains(string(lines[0].Payload), res.Header().Get("X-Forge-Export-Sha256")) {
		t.Fatalf("the export receipt: %+v %v", lines, err)
	}
	if spools, _ := os.ReadDir(filepath.Join(a.cfg.dataDir, "exports")); len(spools) != 0 {
		t.Fatalf("the spool stayed: %v", spools)
	}
}

// A move is refused, naming why, when the file would be wrong or unsafe:
// a chat's record, words or pictures, or a change's attachments, that
// cannot be read, a commit
// the moving records name that the moving refs do not carry, carried
// history holding the forge's runtime files (a lane's committed report file
// is prose and moves), or an eval copy. No refusal is recorded and none
// leaves a file.
func TestAnExportRefusesWhatWouldMakeItWrongAndNamesIt(t *testing.T) {
	t.Parallel()
	w := newExportWorld(t)
	a := w.a
	p, _ := a.Projects.BySlug(context.Background(), "app")
	shipped := w.shipChange(t)
	chat := w.chat(t, ids.Nil, "keep this")
	w.end(t, chat)
	refused := func(want string) {
		t.Helper()
		res := bearerCall(a, w.boss, "GET", "/api/v1/projects/app/export", "")
		var out struct {
			Error   string
			Reasons []string
		}
		if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil || res.Code != 409 {
			t.Fatalf("export: %d %s", res.Code, res.Body.String())
		}
		if !strings.Contains(strings.Join(out.Reasons, "\n"), want) {
			t.Fatalf("the refusal does not name %q: %v", want, out.Reasons)
		}
	}
	unreadable := func(path string) func() {
		t.Helper()
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		// Root opens a file or folder whatever its mode, so the refusal
		// cannot be provoked; CI runs the suite as an ordinary user.
		if f, err := os.Open(path); err == nil {
			_ = f.Close()
			t.Skip("this user reads unreadable files (root)")
		}
		return func() { _ = os.Chmod(path, 0o755) }
	}

	records, _ := filepath.Glob(filepath.Join(a.cfg.dataDir, "console", chat, "sessions", "*.jsonl"))
	if len(records) != 1 {
		t.Fatalf("the chat's record: %v", records)
	}
	restore := unreadable(records[0])
	refused("chat " + chat)
	// The project's past chats say a record could not be read, never that
	// there is nothing: an administrator sees which folder, a reader how many.
	pastChatsSay := func(token string, wantFolder bool) {
		t.Helper()
		res := bearerCall(a, token, "GET", "/api/v1/projects/app/transcript", "")
		var out struct {
			Sessions          []json.RawMessage
			Unreadable        int
			UnreadableFolders []string
		}
		if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil || res.Code != 200 || out.Unreadable != 1 || (len(out.UnreadableFolders) == 1) != wantFolder || wantFolder && out.UnreadableFolders[0] != chat {
			t.Fatalf("past chats with an unreadable record: %d %s", res.Code, res.Body.String())
		}
	}
	pastChatsSay(w.boss, true)
	pastChatsSay(tokenFor(t, a, "viewer-cli", "viewer", "member", access.TokenCLI), false)
	restore()
	if err := os.Chmod(records[0], 0o644); err != nil {
		t.Fatal(err)
	}
	ended := filepath.Join(a.cfg.dataDir, "console", chat, console.EndedFile)
	restore = unreadable(ended)
	refused(chat)
	pastChatsSay(w.boss, true)
	restore()
	if err := os.Chmod(ended, 0o644); err != nil {
		t.Fatal(err)
	}
	uploads := filepath.Join(a.cfg.dataDir, "console", chat, "uploads")
	restore = unreadable(uploads)
	refused("uploads")
	restore()

	// No owner operation attaches a file to a landed change; the folder is
	// written as an upload leaves it.
	attachments := workflow.GitWorkspace{DataDir: a.cfg.dataDir}.AttachmentDir(shipped.ID)
	if err := os.MkdirAll(attachments, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attachments, "brief.txt"), []byte("brief"), 0o644); err != nil {
		t.Fatal(err)
	}
	restore = unreadable(attachments)
	refused(fmt.Sprintf("change %d's attachments", shipped.ID))
	restore()

	// sql-guarantee: no owner operation records a commit the repository does not hold, which is the damage this refusal exists for.
	if _, err := a.DB.SQL().ExecContext(context.Background(), `UPDATE changes SET base_sha = $1 WHERE id = $2`, strings.Repeat("d", 40), shipped.ID); err != nil {
		t.Fatal(err)
	}
	refused("commit " + strings.Repeat("d", 40))
	// sql-guarantee: restores the change's recorded commit the line above replaced.
	if _, err := a.DB.SQL().ExecContext(context.Background(), `UPDATE changes SET base_sha = $1 WHERE id = $2`, shipped.BaseSHA, shipped.ID); err != nil {
		t.Fatal(err)
	}

	// sql-guarantee: no owner operation records a landing whose merge is not on the default branch, which is what this refusal guards; this test's database holds the one landing.
	if res, err := a.DB.SQL().ExecContext(context.Background(), `UPDATE run_merges SET result = $1 WHERE state = 'merged'`, strings.Repeat("e", 40)); err != nil {
		t.Fatal(err)
	} else if n, _ := res.RowsAffected(); n == 0 {
		t.Fatal("the shipped change has no recorded merge")
	}
	refused("landed commit " + strings.Repeat("e", 40))
	// sql-guarantee: restores the merge record the line above replaced, by the default branch's tip it merged.
	if _, err := a.DB.SQL().ExecContext(context.Background(), `UPDATE run_merges SET result = $1 WHERE state = 'merged'`, gittest.Run(t, a.projectRepoPath("app"), "rev-parse", p.DefaultBranch)); err != nil {
		t.Fatal(err)
	}

	// A lane's build report committed on the default branch, as older lanes
	// did: prose, which moves.
	hub := a.projectRepoPath("app")
	report := plantCommit(t, hub, p.DefaultBranch, ".forge/REPORT.md", "# Build report\n")
	gittest.Run(t, hub, "update-ref", "refs/heads/"+p.DefaultBranch, report)
	if res := bearerCall(a, w.boss, "GET", "/api/v1/projects/app/export", ""); res.Code != 200 {
		t.Fatalf("a committed build report refused the export: %d %s", res.Code, res.Body.String())
	}
	// The default branch holding an agent's home, as history from before the
	// publication's source rule could: never carried.
	planted := plantCommit(t, hub, p.DefaultBranch, ".forge/pi-home/auth.json", "planted-credential")
	gittest.Run(t, hub, "update-ref", "refs/heads/"+p.DefaultBranch, planted)
	refused(".forge/pi-home/auth.json")

	evalCopyForTest(t, a, p.ID)
	refused("eval copy")

	lines, err := audit.Recent(context.Background(), a.DB, audit.Filter{ProjectID: p.ID, KindPrefix: "project.exported"})
	if err != nil || len(lines) != 1 {
		t.Fatalf("only the one export that went is recorded: %+v %v", lines, err)
	}
	if spools, _ := os.ReadDir(filepath.Join(a.cfg.dataDir, "exports")); len(spools) != 0 {
		t.Fatalf("a refused export left a file: %v", spools)
	}
}
