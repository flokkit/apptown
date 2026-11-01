package digitalocean

import (
	"context"

	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
)

// ManagedData is what AppTown asks of a managed database cluster to
// protect its data (design/subsystems/deployment.md, "Protecting data
// off-host"): the app
// user's connection, read at use time and never recorded; a new, empty
// database, made by the platform and never adopted by name; its removal;
// the platform's own backups, read for recency; and the cluster's deletion.
// The cluster's admin connection is used only here, by AppTown itself,
// to hand a new database to the app's user.
type ManagedData struct {
	Client func(context.Context) (*Client, Config, error)
	// OwnDatabase makes the user the owner of the database through the
	// admin connection; nil is the real one.
	OwnDatabase func(ctx context.Context, adminURI, database, user string) error
}

// Connection and NativeBackup are the provider-neutral answers.
type (
	connection   = deploy.Connection
	nativeBackup = deploy.NativeBackup
)

var _ deploy.ManagedData = ManagedData{}

// errDatabaseExists refuses to make a database whose name the cluster
// already holds: a new place is made by the platform, never adopted.
var errDatabaseExists = errors.New("the cluster already holds a database of that name; a new place is never adopted by name")

func (m ManagedData) client(ctx context.Context) (*Client, error) {
	if m.Client == nil {
		return nil, errors.New("no DigitalOcean account for this target")
	}
	c, _, err := m.Client(ctx)
	return c, err
}

func (m ManagedData) cluster(ctx context.Context, c *Client, clusterID string) (*doDatabase, error) {
	var got struct {
		Database doDatabase `json:"database"`
	}
	if err := c.Call(ctx, "GET", "/v2/databases/"+url.PathEscape(clusterID), nil, &got); err != nil {
		return nil, fmt.Errorf("reading the cluster %s: %w", clusterID, err)
	}
	return &got.Database, nil
}

// AppConnection is the app user's connection to database on the cluster,
// over TLS.
func (m ManagedData) AppConnection(ctx context.Context, clusterID, database, user string) (connection, error) {
	c, err := m.client(ctx)
	if err != nil {
		return connection{}, err
	}
	db, err := m.cluster(ctx, c, clusterID)
	if err != nil {
		return connection{}, err
	}
	if db.Connection.Host == "" || db.Connection.Port == 0 {
		return connection{}, fmt.Errorf("the platform gave the cluster %s no host and port", clusterID)
	}
	var got struct {
		User struct {
			Name     string `json:"name"`
			Password string `json:"password"`
		} `json:"user"`
	}
	if err := c.Call(ctx, "GET", "/v2/databases/"+url.PathEscape(clusterID)+"/users/"+url.PathEscape(user), nil, &got); err != nil {
		return connection{}, fmt.Errorf("reading the user %s on the cluster %s: %w", user, clusterID, err)
	}
	if got.User.Password == "" {
		return connection{}, fmt.Errorf("the platform gave no password for the user %s on the cluster %s", user, clusterID)
	}
	return connection{Host: db.Connection.Host, Port: db.Connection.Port, Database: database, User: user, Password: got.User.Password, SSLMode: "require"}, nil
}

// Databases are the databases the cluster holds, as the platform lists
// them.
func (m ManagedData) Databases(ctx context.Context, clusterID string) ([]string, error) {
	c, err := m.client(ctx)
	if err != nil {
		return nil, err
	}
	var got struct {
		DBs []struct {
			Name string `json:"name"`
		} `json:"dbs"`
	}
	if err := c.Call(ctx, "GET", "/v2/databases/"+url.PathEscape(clusterID)+"/dbs", nil, &got); err != nil {
		return nil, fmt.Errorf("listing the databases on the cluster %s: %w", clusterID, err)
	}
	names := make([]string, 0, len(got.DBs))
	for _, d := range got.DBs {
		names = append(names, d.Name)
	}
	return names, nil
}

