package dbtest

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"flokkit.io/forge/internal/database"
)

// Row is one table row as a release left it: the table and the row as JSON
// (row_to_json), so a later build can put it back without that release's code.
type Row struct {
	Table string          `json:"table"`
	Row   json.RawMessage `json:"row"`
}

func quoteIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func quoteLiteral(s string) string { return `'` + strings.ReplaceAll(s, `'`, `''`) + `'` }

type column struct {
	name, typ string
	json      bool
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// tableColumns names a table's columns in order, with their types; written
// leaves out generated columns, which compute themselves.
func tableColumns(ctx context.Context, q querier, table string, written bool) ([]column, error) {
	query := `SELECT column_name, udt_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1`
	if written {
		query += ` AND is_generated = 'NEVER'`
	}
	got, err := q.QueryContext(ctx, query+` ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	var out []column
	for got.Next() {
		var c column
		if err := got.Scan(&c.name, &c.typ); err != nil {
			_ = got.Close()
			return nil, err
		}
		c.json = c.typ == "json" || c.typ == "jsonb"
		out = append(out, c)
	}
	return out, got.Close()
}

// Snapshot reads every row of every table in the public schema, sorted, except
// the tables skip names. It is how an upgrade fixture freezes a release's
// database; Restore puts it back.
func Snapshot(ctx context.Context, db *database.DB, skip func(table string) bool) ([]Row, error) {
	rows, err := db.Pool().QueryContext(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if skip == nil || !skip(t) {
			tables = append(tables, t)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var out []Row
	for _, t := range tables {
		cols, err := tableColumns(ctx, db.Pool(), t, false)
		if err != nil {
			return nil, err
		}
		// A JSON column is kept as its text: row_to_json would write a stored
		// JSON null and an SQL NULL alike, and Restore must tell them apart.
		var list []string
		for _, c := range cols {
			if c.json {
				list = append(list, quoteIdent(c.name)+`::text AS `+quoteIdent(c.name))
			} else {
				list = append(list, quoteIdent(c.name))
			}
		}
		got, err := db.Pool().QueryContext(ctx, `SELECT row_to_json(r)::text FROM (SELECT `+strings.Join(list, ", ")+` FROM `+quoteIdent(t)+`) r`)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", t, err)
		}
		var lines []string
		for got.Next() {
			var line string
			if err := got.Scan(&line); err != nil {
				_ = got.Close()
				return nil, err
			}
			lines = append(lines, line)
		}
		if err := got.Close(); err != nil {
			return nil, err
		}
		sort.Strings(lines) // a stable fixture: the same rows read the same
		for _, l := range lines {
			out = append(out, Row{Table: t, Row: json.RawMessage(l)})
		}
	}
	return out, nil
}

// Restore makes db hold a release's database: its public schema emptied, the
// release's own schema run, and its rows put back with triggers, foreign-key
// checks among them, switched off while they load (session_replication_role).
// That is a load exemption, not a deferral: nothing checks the rows
// afterwards, so they must be what Snapshot read from a consistent database.
// A generated column is left to compute itself.
func Restore(ctx context.Context, db *database.DB, schemaSQL string, rows []Row) error {
	if _, err := db.Pool().ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		return fmt.Errorf("emptying the database: %w", err)
	}
	if _, err := db.Pool().ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("the release's schema: %w", err)
	}
	tx, err := db.Pool().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		return fmt.Errorf("holding off foreign keys: %w", err)
	}
	statements := map[string]string{}
	for _, r := range rows {
		stmt, ok := statements[r.Table]
		if !ok {
			cols, err := tableColumns(ctx, tx, r.Table, true)
			if err != nil {
				return err
			}
			if len(cols) == 0 {
				return fmt.Errorf("the release's schema has no table %s", r.Table)
			}
			t := quoteIdent(r.Table)
			var names, values []string
			for _, c := range cols {
				names = append(names, quoteIdent(c.name))
				if c.json {
					values = append(values, `($1::json->>`+quoteLiteral(c.name)+`)::`+c.typ)
				} else {
					values = append(values, `p.`+quoteIdent(c.name))
				}
			}
			stmt = `INSERT INTO ` + t + ` (` + strings.Join(names, ", ") + `) OVERRIDING SYSTEM VALUE SELECT ` + strings.Join(values, ", ") + ` FROM json_populate_record(NULL::` + t + `, $1::json) p`
			statements[r.Table] = stmt
		}
		if _, err := tx.ExecContext(ctx, stmt, string(r.Row)); err != nil {
			return fmt.Errorf("putting back a row of %s: %w", r.Table, err)
		}
	}
	return tx.Commit()
}

// Shape is a database's public schema as text: every column with its type,
// nullability, default and generation; every constraint with its definition;
// every index with its definition. Two databases with the same Shape have the
// same schema, whatever order their objects were made in.
func Shape(ctx context.Context, db *database.DB) (string, error) {
	var lines []string
	queries := []string{
		`SELECT table_name || '.' || column_name || ' ' || udt_name || ' null=' || is_nullable || ' default=' || COALESCE(column_default, '') || ' generated=' || is_generated || ' ' || COALESCE(generation_expression, '') FROM information_schema.columns WHERE table_schema = 'public'`,
		`SELECT c.conrelid::regclass::text || ' constraint ' || c.conname || ' ' || pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace WHERE n.nspname = 'public'`,
		`SELECT tablename || ' index ' || indexname || ' ' || indexdef FROM pg_indexes WHERE schemaname = 'public'`,
	}
	for _, q := range queries {
		got, err := db.Pool().QueryContext(ctx, q)
		if err != nil {
			return "", err
		}
		for got.Next() {
			var l string
			if err := got.Scan(&l); err != nil {
				_ = got.Close()
				return "", err
			}
			lines = append(lines, l)
		}
		if err := got.Close(); err != nil {
			return "", err
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n"), nil
}

// WriteRows keeps rows at path as gzipped JSON lines, one row each.
func WriteRows(path string, rows []Row) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	enc := json.NewEncoder(gz)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			_ = f.Close()
			return err
		}
	}
	return errors.Join(gz.Close(), f.Close())
}

// ReadRows reads rows WriteRows kept.
func ReadRows(path string) ([]Row, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	var rows []Row
	dec := json.NewDecoder(gz)
	for dec.More() {
		var r Row
		if err := dec.Decode(&r); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		rows = append(rows, r)
	}
	return rows, nil
}
