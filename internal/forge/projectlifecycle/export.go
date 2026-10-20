package projectlifecycle

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/adrs"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/forge/usage"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/gitexec"
)

// Moving a project to another AppTown, the export half. The file is an
// uncompressed tar: a Git bundle of the project's refs, one document per
// owner, the files kept outside the database, and the manifest last, naming
// every other entry's size and SHA-256. It is as sensitive as this AppTown's
// database: logs, artifacts and chats travel as they are.

// ExportFormat and ExportVersion name the file's shape. There is one
// version. There is no import in 1.0 (it is parked); a future one is to
// refuse any other version, from another build.
// The export is experimental in 1.0 (stock/docs/guides/compatibility.md): a
// copy of a project's records for the same build, not a format promised
// across releases, so the version does not move with the build.
const (
	ExportFormat  = "flokkit.project-export"
	ExportVersion = 1
)

// ErrExportRefused refuses a move that would make the file wrong or unsafe:
// an eval copy, history that cannot be read, a commit the moving records
// name that the moving refs do not carry, or carried history holding the
// forge's runtime files.
var ErrExportRefused = fault.New(fault.Conflict, "the project cannot be exported now")

// ExportRefusal names what keeps a project from moving now.
type ExportRefusal struct{ Reasons []string }

func (e *ExportRefusal) Error() string {
	return "the project cannot be exported now: " + strings.Join(e.Reasons, "; ")
}
func (e *ExportRefusal) Unwrap() error { return ErrExportRefused }

// ExportSources are what an export reads beyond the database and the
// project's repository.
type ExportSources struct {
	// ReportFile says whether a .apptown path is a lane's report file
	// (repositoryedit.ReportArtifact): prose committed by older lanes,
	// which may move; every other runtime path refuses.
	ReportFile func(path string) bool
	// Chats are the chats' records on disk, read without waking any.
	Chats       conversation.ChatRecords
	Deployments interface {
		ForProject(ctx context.Context, projectID ids.ID) ([]*deployment.Deployment, error)
	}
	Releases interface {
		ForProject(ctx context.Context, projectID ids.ID, limit int) ([]*deployment.Release, error)
	}
}

// ExportOrigin is AppTown the file comes from: its UUID, the web address
// the export was asked on, the build and the schema it ran.
type ExportOrigin struct {
	AppTown ids.ID `json:"apptown"`
	Host    string `json:"host"`
	Build   string `json:"build"`
	Schema  int    `json:"schema"`
}

// ManifestEntry is one entry of the file, checked by whoever reads it.
type ManifestEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest is the file's last entry: where it came from, what it holds and
// each other entry's hash.
type Manifest struct {
	Format     string         `json:"format"`
	Version    int            `json:"version"`
	Source     ExportOrigin   `json:"source"`
	ProjectID  ids.ID         `json:"projectId"`
	Slug       string         `json:"slug"`
	ExportedAt time.Time      `json:"exportedAt"`
	ExportedBy string         `json:"exportedBy"`
	Counts     map[string]int `json:"counts"`
	EmptyChats int            `json:"emptyChats"`
	Missing    []string       `json:"missingTranscripts,omitempty"`
	// Skipped is what stays on this AppTown: changes not landed (open or
	// abandoned) with their runs, chats, refs and attachments, and runs
	// still running.
	Skipped      []workflow.ExportSkip      `json:"skipped"`
	SkippedChats []conversation.SkippedChat `json:"skippedChats"`
	Entries      []ManifestEntry            `json:"entries"`
}

// ExportRef is one ref the bundle carries, and what it is.
type ExportRef struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Kind string `json:"kind"` // default | change | run | candidate | refused | other
}

// Listed is what the file reports but does not move: an operator sets it up
// again on the receiving AppTown, or it stays with this one.
type Listed struct {
	OwnerGroup       string             `json:"ownerGroup"`
	DefaultApprovers string             `json:"defaultApprovers"`
	GitLabRemote     string             `json:"gitlabRemote,omitempty"`
	Deployments      []ListedDeployment `json:"deployments"`
	Releases         []ListedRelease    `json:"releases"`
	Secrets          []secret.BoundName `json:"secrets"`
	// Skipped are the changes and runs that stay (Manifest.Skipped).
	Skipped []workflow.ExportSkip `json:"skipped"`
	// SecretSpec is the project's declaration of its secrets, read at its
	// default branch: names, never values.
	SecretSpec string `json:"secretSpec,omitempty"`
	// NotMoved is every kind of record that stays, and why.
	NotMoved []MoveClass `json:"notMoved"`
	// SkippedChats are chats still live or on a change that does not move.
	SkippedChats []conversation.SkippedChat `json:"skippedChats"`
	// RefsNotMoved are the repository's refs that stay: refused and
	// candidate work (never source-checked; it can hold runtime files),
	// and the branches of changes and runs that do not move.
	RefsNotMoved []ExportRef `json:"refsNotMoved"`
	// LedgerStayed counts, by kind, the ledger lines that stayed: about
	// work that stays, or not shown to be about the project or moving work.
	LedgerStayed map[string]int `json:"ledgerStayed"`
}

