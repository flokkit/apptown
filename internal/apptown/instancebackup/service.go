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

// BuiltinDatabase is AppTown's built-in database, which the composition
// root makes, names and dumps (dbboot.go): Names is what db.env says its
// container and database are (false: there is none), Dump writes a whole
// gzipped dump at path or nothing, Ensure starts or makes it and answers
// its URL once it answers.
type BuiltinDatabase interface {
	Names() (container, database string, ok bool)
	Dump(ctx context.Context, path string) error
	DumpClientVersion(ctx context.Context) (string, error)
	Ensure(ctx context.Context) (url string, err error)
}

// Dependencies are what a backup and the record of a restore run on.
type Dependencies struct {
	DB      *database.DB
	DataDir string
	// Database is the built-in database; nil when AppTown runs on -db-url,
	// whose database it does not back up.
	Database    BuiltinDatabase
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
