package conformance

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/conformance/internal/conformancesql"

	"github.com/flokkit/apptown/internal/database"
)

// Project metadata and pinned declarations are read together. Git/model/container
// effects happen only after this database snapshot has been released.
type Project struct {
	ID                 ids.ID
	Slug, Name, Branch string
	Checks             []DeclaredCheck
}

func readConformanceProjects(ctx context.Context, db database.Queryer, idList []ids.ID, repoOnly bool) ([]Project, error) {
	rows, err := queries(db).ConformanceProjects(ctx, conformancesql.ConformanceProjectsParams{Column1: idList, Column2: repoOnly})
	if err != nil {
		return nil, err
	}
	out := make([]Project, 0, len(rows))
	for _, r := range rows {
		out = append(out, Project{ID: r.ID, Slug: r.Slug, Name: r.Name, Branch: r.DefaultBranch})
	}
	return out, nil
}

// Enabled imports retain manifest order and their exact pinned versions. A
// missing version is an error, distinct from a valid declaration with no checks.
func readAdoptedChecks(ctx context.Context, db database.Queryer, source checkSource, idList []ids.ID) (map[ids.ID][]DeclaredCheck, error) {
	if len(idList) == 0 {
		return map[ids.ID][]DeclaredCheck{}, nil
	}
	if source == nil {
		return nil, fmt.Errorf("adopted check source is unavailable")
	}
	return source.ChecksIn(ctx, db, idList)
}

func readPreparedConformanceProjects(ctx context.Context, db database.Queryer, source checkSource, idList []ids.ID, repoOnly bool) ([]Project, error) {
	projects, err := readConformanceProjects(ctx, db, idList, repoOnly)
	if err != nil {
		return nil, err
	}
	repoIDs := make([]ids.ID, 0, len(projects))
	for _, p := range projects {
		if p.Branch != "" {
			repoIDs = append(repoIDs, p.ID)
		}
	}
	checks, err := readAdoptedChecks(ctx, db, source, repoIDs)
	if err != nil {
		return nil, err
	}
	for i := range projects {
		projects[i].Checks = checks[projects[i].ID]
	}
	return projects, nil
}

func (s reviewStore) prepareProjects(ctx context.Context, idList []ids.ID) ([]Project, error) {
	return readConformance(ctx, s.db, func(db database.Queryer) ([]Project, error) {
		return readPreparedConformanceProjects(ctx, db, s.checks, idList, false)
	})
}

// One batch selects the latest record for each requested check. Old unadopted
// versions and unrelated projects are never decoded; the database can join the
// requested set rather than perform a client-side lookup loop.
func readCurrentVerdicts(ctx context.Context, db database.Queryer, projects []Project) ([]Verdict, error) {
	var idList []ids.ID
	var versions []int64
	var blocks, checks []string
	for _, p := range projects {
		for _, c := range p.Checks {
			idList = append(idList, p.ID)
			blocks = append(blocks, c.Block)
			versions = append(versions, int64(c.Version))
			checks = append(checks, c.Check.Name)
		}
	}
	if len(idList) == 0 {
		return []Verdict{}, nil
	}
	rows, err := queries(db).CurrentVerdicts(ctx, conformancesql.CurrentVerdictsParams{
		Column1: idList, Column2: blocks, Column3: versions, Column4: checks,
	})
	if err != nil {
		return nil, err
	}
	return verdicts(rows)
}

type currentConformanceFacts struct {
	Projects           []Project
	Verdicts           []Verdict
	Exceptions         map[ids.ID][]Disposition
	Adjudications      map[ids.ID]Adjudication
	PriorConfirmations map[ids.ID]PriorConfirmation
	Reviews            []Review
}

func readCurrentConformance(ctx context.Context, db database.Queryer, source checkSource, at time.Time, projectIDs ...ids.ID) (*currentConformanceFacts, error) {
	f := &currentConformanceFacts{Exceptions: map[ids.ID][]Disposition{}}
	var err error
	f.Projects, err = readPreparedConformanceProjects(ctx, db, source, projectIDs, true)
	if err != nil {
		return nil, err
	}
	f.Verdicts, err = readCurrentVerdicts(ctx, db, f.Projects)
	if err != nil {
		return nil, err
	}
	idList := make([]ids.ID, 0, len(f.Projects))
	for _, p := range f.Projects {
		idList = append(idList, p.ID)
	}
	exceptions, err := readDispositions(ctx, db, idList, true, at)
	if err != nil {
		return nil, err
	}
	for _, d := range exceptions {
		f.Exceptions[d.ProjectID] = append(f.Exceptions[d.ProjectID], d)
	}
	f.Adjudications, err = readAdjudications(ctx, db, verdictIDs(f.Verdicts))
	if err != nil {
		return nil, err
	}
	f.PriorConfirmations, err = readPriorConfirmations(ctx, db, RecordedObservations(f.Verdicts), f.Adjudications)
	if err != nil {
		return nil, err
	}
	if len(projectIDs) == 0 {
		f.Reviews, err = readConformanceReviews(ctx, db)
	}
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (s reviewStore) current(ctx context.Context) (*currentConformanceFacts, error) {
	return readConformance(ctx, s.db, func(db database.Queryer) (*currentConformanceFacts, error) {
		return readCurrentConformance(ctx, db, s.checks, s.now())
	})
}

// Historical definitions are optional evidence: a removed catalog entry does not
// erase a saved verdict. Query/decoding failures are errors, never absence.
func readHistoricalCheckDefs(ctx context.Context, db database.Queryer, source checkSource, vs []Verdict) (map[string]CheckDefinition, error) {
	seen := map[CheckVersion]bool{}
	var versions []CheckVersion
	for _, v := range vs {
		key := CheckVersion{Block: v.Block, Version: v.Version}
		if !seen[key] {
			seen[key] = true
			versions = append(versions, key)
		}
	}
	catalog := map[string]CheckDefinition{}
	if len(versions) > 0 {
		if source == nil {
			return nil, fmt.Errorf("adopted check source is unavailable")
		}
		declarations, err := source.HistoricalChecksIn(ctx, db, versions)
		if err != nil {
			return nil, err
		}
		for _, d := range declarations {
			def := definitionOf(d.Block, d.Version, d.Check)
			catalog[def.Key] = def
		}
	}
	out := map[string]CheckDefinition{}
	for _, v := range vs {
		key := CheckKey(v.Block, v.Version, v.Check)
		d, exists := out[key]
		if !exists {
			var found bool
			d, found = catalog[key]
			if !found {
				d = CheckDefinition{Key: key, Block: v.Block, Version: v.Version, Check: v.Check, Evaluator: v.Evaluator, Severity: v.Severity}
			}
		}
		d.Adopters = append(d.Adopters, v.ProjectSlug)
		out[key] = d
	}
	for key, d := range out {
		sort.Strings(d.Adopters)
		out[key] = d
	}
	return out, nil
}