type ListedDeployment struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	TargetID ids.ID `json:"targetId"`
}

type ListedRelease struct {
	Number     int64  `json:"number"`
	Deployment string `json:"deployment"`
	State      string `json:"state"`
}

// ExportFile is a project's export, spooled under the data directory and
// hashed, ready to be sent once.
type ExportFile struct {
	Path     string
	SHA256   string
	Size     int64
	Manifest Manifest

	db      *database.DB
	project *project.Project
	actor   string
	now     func() time.Time
}

// Export spools a project's move file. Only landed history moves:
// shipped changes with their runs, evidence, chats, attachments
// and branches, the project's ended chats, and the default branch. A change
// not landed, a run still running and a chat still live stay, and the file
// lists them. Nothing is changed on this AppTown; Send records the export
// once the file has gone.
func (s *Service) Export(ctx context.Context, p *project.Project, actor string, origin ExportOrigin, src ExportSources) (*ExportFile, error) {
	if !p.Purpose.Moves() {
		return nil, &ExportRefusal{Reasons: []string{"it is an eval copy, a throwaway that never moves"}}
	}
	// While the export reads, the name cannot change and the repository does
	// not move. A chat that is live, or opens meanwhile, stays and is listed.
	releaseName, err := s.ReserveNames(p.Slug)
	if err != nil {
		return nil, err
	}
	defer releaseName()
	releaseRepo, err := s.repositories.Acquire(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	defer releaseRepo()

	hub := project.HubPath(s.dataDir, p.Slug)
	// A landing that settled between the repository and the records is read
	// again once; a second disagreement refuses, naming the commits.
	var snap exportSnapshot
	for attempt := 0; attempt < 2; attempt++ {
		if snap, err = s.snapshot(ctx, p, hub); err != nil {
			return nil, err
		}
		if len(snap.unreachable) == 0 {
			break
		}
	}
	if len(snap.unreachable) > 0 {
		return nil, &ExportRefusal{Reasons: snap.unreachable}
	}
	docs, refs := snap.docs, snap.refs
	// History that holds AppTown's runtime files (an agent's home, its
	// credentials among them) never leaves: the same source rule a
	// publication applies, read over every carried commit. A lane's report
	// file, which older lanes committed, is prose and may move.
	if found, err := scm.NonSourceIn(ctx, hub, refTips(refs)...); err != nil {
		return nil, err
	} else if runtime := slices.DeleteFunc(found, src.ReportFile); len(runtime) > 0 {
		return nil, &ExportRefusal{Reasons: []string{"the carried history holds AppTown's runtime files, which can hold credentials: " + strings.Join(runtime, ", ")}}
	}
	moving := map[ids.ID]bool{}
	idList, err := docs.work.ChangeIDs()
	if err != nil {
		return nil, err
	}
	for _, id := range idList {
		moving[id] = true
	}
	chats, err := conversation.ExportChats(src.Chats, p.ID, func(id ids.ID) bool { return moving[id] })
	if err != nil {
		return nil, &ExportRefusal{Reasons: []string{"a chat's files cannot be read: " + err.Error()}}
	}
	// The ledger moves with what it is about, safe by default: the
	// project's own lines and those naming only moving work move; every
	// other line stays, counted by kind.
	moveChats := make([]ids.ID, 0, len(chats.Chats))
	for _, c := range chats.Chats {
		if id, err := ids.Parse(c.ID); err == nil {
			moveChats = append(moveChats, id)
		}
	}
	runIDs, err := docs.work.RunIDs()
	if err != nil {
		return nil, err
	}
	ledger, err := audit.Select(docs.audit, audit.Moving{Changes: idList, Runs: runIDs, Chats: moveChats})
	if err != nil {
		return nil, err
	}
	docs.audit = ledger.Lines
	ledgerStayed := 0
	for _, n := range ledger.Stayed {
		ledgerStayed += n
	}
	spend, spendStayed, err := usage.Moving(docs.usage, docs.stayingRuns, chats.Staying())
	if err != nil {
		return nil, err
	}
	docs.usage = spend

	spool, err := os.MkdirTemp(s.exportsDir(), "export-")
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(spool)
		}
	}()
	bundle := filepath.Join(spool, "repository.bundle")
	if len(refs) > 0 {
		args := []string{"bundle", "create", "-q", bundle}
		for _, r := range refs {
			args = append(args, r.Ref)
		}
		if out, err := gitexec.HubRun(ctx, hub, args...); err != nil {
			return nil, fmt.Errorf("bundling the repository: %w: %s", err, out)
		}
	}
	listed, err := s.listed(ctx, p, hub, src, docs)
	if err != nil {
		return nil, err
	}
	listed.SkippedChats = chats.Skipped
	if listed.SkippedChats == nil {
		listed.SkippedChats = []conversation.SkippedChat{}
	}
	listed.RefsNotMoved = snap.stay
	listed.LedgerStayed = ledger.Stayed
	w, err := newExportWriter(filepath.Join(spool, p.Slug+".apptown-export"))
	if err != nil {
		return nil, err
	}
	defer w.close()
	if len(refs) > 0 {
		if err := w.file("repository.bundle", bundle); err != nil {
			return nil, err
		}
	}
	documents := []struct {
		name string
		doc  any
	}{
		{"refs.json", refs}, {"owners/project.json", docs.project}, {"owners/adoption.json", docs.adoption},
		{"owners/library.json", docs.library}, {"owners/work.json", docs.work}, {"owners/adrs.json", docs.adrs},
		{"owners/usage.json", docs.usage}, {"owners/audit.json", docs.audit}, {"owners/listed.json", listed},
	}
	for _, d := range documents {
		if err := w.json(d.name, d.doc); err != nil {
			return nil, err
		}
	}
	digests, err := docs.work.TranscriptDigests()
	if err != nil {
		return nil, err
	}
	var missingTranscripts []string
	for _, digest := range digests {
		raw, err := workflow.ExportTranscript(s.dataDir, digest)
		if err != nil {
			// A receipt naming a file this AppTown no longer holds is said in
			// the manifest; the receipt still moves.
			missingTranscripts = append(missingTranscripts, digest+": "+err.Error())
			continue
		}
		if err := w.bytes("transcripts/"+digest+".jsonl", raw); err != nil {
			return nil, err
		}
	}
	for _, id := range idList {
		files, err := workflow.ExportAttachments(s.dataDir, id)
		if err != nil {
			return nil, &ExportRefusal{Reasons: []string{fmt.Sprintf("change %d's attachments cannot be read: %v", id, err)}}
		}
		for _, name := range sortedKeys(files) {
			if err := w.bytes(fmt.Sprintf("attachments/%d/%s", id, name), files[name]); err != nil {
				return nil, err
			}
		}
	}
	for _, chat := range chats.Chats {
		for _, name := range sortedKeys(chat.Files) {
			if err := w.bytes("chats/"+chat.ID+"/"+filepath.ToSlash(name), chat.Files[name]); err != nil {
				return nil, err
			}
		}
	}

	counts := docs.work.Counts()
	counts["adrs"], counts["usage"], counts["ledger"] = len(docs.adrs), len(docs.usage), len(docs.audit)
	counts["adoptionRevisions"], counts["libraryVersions"] = len(docs.adoption.Revisions), len(docs.library)
	counts["chats"], counts["refs"], counts["transcripts"] = len(chats.Chats), len(refs), len(digests)-len(missingTranscripts)
	counts["ledgerStayed"], counts["usageStayed"] = ledgerStayed, spendStayed
	manifest := Manifest{Format: ExportFormat, Version: ExportVersion, Source: origin, ProjectID: p.ID, Slug: p.Slug,
		ExportedAt: s.now().UTC(), ExportedBy: actor, Counts: counts, EmptyChats: chats.Empty, Missing: missingTranscripts,
		Skipped: docs.skipped, SkippedChats: listed.SkippedChats}
	if manifest.Skipped == nil {
		manifest.Skipped = []workflow.ExportSkip{}
	}
	manifest.Entries = w.entries
	if err := w.json("manifest.json", manifest); err != nil {
		return nil, err
	}
	sum, size, err := w.finish()
	if err != nil {
		return nil, err
	}
	keep = true
	return &ExportFile{Path: w.path, SHA256: sum, Size: size, Manifest: manifest, db: s.db, project: p, actor: actor, now: s.now}, nil
}

