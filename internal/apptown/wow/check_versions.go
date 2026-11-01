package wow

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/internal/database"
)

// versionChecks is an exact authored declaration. Missing historical versions
// remain absent so a consumer can retain its own durable evidence.
type versionChecks struct {
	Block      string
	Version    int
	ChecksJSON string
}

func CheckVersionsIn(ctx context.Context, q database.Queryer, refs []BlockRef) ([]versionChecks, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	names := make([]string, len(refs))
	versions := make([]int64, len(refs))
	for i, ref := range refs {
		names[i] = ref.Name
		versions[i] = int64(ref.Version)
	}
	rows, err := wowsql.New(q.SQL()).CheckDefinitionsAtVersions(ctx, wowsql.CheckDefinitionsAtVersionsParams{Blocks: names, Versions: versions})
	if err != nil {
		return nil, err
	}
	out := make([]versionChecks, 0, len(rows))
	for _, r := range rows {
		out = append(out, versionChecks{Block: r.Name, Version: int(r.Version), ChecksJSON: r.ChecksJson})
	}
	return out, nil
}
