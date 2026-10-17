package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/poll"
	"github.com/jackc/pgx/v5"
)

// ownDatabase connects to the cluster as its admin (the connection the
// API returns to the cluster's owner, TLS required) and makes the app's
// user the owner of the app's database. Idempotent.
func ownDatabase(ctx context.Context, adminURI, dbName, dbUser string) error {
	if adminURI == "" {
		return errors.New("the platform returned no admin connection for the cluster")
	}
	if !strings.Contains(adminURI, "sslmode=") {
		sep := "?"
		if strings.Contains(adminURI, "?") {
			sep = "&"
		}
		adminURI += sep + "sslmode=require"
	}
	conn, err := pgx.Connect(ctx, adminURI)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, fmt.Sprintf(`ALTER DATABASE %s OWNER TO %s`, pgx.Identifier{dbName}.Sanitize(), pgx.Identifier{dbUser}.Sanitize()))
	return err
}

// --- the managed database (§2.6, an API-driven need module) ----------------------------------

// doDBRegion maps an App Platform region slug to a database region slug
// (syd → syd1); an explicit database region is passed through.
func doDBRegion(region string) string {
	switch region {
	case "syd":
		return "syd1"
	case "nyc":
		return "nyc3"
	case "sfo":
		return "sfo3"
	case "ams":
		return "ams3"
	case "sgp":
		return "sgp1"
	case "lon":
		return "lon1"
	case "fra":
		return "fra1"
	case "tor":
		return "tor1"
	case "blr":
		return "blr1"
	}
	return region
}

type doDatabase struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Engine     string   `json:"engine"`
	Version    string   `json:"version"`
	Region     string   `json:"region"`
	Size       string   `json:"size"`
	Status     string   `json:"status"`
	Tags       []string `json:"tags"`
	Connection struct {
		URI  string `json:"uri"` // the admin connection, returned to the cluster's owner
		Host string `json:"host"`
		Port int    `json:"port"`
	} `json:"connection"`
}

