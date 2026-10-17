package adoption

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids"
	yamlv3 "gopkg.in/yaml.v3"
)

func dockerLane(name, with string) string {
	return "apiVersion: forge.flokkit.io/v1\nkind: Workflow\nmetadata:\n  name: " + name +
		"\nspec:\n  kind: pipeline\n  role: working\n  stages:\n    - name: work\n      steps:\n        - name: build\n          uses: agent/pi\n          with:\n            prompt: do it\n" + with
}

const oldRule = "            skills: [skill/implementation]\n"

// The one-time migration declares private Docker where skill/implementation
// gave it: in the library's stock and custom workflows and in the versions a
// project's current adoption pins, as one attributed revision per project. An
// explicit docker key, true or false, is left alone, and a second run changes
// nothing.
func TestPrivateDockerIsDeclaredOnceInLibraryAndAdoptions(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	stock := fstest.MapFS{"quick.yaml": {Data: []byte(dockerLane("quick", oldRule))}}
	ways := wow.New(db, wow.Dependencies{Stock: stock})
	ways.SyncStockWorkflows(t.Context())
	as := New(db, Dependencies{Ways: ways})
	put := func(src string) {
		t.Helper()
		if _, _, err := ways.PublishWorkflow(t.Context(), src, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	put(dockerLane("custom", oldRule))
	put(dockerLane("off", oldRule+"            docker: false\n"))
	put(dockerLane("on", oldRule+"            docker: true\n"))
	id := adoptionTestProject(t, db, "p", "P")
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		if err := InitializeIn(t.Context(), tx, id, 0, "", ""); err != nil {
			return err
		}
		return RecordIn(t.Context(), tx, id, 1, AdoptionCreate, "fixture", EmptyReceipt(), "tester", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"quick", "custom", "off", "on"} {
		if err := as.SelectLane(t.Context(), id, name, true, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	// The release now ships quick declared (v2), and alice edits custom (v2)
	// without declaring it; the project stays on v1 of both.
	stock["quick.yaml"] = &fstest.MapFile{Data: []byte(dockerLane("quick", oldRule+"            docker: true\n"))}
	ways.SyncStockWorkflows(t.Context())
	put(dockerLane("custom", "            timeoutMinutes: 5\n"+oldRule))

	if err := as.DeclarePrivateDocker(t.Context(), ways); err != nil {
		t.Fatal(err)
	}
	pins := func() map[string]int {
		cur, err := as.CurrentReceipt(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int{}
		for _, w := range cur.Receipt.Workflows {
			out[w.Name] = w.Version
		}
		return out
	}
	// quick v1 is amended to the release's v2, which reads the same; custom's
	// latest v2 becomes v3, and the project's v1 is amended as v4.
	if got := pins(); got["quick"] != 2 || got["custom"] != 4 || got["off"] != 1 || got["on"] != 1 {
		t.Fatalf("pins after the migration: %v", got)
	}
	declared := func(name string, version int) bool {
		t.Helper()
		w, err := ways.Workflow(t.Context(), wow.WorkflowRef{Name: name, Version: version})
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(w.YAML, "docker: true")
	}
	if !declared("custom", 3) || !declared("custom", 4) || declared("custom", 1) {
		t.Fatal("custom's amended versions do not declare private Docker, or v1 was rewritten")
	}
	if w, _ := ways.Workflow(t.Context(), wow.WorkflowRef{Name: "custom", Version: 4}); strings.Contains(w.YAML, "timeoutMinutes") {
		t.Fatal("the project's v1 was amended from alice's later edit")
	}
	latest, _ := ways.Workflows(t.Context())
	for _, w := range latest {
		if w.Name == "custom" && w.Version != 3 {
			t.Fatalf("custom's latest is v%d, not its amended latest v3", w.Version)
		}
	}
	history, err := as.History(t.Context(), id, 50)
	if err != nil {
		t.Fatal(err)
	}
	last := history[0]
	if last.CreatedBy != MigrationActor || last.Kind != AdoptionEdit || !strings.Contains(last.Reason, "custom v1→v4") || !strings.Contains(last.Reason, "quick v1→v2") {
		t.Fatalf("the migration's revision: %+v", last)
	}
	// The recorded revision is what the adoption resolves to: no drift.
	cur, _ := as.CurrentReceipt(t.Context(), id)
	if union, err := as.Union(t.Context(), id, cur); err != nil || Digest(union) != cur.Digest {
		t.Fatalf("the migrated receipt differs from its resolution: %v\n%s", err, Diff(cur.Receipt, union))
	}

	versions, _ := ways.WorkflowHistory(t.Context(), "custom")
	if err := as.DeclarePrivateDocker(t.Context(), ways); err != nil {
		t.Fatal(err)
	}
	again, _ := as.History(t.Context(), id, 50)
	after, _ := ways.WorkflowHistory(t.Context(), "custom")
	if len(again) != len(history) || len(after) != len(versions) {
		t.Fatalf("a second run recorded %d revision(s) and %d version(s)", len(again)-len(history), len(after)-len(versions))
	}
	// An edit after an amendment of an older version follows every version.
	if _, v, err := ways.PublishWorkflow(t.Context(), dockerLane("custom", oldRule+"            docker: true\n"), "alice"); err != nil || v != 5 {
		t.Fatalf("the next edit of custom is v%d: %v", v, err)
	}
}

// An agent step whose with: is an alias gets the declaration on its own copy
// of the mapping: the alias's other uses are unchanged, the parsed step
// declares it, and a second startup adds neither versions nor revisions.
func TestPrivateDockerIsDeclaredOnceOnAnAliasedWith(t *testing.T) {
	t.Parallel()
	src := strings.Replace(dockerLane("aliased", ""), "spec:\n", "x-agent: &implementation\n  prompt: do it\n  skills: [skill/implementation]\nspec:\n", 1)
	src = strings.Replace(src, "          with:\n            prompt: do it\n", "          with: *implementation\n", 1)
	if _, err := schema.ParseWorkflow([]byte(src)); err != nil {
		t.Fatal(err)
	}
	got, changed, err := declarePrivateDocker(src)
	if err != nil || !changed {
		t.Fatalf("migration: changed=%v err=%v", changed, err)
	}
	doc, err := schema.ParseWorkflow([]byte(got))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Spec.Stages[0].Steps[0].With["docker"] != true {
		t.Errorf("aliased step still has no docker declaration: %v", doc.Spec.Stages[0].Steps[0].With)
	}
	var raw struct {
		Agent map[string]any `yaml:"x-agent"`
	}
	if err := yamlv3.Unmarshal([]byte(got), &raw); err != nil || raw.Agent["docker"] != nil || raw.Agent["prompt"] != "do it" {
		t.Errorf("the anchored mapping's other uses changed: %v %v", raw.Agent, err)
	}
	if _, changed, err := declarePrivateDocker(got); err != nil || changed {
		t.Errorf("second migration changed=%v err=%v; must converge", changed, err)
	}

	db := testDB(t)
	ways := wow.New(db, wow.Dependencies{})
	if _, _, err := ways.PublishWorkflow(t.Context(), src, "alice"); err != nil {
		t.Fatal(err)
	}
	as := New(db, Dependencies{Ways: ways})
	id := adoptionTestProject(t, db, "aliased", "Aliased")
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		if err := InitializeIn(t.Context(), tx, id, 0, "", ""); err != nil {
			return err
		}
		return RecordIn(t.Context(), tx, id, 1, AdoptionCreate, "fixture", EmptyReceipt(), "tester", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	if err := as.SelectLane(t.Context(), id, "aliased", true, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := as.DeclarePrivateDocker(t.Context(), ways); err != nil {
		t.Fatal(err)
	}
	history, _ := as.History(t.Context(), id, 50)
	versions, _ := ways.WorkflowHistory(t.Context(), "aliased")
	if len(versions) != 2 || history[0].CreatedBy != MigrationActor {
		t.Fatalf("the first startup: %d version(s), last revision by %s", len(versions), history[0].CreatedBy)
	}
	if err := as.DeclarePrivateDocker(t.Context(), ways); err != nil {
		t.Fatal(err)
	}
	again, _ := as.History(t.Context(), id, 50)
	after, _ := ways.WorkflowHistory(t.Context(), "aliased")
	if len(again) != len(history) || len(after) != len(versions) {
		t.Fatalf("a second startup recorded %d revision(s) and %d version(s)", len(again)-len(history), len(after)-len(versions))
	}
}

// A project's pack publications move with the migration: a later adoption
// that keeps the pack, whether the project selects its latest or an older
// publication, keeps the declared workflow. Old publications are kept, the
// move is one revision by MigrationActor, and a second startup adds nothing.
func TestAPackKeepsPrivateDockerDeclaredInTheNextAdoption(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ways := wow.New(db, wow.Dependencies{})
	if _, _, err := ways.PublishWorkflow(t.Context(), dockerLane("build", oldRule), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := ways.PublishPack(t.Context(), "team", "", `{"workflows":["build"]}`, ""); err != nil {
		t.Fatal(err)
	}
	as := New(db, Dependencies{Ways: ways})
	adopt := func(slug string, pack int64) ids.ID {
		t.Helper()
		id := adoptionTestProject(t, db, slug, slug)
		w, err := ways.Workflow(t.Context(), wow.WorkflowRef{Name: "build", Version: int(pack)})
		if err != nil {
			t.Fatal(err)
		}
		receipt := EmptyReceipt()
		receipt.Pack = "team"
		receipt.Workflows = []ReceiptWorkflow{{Name: "build", Version: int(pack), Origin: fmt.Sprintf("lib/build@%d", pack), YAMLHash: HashText(w.YAML)}}
		c := Candidate{ProjectID: id, Packs: []PackPick{{Pack: "team", Version: pack}}, Receipt: receipt, Digest: Digest(receipt)}
		// The adoption transaction owner, as a person's adoption settles.
		if err := db.InTx(context.Background(), func(tx *database.Tx) error {
			if err := InitializeIn(t.Context(), tx, id, 0, "", ""); err != nil {
				return err
			}
			current, err := CurrentForUpdateIn(context.Background(), tx, id)
			if err != nil {
				return err
			}
			_, err = adoptIn(context.Background(), tx, current, c, 0, "alice", "fixture", nil, nil, time.Now())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if _, _, err := ways.PublishWorkflow(t.Context(), dockerLane("build", "            timeoutMinutes: 5\n"+oldRule), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := ways.CutPacksOnPinChange(t.Context()); err != nil {
		t.Fatal(err)
	}
	latest := adopt("latest", 2) // team v2 pins build v2
	older := adopt("older", 1)   // team v1 pins build v1

	if err := as.DeclarePrivateDocker(t.Context(), ways); err != nil {
		t.Fatal(err)
	}
	p, err := ways.Pack(t.Context(), "team")
	if err != nil {
		t.Fatal(err)
	}
	publications, _ := ways.PackPublications(t.Context(), p.ID)
	// build v2 (latest) → v3, v1 → v4; team v2 (latest) → v3, then team v1
	// → v4 without moving the latest.
	if p.LatestVersion != 3 || len(publications) != 4 {
		t.Fatalf("team is at v%d with %d publications", p.LatestVersion, len(publications))
	}
	// team v1 was the recommended publication and latest was ahead of it:
	// the amendment carries the recommendation to v4, so new projects get
	// what the migrated ones are on, and moves nothing else.
	if h, err := ways.PackHistory(t.Context(), "team"); err != nil || h.Recommended != 4 {
		t.Fatalf("team's recommendation after the migration amended v1: %+v %v", h, err)
	}
	for id, want := range map[ids.ID][3]int64{latest: {2, 3, 3}, older: {1, 4, 4}} { // team from, team to, build

		members, err := ProjectPacksIn(t.Context(), db, id)
		if err != nil || len(members) != 1 || members[0].Version != want[1] || members[0].AdoptedBy != MigrationActor {
			t.Fatalf("project %d's pack: %+v %v", id, members, err)
		}
		history, _ := as.History(t.Context(), id, 50)
		if history[0].CreatedBy != MigrationActor || !strings.Contains(history[0].Reason, fmt.Sprintf("pack team v%d→v%d", want[0], want[1])) {
			t.Fatalf("project %d's revision: %+v", id, history[0])
		}
		candidate, err := as.Prepare(t.Context(), id, Request{})
		if err != nil || len(candidate.Problems) != 0 {
			t.Fatalf("project %d's next candidate: %v %v", id, err, candidate)
		}
		if err := as.validateCandidateIn(context.Background(), db, id, *candidate); err != nil {
			t.Fatal(err)
		}
		if pin := candidate.Receipt.Workflows[0]; pin.Version != int(want[2]) {
			t.Fatalf("project %d's next candidate pins build v%d, want the declared v%d", id, pin.Version, want[2])
		}
	}
	for _, v := range []int64{1, 2} {
		old, err := ways.PackPublication(t.Context(), "team", v)
		if err != nil || !strings.Contains(strings.ReplaceAll(old.PinsJSON, " ", ""), fmt.Sprintf(`"build":%d}`, v)) {
			t.Fatalf("team v%d was rewritten: %+v %v", v, old, err)
		}
	}

	before, _ := as.History(t.Context(), older, 50)
	if err := as.DeclarePrivateDocker(t.Context(), ways); err != nil {
		t.Fatal(err)
	}
	again, _ := ways.PackPublications(t.Context(), p.ID)
	after, _ := as.History(t.Context(), older, 50)
	if len(again) != len(publications) || len(after) != len(before) {
		t.Fatalf("a second startup added %d publication(s) and %d revision(s)", len(again)-len(publications), len(after)-len(before))
	}
	// A project put on team v1 later moves to the publication that reads the
	// same, v4, with no new one.
	late := adopt("late", 1)
	if err := as.DeclarePrivateDocker(t.Context(), ways); err != nil {
		t.Fatal(err)
	}
	members, _ := ProjectPacksIn(t.Context(), db, late)
	if again, _ = ways.PackPublications(t.Context(), p.ID); len(again) != len(publications) || len(members) != 1 || members[0].Version != 4 {
		t.Fatalf("the late project is on %+v; %d publication(s) added", members, len(again)-len(publications))
	}
}

// Finishing the earlier workflow-only migration moves only the pack
// membership; the move is kept in revision history, attributed, even though
// the receipt is equal. A replay records nothing.
func TestAMembershipOnlyMigrationRecordsOneRevision(t *testing.T) {
	db := testDB(t)
	ways := wow.New(db, wow.Dependencies{})
	if _, _, err := ways.PublishWorkflow(t.Context(), dockerLane("build", oldRule), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := ways.PublishPack(t.Context(), "team", "", `{"workflows":["build"]}`, ""); err != nil {
		t.Fatal(err)
	}
	as := New(db, Dependencies{Ways: ways})
	id := adoptionTestProject(t, db, "review-membership", "Review membership")
	w, err := ways.Workflow(t.Context(), wow.WorkflowRef{Name: "build", Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	r := EmptyReceipt()
	r.Pack = "team"
	r.Workflows = []ReceiptWorkflow{{Name: "build", Version: 1, Origin: "lib/build@1", YAMLHash: HashText(w.YAML)}}
	c := Candidate{ProjectID: id, Packs: []PackPick{{Pack: "team", Version: 1}}, Receipt: r, Digest: Digest(r)}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		if err := InitializeIn(t.Context(), tx, id, 0, "", ""); err != nil {
			return err
		}
		current, err := CurrentForUpdateIn(context.Background(), tx, id)
		if err != nil {
			return err
		}
		_, err = adoptIn(context.Background(), tx, current, c, 0, "alice", "fixture", nil, nil, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Reproduce the previous implementation's supported owner operations:
	// amend the workflow and advance the receipt, leaving the pack selected.
	declared, _, err := declarePrivateDocker(w.YAML)
	if err != nil {
		t.Fatal(err)
	}
	v, err := ways.AmendWorkflow(t.Context(), "build", 1, declared, MigrationActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := as.Advance(t.Context(), id, AdoptionEdit, "earlier workflow-only migration", MigrationActor, func(_ *database.Tx, prior *RecordedRevision, _ time.Time) (Receipt, error) {
		next := cloneReceipt(prior.Receipt)
		next.Workflows[0] = ReceiptWorkflow{Name: "build", Version: v, Origin: fmt.Sprintf("lib/build@%d", v), YAMLHash: HashText(declared)}
		return next, nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := as.History(t.Context(), id, 50)
	if err != nil {
		t.Fatal(err)
	}
	if err := as.DeclarePrivateDocker(t.Context(), ways); err != nil {
		t.Fatal(err)
	}
	members, err := ProjectPacksIn(t.Context(), db, id)
	if err != nil || len(members) != 1 || members[0].Version == 1 {
		t.Fatalf("membership did not move: %+v %v", members, err)
	}
	after, err := as.History(t.Context(), id, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("pack moved from v1 to v%d, but history remains %d revisions (was %d)", members[0].Version, len(after), len(before))
	}
	if after[0].CreatedBy != MigrationActor || !strings.Contains(after[0].Reason, "pack team v1→v") {
		t.Fatalf("the membership move's revision is not attributed with its reason: %+v", after[0])
	}
	// Replay: the next start finds nothing to move and records nothing.
	if err := as.DeclarePrivateDocker(t.Context(), ways); err != nil {
		t.Fatal(err)
	}
	if again, err := as.History(t.Context(), id, 50); err != nil || len(again) != len(after) {
		t.Fatalf("replay recorded %d revisions after %d: %v", len(again), len(after), err)
	}
}
