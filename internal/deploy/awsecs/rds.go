package awsecs

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/flokkit/apptown/internal/deploy"
)

// ProviderOwnedDatabase is RDS's management database. AWS owns it and excludes
// it from its own dump tooling; every other non-template database needs a dump.
const ProviderOwnedDatabase = "rdsadmin"
const appUser = "app"
const appDatabase = "app"
const adminUser = "apptown_admin"

func pgEnv(c deploy.Connection) map[string]string {
	return map[string]string{"PGHOST": c.Host, "PGPORT": strconv.Itoa(c.Port), "PGDATABASE": c.Database, "PGUSER": c.User, "PGPASSWORD": c.Password, "PGSSLMODE": "require"}
}
func (d *Driver) database(ctx context.Context, a *API, c Config, p deploy.Placement, instance string) (rdstypes.DBInstance, error) {
	out, err := a.RDS.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(instance)})
	if err != nil {
		return rdstypes.DBInstance{}, err
	}
	if len(out.DBInstances) != 1 {
		return rdstypes.DBInstance{}, errors.New("AWS returned no RDS instance")
	}
	db := out.DBInstances[0]
	o := deploy.Owned{Kind: "rds-instance", ID: aws.ToString(db.DBInstanceArn), Name: aws.ToString(db.DBInstanceIdentifier), Data: true, Need: "postgres"}
	if instance != o.ID && instance != o.Name {
		return db, errors.New("RDS returned another instance")
	}
	live, err := d.checkOwned(ctx, a, c, p, o)
	if err != nil {
		return db, err
	}
	if !live {
		return db, errors.New("RDS instance is absent")
	}
	return db, nil
}
func (d *Driver) FulfilNeed(ctx context.Context, p deploy.Placement, need string, params map[string]string, log deploy.Logf) (deploy.NeedBinding, map[string]any, error) {
	empty := deploy.NeedBinding{}
	if need != "postgres" {
		return empty, nil, deploy.Terminal(fmt.Errorf("AWS ECS does not provide %s", need))
	}
	for _, k := range []string{"instance", "shared", "sharedDatabase"} {
		if params[k] != "" {
			return empty, nil, deploy.Terminal(errors.New("AWS ECS provides a dedicated RDS instance per app"))
		}
	}
	if d.PostgresSetup == "" {
		return empty, nil, errors.New("the postgres kind has no AWS setup command")
	}
	if d.Needs == nil || d.Credentials == nil {
		return empty, nil, errors.New("AWS needs durable need records and generated credentials")
	}
	version := params["version"]
	if version == "" {
		version = "16"
	}
	if version != "16" && version != "17" {
		return empty, nil, deploy.Terminal(errors.New("AWS ECS provides PostgreSQL 16 and 17"))
	}
	user, database := params["user"], params["database"]
	if user == "" {
		user = appUser
	}
	if database == "" {
		database = appDatabase
	}
	if user == adminUser || user == "postgres" || user == ProviderOwnedDatabase || database == ProviderOwnedDatabase || database == "postgres" || strings.HasPrefix(database, "template") {
		return empty, nil, deploy.Terminal(errors.New("AWS needs an ordinary app user and a database of its own"))
	}
	name := d.name(p)
	requested := map[string]any{"name": name, "database": database, "user": user, "version": version}
	recorded, err := d.Needs.Recorded(ctx, p.ID, need)
	if err != nil {
		return empty, nil, err
	}
	if recorded != nil {
		prior := recorded
		if request, ok := recorded["requested"].(map[string]any); ok {
			prior = request
		}
		if old := text(prior, "version"); old != "" && old != version {
			return empty, nil, deploy.Terminal(errors.New("an RDS instance keeps its PostgreSQL major version; start a new deployment from a copy"))
		}
	}
	if err = d.Needs.RecordIntent(ctx, p.ID, need, requested); err != nil {
		return empty, nil, err
	}
	a, c, e, err := d.Client(ctx)
	if err != nil {
		return empty, nil, err
	}
	group, _, err := d.ensureBase(ctx, a, c, e, p)
	if err != nil {
		return empty, nil, err
	}
	creds, err := d.Credentials(ctx)
	if err != nil {
		return empty, nil, err
	}
	if creds["adminPassword"] == "" || creds["password"] == "" {
		return empty, nil, errors.New("AWS needs both generated database credentials")
	}
	var db rdstypes.DBInstance
	if text(recorded, "id") != "" {
		db, err = d.database(ctx, a, c, p, text(recorded, "id"))
		if err != nil {
			return empty, nil, err
		}
	} else {
		// The intent precedes CreateDBInstance. Only that recorded request may
		// recover a lost answer by the deterministic name, with live tags checked.
		if recorded != nil {
			db, err = d.database(ctx, a, c, p, name)
		} else {
			err = errors.New("not requested yet")
		}
		if err != nil && recorded != nil && !missing(err) {
			return empty, nil, err
		}
		if err != nil {
			tags := []rdstypes.Tag{}
			for _, t := range d.tags(p) {
				tags = append(tags, rdstypes.Tag{Key: t.Key, Value: t.Value})
			}
			dbGroup, err := d.ensureDBNetwork(ctx, a, c, e, p, group)
			if err != nil {
				return empty, nil, err
			}
			subnets := c.DatabaseSubnets
			if len(subnets) == 0 {
				subnets = c.TaskSubnets
			}
			existing, err := d.Owned(ctx)
			if err != nil {
				return empty, nil, err
			}
			haveSubnet := false
			for _, o := range existing {
				if o.Kind == "rds-subnet-group" && o.Name == name {
					live, err := d.checkOwned(ctx, a, c, p, o)
					if err != nil {
						return empty, nil, err
					}
					haveSubnet = live
				}
			}
			if !haveSubnet {
				subnet, err := a.RDS.CreateDBSubnetGroup(ctx, &rds.CreateDBSubnetGroupInput{DBSubnetGroupName: aws.String(name), DBSubnetGroupDescription: aws.String("AppTown deployment database"), SubnetIds: subnets, Tags: tags})
				if err != nil {
					return empty, nil, err
				}
				if subnet.DBSubnetGroup == nil {
					return empty, nil, errors.New("AWS returned no DB subnet group")
				}
				if err := d.record(ctx, deploy.Owned{Kind: "rds-subnet-group", ID: aws.ToString(subnet.DBSubnetGroup.DBSubnetGroupArn), Name: name, Data: true, Need: need}); err != nil {
					return empty, nil, err
				}
			}

			made, err := a.RDS.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{DBInstanceIdentifier: aws.String(name), DBInstanceClass: aws.String("db.t4g.micro"), Engine: aws.String("postgres"), EngineVersion: aws.String(version), AllocatedStorage: aws.Int32(20), StorageType: aws.String("gp3"), StorageEncrypted: aws.Bool(true), PubliclyAccessible: aws.Bool(false), MasterUsername: aws.String(adminUser), MasterUserPassword: aws.String(creds["adminPassword"]), DBSubnetGroupName: aws.String(name), VpcSecurityGroupIds: []string{dbGroup}, BackupRetentionPeriod: aws.Int32(1), Tags: tags})
			if err != nil {
				return empty, nil, err
			}
			if made.DBInstance == nil {
				return empty, nil, errors.New("AWS returned no RDS instance")
			}
			db = *made.DBInstance
		}
		if err = d.Needs.RecordIdentity(context.WithoutCancel(ctx), p.ID, need, requested, aws.ToString(db.DBInstanceArn), name); err != nil {
			return empty, nil, err
		}
	}
	wait, cancel := context.WithTimeout(ctx, d.JobTimeout)
	defer cancel()
	for aws.ToString(db.DBInstanceStatus) != "available" {
		select {
		case <-wait.Done():
			return empty, nil, wait.Err()
		case <-time.After(d.PollInterval):
		}
		db, err = d.database(wait, a, c, p, name)
		if err != nil {
			return empty, nil, err
		}
	}
	if db.Endpoint == nil {
		return empty, nil, errors.New("RDS instance has no endpoint")
	}
	admin := deploy.Connection{Host: aws.ToString(db.Endpoint.Address), Port: int(aws.ToInt32(db.Endpoint.Port)), User: adminUser, Password: creds["adminPassword"], Database: "postgres", SSLMode: "require"}
	env := pgEnv(admin)
	env["APPTOWN_DB_PASSWORD"] = creds["password"]
	// psql variables quote generated credentials as SQL literals; no credential
	// is interpolated into the shell command or task definition.
	env["APPTOWN_DB_USER"], env["APPTOWN_DB_NAME"] = user, database
	script := d.PostgresSetup
	if script == "" {
		return empty, nil, errors.New("the postgres kind has no AWS setup command")
	}
	if err = d.runJob(ctx, p, "postgres:"+version+"-alpine", script, env); err != nil {
		return empty, nil, err
	}
	conn := admin
	conn.User = user
	conn.Password = creds["password"]
	conn.Database = database
	vars := pgEnv(conn)
	vars["DATABASE_URL"] = (&url.URL{Scheme: "postgresql", User: url.UserPassword(conn.User, conn.Password), Host: fmt.Sprintf("%s:%d", conn.Host, conn.Port), Path: "/" + conn.Database, RawQuery: "sslmode=require"}).String()
	handle := map[string]any{"id": aws.ToString(db.DBInstanceArn), "name": name, "database": database, "user": user, "version": version}
	return deploy.NeedBinding{Secret: vars, Native: handle}, handle, nil
}
func (d *Driver) ensureDBNetwork(ctx context.Context, a *API, c Config, e Estate, p deploy.Placement, appGroup string) (string, error) {
	owned, err := d.Owned(ctx)
	if err != nil {
		return "", err
	}
	for _, o := range owned {
		if o.Kind == "security-group" && o.Data {
			live, err := d.checkOwned(ctx, a, c, p, o)
			if err != nil {
				return "", err
			}
			if live {
				return o.ID, nil
			}
		}
	}
	tags := []ec2types.Tag{}
	for _, t := range d.tags(p) {
		tags = append(tags, ec2types.Tag{Key: t.Key, Value: t.Value})
	}
	made, err := a.EC2.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{GroupName: aws.String(d.name(p) + "-db"), Description: aws.String("AppTown database; deployment tasks only"), VpcId: aws.String(e.VPC), TagSpecifications: []ec2types.TagSpecification{{ResourceType: ec2types.ResourceTypeSecurityGroup, Tags: tags}}})
	if err != nil {
		return "", err
	}
	id := aws.ToString(made.GroupId)
	if err = d.record(ctx, deploy.Owned{Kind: "security-group", ID: id, Name: d.name(p) + "-db", Data: true, Need: "postgres"}); err != nil {
		return "", err
	}
	_, err = a.EC2.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{GroupId: aws.String(id), IpPermissions: []ec2types.IpPermission{{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(5432), ToPort: aws.Int32(5432), UserIdGroupPairs: []ec2types.UserIdGroupPair{{GroupId: aws.String(appGroup)}}}}})
	return id, err
}