// fulfilNeed makes the environment's managed need exist and returns the
// binding the renderer expresses natively: an App Platform database
// component, which the platform wires (trusted source, credentials) and
// exposes to the app as ${db.DATABASE_URL} — the variable the emulator
// injects locally. Object storage (s3) is a Spaces bucket (storage.go).
func (d *Driver) FulfilNeed(ctx context.Context, p deploy.Placement, need string, params map[string]string, logf func(string, ...any)) (deploy.NeedBinding, map[string]any, error) {
	if need == needS3 {
		return d.fulfilStorage(ctx, p, logf)
	}
	if need != "postgres" {
		return deploy.NeedBinding{}, nil, deploy.Terminal(fmt.Errorf("App Platform can provide postgres and s3; %s is not offered here yet", need))
	}
	c, cfg, err := d.Client(ctx)
	if err != nil {
		return deploy.NeedBinding{}, nil, err
	}
	slug := d.Slug
	name := "forge-" + slug + "-" + p.Name
	version := params["version"]
	if version == "" {
		version = "16"
	}
	dbName, dbUser := "app", "app"
	if v := params["database"]; v != "" {
		dbName = v
	}
	if v := params["user"]; v != "" {
		dbUser = v
	}
	db, err := d.findDatabase(ctx, c, name)
	if err != nil {
		return deploy.NeedBinding{}, nil, err
	}
	requested := map[string]any{"name": name, "engine": "pg", "version": version, "region": doDBRegion(cfg.Region), "size": cfg.DBSize, "database": dbName, "user": dbUser}
	if db != nil {
		// Found by name: adopt it only when it is what was asked for and
		// carries the forge's marker; anything else is somebody's cluster
		// with our name on it, and the mint is refused (§2.6).
		if why := d.validateDiscovered(db, version, slug, p.Name); why != "" {
			return deploy.NeedBinding{}, nil, deploy.Terminal(fmt.Errorf("a cluster named %s exists but is not this deployment's: %s", name, why))
		}
	}
	// Durable intent before the act: the need instance says what was
	// requested and that it is being made, so a crash mid-create leaves
	// a record to observe against, not a mystery cluster.
	if err := d.Needs.RecordIntent(ctx, p.ID, need, requested); err != nil {
		if fault.KindOf(err) == fault.Conflict {
			// The record refuses another identity for this need: asking
			// again changes nothing, so the release ends here.
			return deploy.NeedBinding{}, nil, deploy.Terminal(err)
		}
		return deploy.NeedBinding{}, nil, err
	}
	if db == nil {
		logf("creating the managed Postgres cluster %s (%s, pg %s, %s) — this takes a few minutes", name, doDBRegion(cfg.Region), version, cfg.DBSize)
		var made struct {
			Database doDatabase `json:"database"`
		}
		body := map[string]any{"name": name, "engine": "pg", "version": version, "region": doDBRegion(cfg.Region),
			"size": cfg.DBSize, "num_nodes": 1, "tags": []string{"forge", "forge-project-" + slug, "forge-env-" + p.Name}}
		if err := c.Call(ctx, "POST", "/v2/databases", body, &made); err != nil {
			return deploy.NeedBinding{}, nil, fmt.Errorf("creating the database cluster: %w", err)
		}
		db = &made.Database
		// The provider's id is the identity underneath the name.
		if err := d.Needs.RecordIdentity(ctx, p.ID, need, requested, db.ID, db.Name); err != nil {
			return deploy.NeedBinding{}, nil, fmt.Errorf("recording the cluster's id: %w", err)
		}
	} else {
		logf("the managed Postgres cluster %s exists (%s)", name, db.Status)
	}
	// Wait for online: a fresh cluster takes minutes.
	deadline := d.options.Now().Add(15 * time.Minute)
	for db.Status != "online" {
		if d.options.Now().After(deadline) {
			return deploy.NeedBinding{}, nil, fmt.Errorf("the cluster %s is still %s after 15 minutes", name, db.Status)
		}
		if err := poll.Wait(ctx, d.options.DBPoll); err != nil {
			return deploy.NeedBinding{}, nil, fmt.Errorf("waiting for the cluster %s to come online: %w", name, err)
		}
		var got struct {
			Database doDatabase `json:"database"`
		}
		if err := c.Call(ctx, "GET", "/v2/databases/"+db.ID, nil, &got); err != nil {
			return deploy.NeedBinding{}, nil, err
		}
		if got.Database.Status != db.Status {
			logf("cluster %s is %s", name, got.Database.Status)
		}
		db = &got.Database
	}
	// The database and the user inside it, idempotently.
	if err := c.Call(ctx, "POST", "/v2/databases/"+db.ID+"/dbs", map[string]any{"name": dbName}, nil); err != nil && !IsStatus(err, 422) && !IsStatus(err, 409) {
		return deploy.NeedBinding{}, nil, fmt.Errorf("creating database %s: %w", dbName, err)
	}
	if err := c.Call(ctx, "POST", "/v2/databases/"+db.ID+"/users", map[string]any{"name": dbUser}, nil); err != nil && !IsStatus(err, 422) && !IsStatus(err, 409) {
		return deploy.NeedBinding{}, nil, fmt.Errorf("creating user %s: %w", dbUser, err)
	}
	// Postgres 15 took CREATE on the public schema away from everyone but
	// the database's owner, and the platform makes the database owned by
	// its admin: a migration run as the app's user is refused. The user
	// owns its database, so its schema is its own.
	if err := d.options.OwnDatabase(ctx, db.Connection.URI, dbName, dbUser); err != nil {
		return deploy.NeedBinding{}, nil, fmt.Errorf("making %s the owner of database %s: %w", dbUser, dbName, err)
	}
	logf("database %s is owned by user %s", dbName, dbUser)
	// The component name is the need's name: ${postgres.DATABASE_URL}.
	comp := need
	b := deploy.NeedBinding{
		Env: map[string]string{
			"DATABASE_URL": "${" + comp + ".DATABASE_URL}",
			"PGHOST":       "${" + comp + ".HOSTNAME}",
			"PGPORT":       "${" + comp + ".PORT}",
			"PGDATABASE":   "${" + comp + ".DATABASE}",
			"PGUSER":       "${" + comp + ".USERNAME}",
			"PGPASSWORD":   "${" + comp + ".PASSWORD}",
			"PGSSLMODE":    "require",
		},
		Native: map[string]any{"name": comp, "engine": "PG", "production": true, "cluster_name": db.Name, "db_name": dbName, "db_user": dbUser},
	}
	handle := map[string]any{"name": db.Name, "id": db.ID, "region": db.Region, "size": db.Size, "version": db.Version, "database": dbName, "user": dbUser}
	return b, handle, nil
}

// validateDiscovered: a cluster found by name is this deployment's only
// if it is Postgres of the requested major version and carries the tags
// the forge put on it. Returns the reason it is not, or "".
func (d *Driver) validateDiscovered(db *doDatabase, version, slug, deploymentName string) string {
	if db.Engine != "pg" {
		return "its engine is " + db.Engine + ", not postgres"
	}
	if major := strings.SplitN(db.Version, ".", 2)[0]; major != version {
		return fmt.Sprintf("it runs Postgres %s, the contract asks for %s", db.Version, version)
	}
	if !containsString(db.Tags, "forge-project-"+slug) || !containsString(db.Tags, "forge-env-"+deploymentName) {
		return "it does not carry this forge deployment's tags (forge-project-" + slug + ", forge-env-" + deploymentName + ")"
	}
	return ""
}

func (d *Driver) findDatabase(ctx context.Context, c *Client, name string) (*doDatabase, error) {
	databases, err := listAll[doDatabase](ctx, c, "/v2/databases?per_page=200", "databases")
	if err != nil {
		return nil, err
	}
	for i := range databases {
		if databases[i].Name == name {
			return &databases[i], nil
		}
	}
	return nil, nil
}