// exportSnapshot is one agreeing read of the records and the repository:
// the owners' documents, the refs that move and those that stay, and each
// commit the moving records name that the moving refs do not carry.
type exportSnapshot struct {
	docs        ownerDocs
	refs, stay  []ExportRef
	unreachable []string
}

func (s *Service) snapshot(ctx context.Context, p *project.Project, hub string) (exportSnapshot, error) {
	docs, err := s.readOwners(ctx, p.ID)
	if err != nil {
		return exportSnapshot{}, err
	}
	branches, err := docs.work.Branches()
	if err != nil {
		return exportSnapshot{}, err
	}
	runs, err := docs.work.RunIDs()
	if err != nil {
		return exportSnapshot{}, err
	}
	refs, stay, err := exportRefs(ctx, hub, p.DefaultBranch, branches, runs)
	if err != nil {
		return exportSnapshot{}, err
	}
	snap := exportSnapshot{docs: docs, refs: refs, stay: stay}
	missing, err := unreachable(ctx, hub, refs, docs.commits)
	if err != nil {
		return exportSnapshot{}, err
	}
	for _, sha := range missing {
		snap.unreachable = append(snap.unreachable, fmt.Sprintf("commit %s is named by the moving records but is not in the moving refs", sha))
	}
	var main []ExportRef
	for _, r := range refs {
		if r.Kind == "default" {
			main = append(main, r)
		}
	}
	if missing, err = unreachable(ctx, hub, main, docs.landed); err != nil {
		return exportSnapshot{}, err
	}
	for _, sha := range missing {
		snap.unreachable = append(snap.unreachable, fmt.Sprintf("landed commit %s is not on the default branch", sha))
	}
	return snap, nil
}

