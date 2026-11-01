package instancebackup

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

// databaseFiles is the database a backup dumps, the built-in one or the -db-url
// one (the composition root's databaseDump): Dump writes a whole gzipped
// plain dump at path, without owners or privileges so it loads into the
// built-in database, or nothing; DumpClientVersion is the pg_dump that does
// it.
type databaseFiles interface {
	Dump(ctx context.Context, path string) error
	DumpClientVersion(ctx context.Context) (string, error)
}

// builtinDatabase is AppTown's built-in database, which the composition
// root makes, names and dumps (dbboot.go), and which a restore always loads
// into: Names is what db.env says its container and database are (false:
// there is none), Ensure starts or makes it and answers its URL once it
// answers.
type builtinDatabase interface {
	Names() (container, database string, ok bool)
	Ensure(ctx context.Context) (url string, err error)
}

// Dependencies are what a backup and the record of a restore run on.
type Dependencies struct {
	DB      *database.DB
	DataDir string
	// Database is the database AppTown runs on, which a backup dumps.
	Database    databaseFiles
	Protection  *dataprotection.Service
	Deployment  *deployment.Service
	Deployments *deployment.Deployments
	Targets     *deployment.Targets
	// BuiltInTarget names AppTown's own Docker host.
	BuiltInTarget string
	Secrets       *secret.Service
	// NewWork is AppTown's hold of new jobs and steps.
	NewWork *Hold
	Now     func() time.Time
}

// Service takes whole-AppTown backups and records what a restored AppTown
// does when it first starts.
type Service struct {
	deps Dependencies
	// underWay keeps backups one at a time.
	underWay Hold
}

// New builds the service.
func New(deps Dependencies) *Service { return &Service{deps: deps} }

func (s *Service) now() time.Time { return clock.Or(s.deps.Now)() }