// NewDatabase makes a new, empty database on the cluster and hands it to
// owner, who must already exist. A name the cluster already holds is
// refused (ErrDatabaseExists): the place AppTown fills is always one the
// platform made for it.
func (m ManagedData) NewDatabase(ctx context.Context, clusterID, name, owner string) error {
	c, err := m.client(ctx)
	if err != nil {
		return err
	}
	existing, err := m.Databases(ctx, clusterID)
	if err != nil {
		return notMade{err}
	}
	if slices.Contains(existing, name) {
		return notMade{fmt.Errorf("%w: %s", errDatabaseExists, name)}
	}
	if err := c.Call(ctx, "POST", "/v2/databases/"+url.PathEscape(clusterID)+"/dbs", map[string]any{"name": name}, nil); err != nil {
		// DigitalOcean answers 422 both for a name the cluster holds and for
		// a name it does not accept: only the first is "exists".
		var api *apiError
		if isStatus(err, 409) || (errors.As(err, &api) && api.Status == 422 && strings.Contains(api.Msg, "already exists")) {
			return fmt.Errorf("%w: %s (%w)", errDatabaseExists, name, err)
		}
		if errors.As(err, &api) && api.Refused() {
			return notMade{fmt.Errorf("the platform refused the database name %s: %w", name, err)}
		}
		return fmt.Errorf("making the database %s: %w", name, err)
	}
	db, err := m.cluster(ctx, c, clusterID)
	if err != nil {
		return err
	}
	own := m.OwnDatabase
	if own == nil {
		own = ownDatabase
	}
	if err := own(ctx, db.Connection.URI, name, owner); err != nil {
		return fmt.Errorf("making %s the owner of the new database %s: %w", owner, name, err)
	}
	return nil
}

// notMade is a NewDatabase failure before it asked for the database: the
// database was not made (deploy.Refused).
type notMade struct{ error }

func (e notMade) Unwrap() error { return e.error }
func (notMade) Refused() bool   { return true }

// DropDatabase removes one database from the cluster; one already gone is
// no error.
func (m ManagedData) DropDatabase(ctx context.Context, clusterID, name string) error {
	c, err := m.client(ctx)
	if err != nil {
		return err
	}
	if err := c.Call(ctx, "DELETE", "/v2/databases/"+url.PathEscape(clusterID)+"/dbs/"+url.PathEscape(name), nil, nil); err != nil && !isStatus(err, 404) {
		return fmt.Errorf("removing the database %s: %w", name, err)
	}
	return nil
}

// Backups are the platform's own backups of the cluster, newest first.
// DigitalOcean takes one a day and keeps seven; deleting the cluster
// deletes them.
func (m ManagedData) Backups(ctx context.Context, clusterID string) ([]nativeBackup, error) {
	c, err := m.client(ctx)
	if err != nil {
		return nil, err
	}
	var got struct {
		Backups []struct {
			CreatedAt time.Time `json:"created_at"`
			SizeGB    float64   `json:"size_gigabytes"`
		} `json:"backups"`
	}
	if err := c.Call(ctx, "GET", "/v2/databases/"+url.PathEscape(clusterID)+"/backups", nil, &got); err != nil {
		return nil, fmt.Errorf("reading the backups of the cluster %s: %w", clusterID, err)
	}
	out := make([]nativeBackup, 0, len(got.Backups))
	for _, b := range got.Backups {
		out = append(out, nativeBackup{CreatedAt: b.CreatedAt, SizeGB: b.SizeGB})
	}
	slices.SortFunc(out, func(a, b nativeBackup) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out, nil
}

// DeleteInstance deletes the cluster, every database in it and the
// platform's backups of it. One already gone is no error.
func (m ManagedData) DeleteInstance(ctx context.Context, clusterID string) error {
	c, err := m.client(ctx)
	if err != nil {
		return err
	}
	if err := c.Call(ctx, "DELETE", "/v2/databases/"+url.PathEscape(clusterID), nil, nil); err != nil && !isStatus(err, 404) {
		return fmt.Errorf("deleting the cluster %s: %w", clusterID, err)
	}
	return nil
}