func refTips(refs []ExportRef) []string {
	tips := make([]string, len(refs))
	for i, r := range refs {
		tips[i] = r.SHA
	}
	return tips
}

// Send writes the file to w, records the project.exported receipt once it
// has all been written, and removes the spool either way.
func (f *ExportFile) Send(ctx context.Context, w io.Writer) error {
	defer f.Discard()
	in, err := os.Open(f.Path)
	if err != nil {
		return err
	}
	defer in.Close()
	if _, err := io.Copy(w, in); err != nil {
		return fmt.Errorf("sending the export: %w", err)
	}
	// The export has left: its receipt's whole transaction is written though
	// the caller has gone.
	ctx = context.WithoutCancel(ctx)
	return f.db.InTx(ctx, func(tx *database.Tx) error {
		return audit.At(f.now()).RecordIn(ctx, tx, f.actor, f.project.ID, "project.exported", f.project.Slug, map[string]any{
			"sha256": f.SHA256, "size": f.Size, "counts": f.Manifest.Counts, "emptyChats": f.Manifest.EmptyChats,
			"source": f.Manifest.Source.Host,
		})
	})
}

// Discard removes the spooled file; Send does it itself.
func (f *ExportFile) Discard() { _ = os.RemoveAll(filepath.Dir(f.Path)) }