// ManagedData resolves only recorded instances. Database enumeration belongs to
// the backup set's verified manifest, since Fargate has no result channel.
type ManagedData struct {
	Resolve func(context.Context, string) (*Driver, deploy.Placement, error)
}

func (m ManagedData) AppConnection(ctx context.Context, instance, database, user string) (deploy.Connection, error) {
	d, p, err := m.Resolve(ctx, instance)
	if err != nil {
		return deploy.Connection{}, err
	}
	a, c, _, err := d.Client(ctx)
	if err != nil {
		return deploy.Connection{}, err
	}
	db, err := d.database(ctx, a, c, p, instance)
	if err != nil {
		return deploy.Connection{}, err
	}
	if db.Endpoint == nil {
		return deploy.Connection{}, errors.New("RDS endpoint is not available")
	}
	creds, err := d.Credentials(ctx)
	if err != nil {
		return deploy.Connection{}, err
	}
	recorded, err := d.Needs.Recorded(ctx, p.ID, "postgres")
	if err != nil {
		return deploy.Connection{}, err
	}
	expected := text(recorded, "user")
	if expected == "" {
		if request, ok := recorded["requested"].(map[string]any); ok {
			expected = text(request, "user")
		}
	}
	if user != expected || expected == "" {
		return deploy.Connection{}, errors.New("AWS copy must use the app user")
	}
	return deploy.Connection{Host: aws.ToString(db.Endpoint.Address), Port: int(aws.ToInt32(db.Endpoint.Port)), Database: database, User: user, Password: creds["password"], SSLMode: "require"}, nil
}
func (m ManagedData) Databases(context.Context, string) ([]string, error) {
	return nil, errors.New("RDS databases must be read from the final backup set's verified manifest")
}
func sqlName(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func (m ManagedData) adminJob(ctx context.Context, instance, command string) error {
	d, p, err := m.Resolve(ctx, instance)
	if err != nil {
		return err
	}
	a, c, _, err := d.Client(ctx)
	if err != nil {
		return err
	}
	db, err := d.database(ctx, a, c, p, instance)
	if err != nil {
		return err
	}
	if db.Endpoint == nil {
		return errors.New("RDS endpoint is not available")
	}
	conn := deploy.Connection{Host: aws.ToString(db.Endpoint.Address), Port: int(aws.ToInt32(db.Endpoint.Port)), Database: "postgres", SSLMode: "require"}
	creds, err := d.Credentials(ctx)
	if err != nil {
		return err
	}
	conn.User = adminUser
	conn.Password = creds["adminPassword"]
	env := pgEnv(conn)
	env["APPTOWN_SQL"] = command
	return d.runJob(ctx, p, "postgres:16-alpine", `set -eu
printf '%s\n' "$APPTOWN_SQL" | psql -v ON_ERROR_STOP=1`, env)
}
func (m ManagedData) NewDatabase(ctx context.Context, instance, name, owner string) error {
	if _, err := m.AppConnection(ctx, instance, "postgres", owner); err != nil {
		return err
	}
	return m.adminJob(ctx, instance, "CREATE DATABASE "+sqlName(name)+" OWNER "+sqlName(owner)+"; REVOKE ALL ON DATABASE "+sqlName(name)+" FROM PUBLIC")
}
func (m ManagedData) DropDatabase(ctx context.Context, instance, name string) error {
	return m.adminJob(ctx, instance, "DROP DATABASE "+sqlName(name)+" WITH (FORCE)")
}
func (m ManagedData) Backups(ctx context.Context, instance string) ([]deploy.NativeBackup, error) {
	d, p, err := m.Resolve(ctx, instance)
	if err != nil {
		return nil, err
	}
	a, c, _, err := d.Client(ctx)
	if err != nil {
		return nil, err
	}
	db, err := d.database(ctx, a, c, p, instance)
	if err != nil {
		return nil, err
	}
	out, err := a.RDS.DescribeDBSnapshots(ctx, &rds.DescribeDBSnapshotsInput{DBInstanceIdentifier: db.DBInstanceIdentifier, SnapshotType: aws.String("automated")})
	if err != nil {
		return nil, err
	}
	var answer []deploy.NativeBackup
	for _, b := range out.DBSnapshots {
		if b.SnapshotCreateTime != nil {
			answer = append(answer, deploy.NativeBackup{CreatedAt: *b.SnapshotCreateTime, SizeGB: float64(aws.ToInt32(b.AllocatedStorage))})
		}
	}
	return answer, nil
}
func (m ManagedData) DeleteInstance(ctx context.Context, instance string) error {
	d, p, err := m.Resolve(ctx, instance)
	if err != nil {
		return err
	}
	a, c, _, err := d.Client(ctx)
	if err != nil {
		return err
	}
	db, err := d.database(ctx, a, c, p, instance)
	if err != nil {
		return err
	}
	_, err = a.RDS.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{DBInstanceIdentifier: db.DBInstanceIdentifier, SkipFinalSnapshot: aws.Bool(true), DeleteAutomatedBackups: aws.Bool(true)})
	if err != nil {
		return err
	}
	for {
		_, err = a.RDS.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: db.DBInstanceIdentifier})
		if missing(err) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.PollInterval):
		}
	}
}
