package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Version is the schema version a database is at.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := d.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v)
	return v, err
}

// CopyNote is told how many rows of each table were copied, and how many were left behind because
// they referred to a row that no longer exists (SQLite kept such rows while its checks were off).
type CopyNote func(table string, rows, orphans int)

// Copy copies every table of src into dst, both at this build's schema, dst without any rows. It
// works both ways - SQLite to PostgreSQL and back - and checks the counts at the end.
func Copy(ctx context.Context, src, dst *DB, note CopyNote) error {
	tables, err := Tables()
	if err != nil {
		return err
	}
	sv, err := src.SchemaVersion(ctx)
	if err != nil {
		return fmt.Errorf("reading the source: %w", err)
	}
	dv, err := dst.SchemaVersion(ctx)
	if err != nil {
		return fmt.Errorf("reading the destination: %w", err)
	}
	if sv != Version() || dv != Version() {
		return fmt.Errorf("both databases must be at schema %d (the source is at %d, the destination at %d) - open each with this Meridian first", Version(), sv, dv)
	}
	for _, t := range tables {
		var n int
		if err := dst.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+t.Name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("the destination already has data (%d rows in %s) - use an empty database", n, t.Name)
		}
	}
	present := map[string]map[int64]bool{} // the ids copied, of the tables others refer to
	referred := map[string]bool{}
	for _, t := range tables {
		for _, fk := range t.FKs {
			referred[fk.Parent] = true
		}
	}
	for _, t := range tables {
		rows, orphans, err := copyTable(ctx, src, dst, t, present, referred[t.Name])
		if err != nil {
			return fmt.Errorf("copying %s: %w", t.Name, err)
		}
		if note != nil {
			note(t.Name, rows, orphans)
		}
	}
	if dst.Dialect == Postgres {
		if err := resetIdentities(ctx, dst, tables); err != nil {
			return err
		}
	}
	// the counts must agree (less what was left behind)
	for _, t := range tables {
		var a, b int
		if err := src.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+t.Name).Scan(&a); err != nil {
			return err
		}
		if err := dst.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+t.Name).Scan(&b); err != nil {
			return err
		}
		if b > a {
			return fmt.Errorf("%s: %d rows copied from %d", t.Name, b, a)
		}
	}
	return nil
}

func copyTable(ctx context.Context, src, dst *DB, t Table, present map[string]map[int64]bool, keepIDs bool) (copied, orphans int, err error) {
	names := make([]string, len(t.Columns))
	idCol := -1
	for i, c := range t.Columns {
		names[i] = c.Name
		if c.Name == "id" {
			idCol = i
		}
	}
	fkCol := map[int]string{} // column -> the table it refers to (by id)
	for _, fk := range t.FKs {
		if fk.ParentColumn != "id" {
			continue
		}
		for i, c := range t.Columns {
			if c.Name == fk.Column {
				fkCol[i] = fk.Parent
			}
		}
	}
	if keepIDs && idCol >= 0 {
		present[t.Name] = map[int64]bool{}
	}
	rows, err := src.QueryContext(ctx, `SELECT `+strings.Join(names, ", ")+` FROM `+t.Name)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	per := max(1, min(500, 30000/len(names))) // rows per statement: within both databases' limits
	var batch [][]any
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		one := "(" + strings.TrimSuffix(strings.Repeat("?, ", len(names)), ", ") + ")"
		vals := make([]string, len(batch))
		args := make([]any, 0, len(batch)*len(names))
		for i, r := range batch {
			vals[i] = one
			args = append(args, r...)
		}
		// the leading comment keeps the PostgreSQL driver from asking each new id back
		q := "-- copy\nINSERT INTO " + t.Name + " (" + strings.Join(names, ", ") + ") VALUES " + strings.Join(vals, ", ")
		err := dst.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		})
		batch = batch[:0]
		return err
	}
	for rows.Next() {
		raw := make([]any, len(names))
		ptrs := make([]any, len(names))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return copied, orphans, err
		}
		vals := make([]any, len(names))
		skip := false
		for i, c := range t.Columns {
			v, err := coerce(raw[i], c.Type)
			if err != nil {
				return copied, orphans, fmt.Errorf("column %s: %w", c.Name, err)
			}
			vals[i] = v
			if parent, ok := fkCol[i]; ok && v != nil {
				if ids := present[parent]; ids != nil && !ids[v.(int64)] {
					skip = true // its parent is gone
				}
			}
		}
		if skip {
			orphans++
			continue
		}
		if keepIDs && idCol >= 0 {
			if id, ok := vals[idCol].(int64); ok {
				present[t.Name][id] = true
			}
		}
		batch = append(batch, vals)
		copied++
		if len(batch) >= per {
			if err := flush(); err != nil {
				return copied, orphans, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return copied, orphans, err
	}
	return copied, orphans, flush()
}