func (s *Service) exportsDir() string {
	dir := filepath.Join(s.dataDir, "exports")
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

type ownerDocs struct {
	project  json.RawMessage
	adoption adoption.ProjectExport
	library  []wow.ExportedVersion
	work     workflow.ProjectExport
	adrs     []json.RawMessage
	usage    []json.RawMessage
	audit    []json.RawMessage
	secrets  []secret.BoundName
	commits  []string
	landed   []string
	skipped  []workflow.ExportSkip
	// stayingRuns are the runs that stay, whose ledger lines stay too.
	stayingRuns []ids.ID
}

// readOwners reads every owner's document in one repeatable-read snapshot,
// so the rows agree with each other.
func (s *Service) readOwners(ctx context.Context, projectID ids.ID) (ownerDocs, error) {
	var d ownerDocs
	tx, err := s.db.ReadSnapshot(ctx)
	if err != nil {
		return d, err
	}
	defer func() { _ = tx.Rollback() }()
	if d.skipped, err = workflow.ExportSkippedIn(ctx, tx, projectID); err != nil {
		return d, err
	}
	if d.landed, err = workflow.ExportLandedCommitsIn(ctx, tx, projectID); err != nil {
		return d, err
	}
	if d.stayingRuns, err = workflow.ExportStayingRunsIn(ctx, tx, projectID); err != nil {
		return d, err
	}
	if d.project, err = project.ExportIn(ctx, tx, projectID); err != nil {
		return d, err
	}
	if d.adoption, err = adoption.ExportProjectIn(ctx, tx, projectID); err != nil {
		return d, err
	}
	refs, err := libraryRefs(d.adoption)
	if err != nil {
		return d, err
	}
	if d.library, err = wow.ExportVersionsIn(ctx, tx, refs); err != nil {
		return d, err
	}
	if d.work, err = workflow.ExportProjectIn(ctx, tx, projectID); err != nil {
		return d, err
	}
	if d.commits, err = workflow.ExportCommitsIn(ctx, tx, projectID); err != nil {
		return d, err
	}
	if d.adrs, err = adrs.ExportProjectIn(ctx, tx, projectID); err != nil {
		return d, err
	}
	if d.usage, err = usage.ExportProjectIn(ctx, tx, projectID); err != nil {
		return d, err
	}
	if d.audit, err = audit.ExportProjectIn(ctx, tx, projectID); err != nil {
		return d, err
	}
	if d.secrets, err = secret.ExportNamesIn(ctx, tx, projectID); err != nil {
		return d, err
	}
	return d, nil
}

// libraryRefs are the library versions the project's current selection
// uses, so the receiving AppTown can find or publish the same content.
func libraryRefs(a adoption.ProjectExport) ([]wow.VersionRef, error) {
	var refs []wow.VersionRef
	receipt, ok, err := a.CurrentReceipt()
	if err != nil {
		return nil, err
	}
	if ok {
		for _, b := range receipt.Guidance {
			refs = append(refs, wow.VersionRef{Kind: "guidance", Name: b.Name, Version: int64(b.Version)})
		}
		for _, w := range receipt.Workflows {
			if w.Version > 0 {
				refs = append(refs, wow.VersionRef{Kind: "workflow", Name: w.Name, Version: int64(w.Version)})
			}
		}
	}
	packs, err := a.PackVersions()
	if err != nil {
		return nil, err
	}
	for _, pv := range packs {
		refs = append(refs, wow.VersionRef{Kind: "pack", PackID: pv.PackID, Version: pv.Version})
	}
	return refs, nil
}

func (s *Service) listed(ctx context.Context, p *project.Project, hub string, src ExportSources, docs ownerDocs) (Listed, error) {
	out := Listed{OwnerGroup: p.OwnerGroup, DefaultApprovers: p.DefaultApprovers, Secrets: docs.secrets,
		Deployments: []ListedDeployment{}, Releases: []ListedRelease{}, NotMoved: notMoved(), Skipped: docs.skipped}
	if out.Skipped == nil {
		out.Skipped = []workflow.ExportSkip{}
	}
	if out.Secrets == nil {
		out.Secrets = []secret.BoundName{}
	}
	if remote, _, ok := scm.Origin(ctx, hub); ok {
		out.GitLabRemote = scm.PublicRemoteURL(remote)
	}
	if p.DefaultBranch != "" {
		if spec, err := gitexec.HubRun(ctx, hub, "show", p.DefaultBranch+":secretspec.toml"); err == nil {
			out.SecretSpec = spec
		}
	}
	deps, err := src.Deployments.ForProject(ctx, p.ID)
	if err != nil {
		return Listed{}, err
	}
	for _, d := range deps {
		out.Deployments = append(out.Deployments, ListedDeployment{Name: d.Name, Kind: string(d.Kind), TargetID: d.TargetID})
	}
	releases, err := src.Releases.ForProject(ctx, p.ID, 100000)
	if err != nil {
		return Listed{}, err
	}
	for _, r := range releases {
		out.Releases = append(out.Releases, ListedRelease{Number: r.Number, Deployment: r.Deployment, State: r.State.String()})
	}
	return out, nil
}

// exportRefs splits the repository's refs into those that move and those
// that stay. The default branch, tags, and the branches of the changes and
// runs that move are carried. Refused and candidate work never are: it was
// never source-checked and can hold AppTown's runtime files. Transient
// refs a publication or a check makes and drops are neither.
func exportRefs(ctx context.Context, hub, defaultBranch string, branches []string, runs []ids.ID) (move, stay []ExportRef, err error) {
	if _, err := os.Stat(hub); errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	out, err := gitexec.HubRun(ctx, hub, "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return nil, nil, fmt.Errorf("listing the repository's refs: %w: %s", err, out)
	}
	carried := map[string]bool{}
	for _, b := range branches {
		carried["refs/heads/"+b] = true
	}
	for _, id := range runs {
		carried[fmt.Sprintf("refs/heads/apptown/run-%d", id)] = true
		// A run made before the AppTown rename keeps the branch it was made
		// on (forge/run-N): its name is its identity, and history stays.
		carried[fmt.Sprintf("refs/heads/forge/run-%d", id)] = true
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, sha, ok := strings.Cut(line, " ")
		if !ok || transientRef(name) {
			continue
		}
		ref := ExportRef{Ref: name, SHA: sha, Kind: refKind(name, defaultBranch)}
		switch {
		case ref.Kind == "default" || strings.HasPrefix(name, "refs/tags/") || carried[name]:
			move = append(move, ref)
		default:
			stay = append(stay, ref)
		}
	}
	return move, stay, nil
}

func transientRef(name string) bool {
	for _, prefix := range []string{scm.ScratchPrefix, "refs/apptown/incoming/", "refs/apptown/commits/"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return name == "refs/apptown/published"
}

func refKind(name, defaultBranch string) string {
	switch {
	case defaultBranch != "" && name == "refs/heads/"+defaultBranch:
		return "default"
	// Changes and runs made before the AppTown rename keep their recorded
	// branch names (forge/change-N, forge/run-N): a branch is its change's
	// identity, and history does not change.
	case strings.HasPrefix(name, "refs/heads/apptown/change-"), strings.HasPrefix(name, "refs/heads/forge/change-"):
		return "change"
	case strings.HasPrefix(name, "refs/heads/apptown/run-"), strings.HasPrefix(name, "refs/heads/forge/run-"):
		return "run"
	case strings.HasPrefix(name, "refs/apptown/candidates/"):
		return "candidate"
	case strings.HasPrefix(name, "refs/apptown/refused/"):
		return "refused"
	}
	return "other"
}

// unreachable are the named commits no exported ref reaches: evidence that
// would point at nothing on the receiving AppTown.
func unreachable(ctx context.Context, hub string, refs []ExportRef, named []string) ([]string, error) {
	if len(named) == 0 {
		return nil, nil
	}
	reachable := map[string]bool{}
	if len(refs) > 0 {
		tips := make([]string, 0, len(refs))
		for _, r := range refs {
			tips = append(tips, r.SHA)
		}
		out, err := gitexec.HubOutput(ctx, hub, strings.NewReader(strings.Join(tips, "\n")+"\n"), "rev-list", "--stdin")
		if err != nil {
			return nil, fmt.Errorf("walking the repository's history: %w: %s", err, out)
		}
		for _, sha := range strings.Fields(string(out)) {
			reachable[sha] = true
		}
	}
	var missing []string
	for _, sha := range named {
		if !reachable[sha] {
			missing = append(missing, sha)
		}
	}
	return missing, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// exportWriter writes the tar in one pass, keeping each entry's hash for
// the manifest.
type exportWriter struct {
	path    string
	out     *os.File
	tar     *tar.Writer
	entries []ManifestEntry
}

func newExportWriter(path string) (*exportWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	return &exportWriter{path: path, out: f, tar: tar.NewWriter(f)}, nil
}

func (w *exportWriter) json(name string, v any) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	return w.bytes(name, b)
}

func (w *exportWriter) bytes(name string, b []byte) error {
	if err := w.tar.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := w.tar.Write(b); err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	w.entries = append(w.entries, ManifestEntry{Path: name, Size: int64(len(b)), SHA256: hex.EncodeToString(sum[:])})
	return nil
}

func (w *exportWriter) file(name, path string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := w.tar.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: info.Size(), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w.tar, h), in); err != nil {
		return err
	}
	w.entries = append(w.entries, ManifestEntry{Path: name, Size: info.Size(), SHA256: hex.EncodeToString(h.Sum(nil))})
	return nil
}

// finish closes the tar and hashes the whole file.
func (w *exportWriter) finish() (string, int64, error) {
	if err := w.tar.Close(); err != nil {
		return "", 0, err
	}
	if err := w.out.Sync(); err != nil {
		return "", 0, err
	}
	if _, err := w.out.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(h, w.out)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func (w *exportWriter) close() { _ = w.out.Close() }