// coerce makes a value fit its column's declared type (SQLite keeps what it is given; PostgreSQL
// takes only its type).
func coerce(v any, typ string) (any, error) {
	if v == nil {
		return nil, nil
	}
	if b, ok := v.([]byte); ok {
		v = string(b)
	}
	switch typ {
	case "INTEGER":
		switch x := v.(type) {
		case int64:
			return x, nil
		case float64:
			return int64(math.Round(x)), nil
		case bool:
			if x {
				return int64(1), nil
			}
			return int64(0), nil
		case string:
			s := strings.TrimSpace(x)
			if s == "" {
				return int64(0), nil
			}
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				return n, nil
			}
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				return int64(math.Round(f)), nil
			}
			return nil, fmt.Errorf("%q is not a number", truncateStr(x, 40))
		}
	case "REAL":
		switch x := v.(type) {
		case float64:
			return x, nil
		case int64:
			return float64(x), nil
		case string:
			s := strings.TrimSpace(x)
			if s == "" {
				return float64(0), nil
			}
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return nil, fmt.Errorf("%q is not a number", truncateStr(x, 40))
			}
			return f, nil
		}
	default:
		switch x := v.(type) {
		case string:
			return x, nil
		case int64:
			return strconv.FormatInt(x, 10), nil
		case float64:
			return strconv.FormatFloat(x, 'f', -1, 64), nil
		case bool:
			return strconv.FormatBool(x), nil
		}
	}
	return nil, fmt.Errorf("a value of type %T", v)
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// resetIdentities sets each table's counter past its highest id after rows came with their ids.
func resetIdentities(ctx context.Context, d *DB, tables []Table) error {
	for _, t := range tables {
		if !t.IDKey {
			continue
		}
		if _, err := d.ExecContext(ctx, `SELECT setval(pg_get_serial_sequence('`+t.Name+`', 'id'), COALESCE((SELECT MAX(id) FROM `+t.Name+`), 0) + 1, false)`); err != nil {
			return fmt.Errorf("resetting %s's ids: %w", t.Name, err)
		}
	}
	return nil
}

// Snapshot writes a consistent copy of the database to a new SQLite file at path - what backups
// carry, whichever database the panel runs on.
func (d *DB) Snapshot(ctx context.Context, path string) error {
	if d.Dialect != Postgres {
		_, err := d.ExecContext(ctx, `VACUUM INTO ?`, path)
		return err
	}
	out, err := Open(path)
	if err != nil {
		return err
	}
	if err := out.clearAll(ctx); err != nil { // what the migrations put in a new database
		out.Close()
		return err
	}
	// one transaction's view of PostgreSQL would be best; the panel's writes are few and the copy is
	// short, and counts are checked
	err = Copy(ctx, d, out, nil)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// ImportSQLite replaces everything in a PostgreSQL database with a SQLite file's data (a restored
// backup, or a panel moving to PostgreSQL). The file is brought to this build's schema first.
func (d *DB) ImportSQLite(ctx context.Context, path string, note CopyNote) error {
	if d.Dialect != Postgres {
		return errors.New("ImportSQLite is for PostgreSQL")
	}
	src, err := Open(path)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer src.Close()
	if err := d.clearAll(ctx); err != nil {
		return err
	}
	return Copy(ctx, src, d, note)
}

// clearAll deletes every row of every table (a database about to receive a copy).
func (d *DB) clearAll(ctx context.Context) error {
	tables, err := Tables()
	if err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		if d.Dialect == Postgres {
			names := make([]string, len(tables))
			for i, t := range tables {
				names[i] = t.Name
			}
			_, err := tx.ExecContext(ctx, `TRUNCATE `+strings.Join(names, ", ")+` RESTART IDENTITY CASCADE`)
			return err
		}
		for i := len(tables) - 1; i >= 0; i-- { // children first
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+tables[i].Name); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM id_floor`) // the deletes' own trace
		return err
	})
}
