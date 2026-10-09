package db

// PostgreSQL: the panel's database on new installs. The panel's SQL is written for SQLite; on
// PostgreSQL a driver wrapped around pgx's translates each statement as it is sent (placeholders,
// SQLite's functions, case-insensitive names, booleans as 0/1, the new row's id) and the schema is
// built from SQLite's own - the migrations replayed in memory and read back table by table - so
// the two never drift apart. One copier (copy.go) moves the data between them both ways, and backups
// are always a SQLite file, which either restores.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
)

// Dialects.
const (
	SQLite   = "sqlite"
	Postgres = "postgres"
)

const pgDriverName = "meridian-postgres"

var registerPG sync.Once

// OpenPostgres opens a PostgreSQL database (a postgres:// URL or key=value settings) and brings its
// schema to this build's version.
func OpenPostgres(dsn string) (*DB, error) {
	registerPG.Do(func() { sql.Register(pgDriverName, &pgDriver{inner: stdlib.GetDefaultDriver()}) })
	sqldb, err := sql.Open(pgDriverName, dsn)
	if err != nil {
		return nil, err
	}
	sqldb.SetMaxOpenConns(16)
	sqldb.SetMaxIdleConns(4)
	sqldb.SetConnMaxIdleTime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := sqldb.PingContext(ctx); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("cannot reach PostgreSQL: %w", err)
	}
	d := &DB{DB: sqldb, Dialect: Postgres}
	if err := d.migratePostgres(); err != nil {
		sqldb.Close()
		return nil, err
	}
	return d, nil
}

// migratePostgres creates the schema on an empty database, at this build's version; a database made
// by an older build gets the newer migrations, translated.
func (d *DB) migratePostgres() error {
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version BIGINT NOT NULL)`); err != nil {
		return err
	}
	var v int
	if err := d.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("the database is from a newer Meridian (schema %d; this one knows %d) - install that version again", v, len(migrations))
	}
	if v == 0 {
		s, err := pgSchema()
		if err != nil {
			return err
		}
		return d.Write(context.Background(), func(tx *sql.Tx) error {
			for _, st := range s.ddl {
				if _, err := tx.Exec(st); err != nil {
					return fmt.Errorf("creating the schema: %w (%s)", err, firstLine(st))
				}
			}
			_, err := tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, len(migrations))
			return err
		})
	}
	for i := v; i < len(migrations); i++ {
		stmts, err := pgMigration(i)
		if err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		err = d.Write(context.Background(), func(tx *sql.Tx) error {
			for _, st := range stmts {
				if _, err := tx.Exec(st); err != nil {
					return fmt.Errorf("%w (%s)", err, firstLine(st))
				}
			}
			_, err := tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, i+1)
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	return nil
}

// pgBase is the migration the PostgreSQL schema starts from: databases made by this build begin at
// its version; later migrations are translated (pgMigration).
const pgBase = 23

// pgMigrations are migrations written for PostgreSQL by hand, where translating SQLite's does not do.
var pgMigrations = map[int][]string{}

// pgMigration is migration i (0-based) for PostgreSQL: written by hand, or translated - adding
// columns, tables and indexes, and plain data changes.
func pgMigration(i int) ([]string, error) {
	if i < pgBase {
		return nil, errors.New("this database's schema is older than PostgreSQL support - move the data again from SQLite")
	}
	if m, ok := pgMigrations[i+1]; ok {
		return m, nil
	}
	var out []string
	for _, st := range splitStatements(migrations[i]) {
		up := strings.ToUpper(strings.TrimSpace(st))
		if strings.HasPrefix(up, "CREATE TRIGGER") || strings.Contains(up, "TEMP TABLE") {
			return nil, errors.New("needs a hand-written PostgreSQL version (pgMigrations)")
		}
		out = append(out, translateDDL(st))
	}
	return out, nil
}

var (
	ddlInteger = regexp.MustCompile(`(?i)\bINTEGER\b`)
	ddlReal    = regexp.MustCompile(`(?i)\bREAL\b`)
	ddlBlob    = regexp.MustCompile(`(?i)\bBLOB\b`)
	ddlPK      = regexp.MustCompile(`(?i)\bBIGINT\s+PRIMARY\s+KEY\b`)
	ddlNocase  = regexp.MustCompile(`(?i)(\w+)\s+COLLATE\s+NOCASE`)
)

// translateDDL turns SQLite's column types into PostgreSQL's (an INTEGER PRIMARY KEY counts by
// itself). A NOCASE index key becomes lower(column); elsewhere NOCASE is dropped (queries that need
// it say so themselves, and are translated).
func translateDDL(st string) string {
	st = ddlInteger.ReplaceAllString(st, "BIGINT")
	st = ddlReal.ReplaceAllString(st, "DOUBLE PRECISION")
	st = ddlBlob.ReplaceAllString(st, "BYTEA")
	st = ddlPK.ReplaceAllString(st, "BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY")
	if up := strings.ToUpper(strings.TrimSpace(st)); strings.HasPrefix(up, "CREATE INDEX") || strings.HasPrefix(up, "CREATE UNIQUE INDEX") {
		return ddlNocase.ReplaceAllString(st, "lower($1)")
	}
	return regexp.MustCompile(`(?i)\s+COLLATE\s+NOCASE`).ReplaceAllString(st, "")
}

// splitStatements splits a migration at the semicolons that end its statements.
func splitStatements(s string) []string {
	var out []string
	for _, st := range strings.Split(s, ";\n") {
		if st = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(st), ";")); st != "" && !strings.HasPrefix(st, "--") {
			out = append(out, st)
		}
	}
	return out
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 100 {
		s = s[:100] + "…"
	}
	return s
}

// ---------------------------------------------------------------- the driver

// pgDriver wraps pgx's database/sql driver: every statement is translated before it is sent.
type pgDriver struct{ inner driver.Driver }

func (d *pgDriver) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &pgConn{c: c}, nil
}

type pgConn struct{ c driver.Conn }

var (
	_ driver.ConnBeginTx        = (*pgConn)(nil)
	_ driver.ConnPrepareContext = (*pgConn)(nil)
	_ driver.ExecerContext      = (*pgConn)(nil)
	_ driver.QueryerContext     = (*pgConn)(nil)
	_ driver.Pinger             = (*pgConn)(nil)
	_ driver.SessionResetter    = (*pgConn)(nil)
	_ driver.Validator          = (*pgConn)(nil)
	_ driver.NamedValueChecker  = (*pgConn)(nil)
)

func (c *pgConn) Prepare(q string) (driver.Stmt, error) {
	return c.c.Prepare(rewrite(q).sql)
}

func (c *pgConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	return c.c.(driver.ConnPrepareContext).PrepareContext(ctx, rewrite(q).sql)
}

func (c *pgConn) Close() error { return c.c.Close() }

func (c *pgConn) Begin() (driver.Tx, error) {
	return c.c.(driver.ConnBeginTx).BeginTx(context.Background(), driver.TxOptions{})
}

func (c *pgConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	return c.c.(driver.ConnBeginTx).BeginTx(ctx, o)
}

func (c *pgConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	rq := rewrite(q)
	if !rq.returning {
		res, err := c.c.(driver.ExecerContext).ExecContext(ctx, rq.sql, args)
		if err != nil && pgDebug {
			slog.Warn("postgres statement failed", "err", err, "sql", rq.sql)
		}
		return res, err
	}
	// an insert into a table with an id: the new row's id comes back, as SQLite's last_insert_rowid
	rows, err := c.c.(driver.QueryerContext).QueryContext(ctx, rq.sql+" RETURNING id", args)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res pgResult
	vals := make([]driver.Value, 1)
	for {
		if err := rows.Next(vals); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		res.rows++
		if id, ok := vals[0].(int64); ok {
			res.id = id
		}
	}
	return res, nil
}

func (c *pgConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	rq := rewrite(q).sql
	rows, err := c.c.(driver.QueryerContext).QueryContext(ctx, rq, args)
	if pgDebug {
		if err != nil {
			slog.Warn("postgres query failed", "err", err, "sql", rq)
		} else if ct, ok := rows.(driver.RowsColumnTypeDatabaseTypeName); ok {
			for i, name := range rows.Columns() {
				if ct.ColumnTypeDatabaseTypeName(i) == "BOOL" {
					slog.Warn("postgres query returns a boolean (SQLite gave 0/1)", "column", name, "sql", rq)
				}
			}
		}
	}
	return rows, err
}

// pgDebug (MERIDIAN_DB_DEBUG=1) logs every statement PostgreSQL refuses and every boolean result -
// for tests, which run the panel's statements on PostgreSQL.
var pgDebug = os.Getenv("MERIDIAN_DB_DEBUG") != ""

func (c *pgConn) Ping(ctx context.Context) error {
	if p, ok := c.c.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *pgConn) ResetSession(ctx context.Context) error {
	if r, ok := c.c.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *pgConn) IsValid() bool {
	if v, ok := c.c.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

// CheckNamedValue sends booleans as 0 and 1, as the schema stores them (and SQLite did).
func (c *pgConn) CheckNamedValue(nv *driver.NamedValue) error {
	if b, ok := nv.Value.(bool); ok {
		nv.Value = int64(0)
		if b {
			nv.Value = int64(1)
		}
		return nil
	}
	if ch, ok := c.c.(driver.NamedValueChecker); ok {
		return ch.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

type pgResult struct{ id, rows int64 }

func (r pgResult) LastInsertId() (int64, error) { return r.id, nil }
func (r pgResult) RowsAffected() (int64, error) { return r.rows, nil }

// ---------------------------------------------------------------- translating statements

type rewritten struct {
	sql       string
	returning bool // an insert whose new id is wanted (RETURNING id is added)
}

var rewrites sync.Map // statement -> rewritten

var (
	reEqNocase    = regexp.MustCompile(`(?i)([\w.]+)\s*=\s*\?\s+COLLATE\s+NOCASE`)
	reNocase      = regexp.MustCompile(`(?i)([\w.]+)\s+COLLATE\s+NOCASE`)
	reUsername    = regexp.MustCompile(`(?i)(^|[^\w.])((?:\w+\.)?username)\s*=\s*\?`)
	reGroupConcat = regexp.MustCompile(`(?i)GROUP_CONCAT\(\s*(DISTINCT\s+)?([^(),]+?)\s*\)`)
	reJSONExtract = regexp.MustCompile(`(?i)json_extract\(\s*([\w.]+)\s*,\s*'\$\.(\w+)'\s*\)`)
	reJSONEach    = regexp.MustCompile(`(?i)json_each\(\s*\?\s*\)`)
	reLike        = regexp.MustCompile(`(?i)\s+LIKE\s+`)
	reCastInt     = regexp.MustCompile(`(?i)\bAS\s+INTEGER\s*\)`)
	reCastReal    = regexp.MustCompile(`(?i)\bAS\s+REAL\s*\)`)
	reInsert      = regexp.MustCompile(`(?is)^\s*INSERT\s+INTO\s+(\w+)\b`)
	reDoUpdate    = regexp.MustCompile(`(?is)\bON\s+CONFLICT\s*\([^)]*\)\s*DO\s+UPDATE\s+SET\s+`)
	reReturning   = regexp.MustCompile(`(?i)\bRETURNING\b`)
)

// rewrite translates one statement from SQLite's SQL to PostgreSQL's (cached: the panel's
// statements are few and fixed).
func rewrite(q string) rewritten {
	if r, ok := rewrites.Load(q); ok {
		return r.(rewritten)
	}
	s := q
	s = reEqNocase.ReplaceAllString(s, "lower($1) = lower(?)")
	s = reNocase.ReplaceAllString(s, "lower($1)")
	s = reUsername.ReplaceAllString(s, "${1}lower($2) = lower(?)") // accounts.username: case-insensitive (COLLATE NOCASE)
	s = reGroupConcat.ReplaceAllString(s, "string_agg(${1}CAST($2 AS TEXT), ',')")
	s = reJSONExtract.ReplaceAllString(s, "(meridian_json($1) ->> '$2')")
	s = reJSONEach.ReplaceAllString(s, "jsonb_array_elements_text(CAST(? AS jsonb))")
	s = reLike.ReplaceAllString(s, " ILIKE ") // SQLite's LIKE ignores case
	s = reCastInt.ReplaceAllString(s, "AS BIGINT)")
	s = reCastReal.ReplaceAllString(s, "AS DOUBLE PRECISION)")
	s = scalarMinMax(s) // MAX(a, b) is never an aggregate: those take one value
	s = upsert(s)
	r := rewritten{sql: placeholders(s)}
	if m := reInsert.FindStringSubmatch(s); m != nil && idTable(strings.ToLower(m[1])) && !reReturning.MatchString(s) {
		r.returning = true
	}
	rewrites.Store(q, r)
	return r
}

// placeholders numbers the ? placeholders ($1, $2 ...), outside quotes and comments.
func placeholders(s string) string {
	var b strings.Builder
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(s) {
				if s[j] == c {
					if j+1 < len(s) && s[j+1] == c { // a doubled quote inside
						j += 2
						continue
					}
					break
				}
				j++
			}
			b.WriteString(s[i:min(j+1, len(s))])
			i = j
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				j = len(s) - i
			}
			b.WriteString(s[i : i+j])
			i += j - 1
		case c == '?':
			n++
			fmt.Fprintf(&b, "$%d", n)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// upsert translates an INSERT's ON CONFLICT ... DO UPDATE SET: PostgreSQL wants the row already
// there named by its table (SQLite takes a bare column), and has GREATEST and LEAST where SQLite
// takes MAX and MIN of two values.
func upsert(s string) string {
	m := reInsert.FindStringSubmatch(s)
	loc := reDoUpdate.FindStringIndex(s)
	if m == nil || loc == nil {
		return s
	}
	table := m[1]
	cols := map[string]bool{}
	if sc, err := pgSchema(); err == nil {
		for _, t := range sc.tables {
			if strings.EqualFold(t.Name, table) {
				for _, c := range t.Columns {
					cols[strings.ToLower(c.Name)] = true
				}
			}
		}
	}
	head, set := s[:loc[1]], s[loc[1]:]
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(set); i++ {
		switch set[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, set[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, set[start:])
	for i, a := range parts {
		lhs, rhs, ok := strings.Cut(a, "=")
		if !ok {
			continue
		}
		rhs = scalarMinMax(rhs)
		parts[i] = lhs + "=" + qualify(rhs, table, cols)
	}
	return head + strings.Join(parts, ",")
}

// scalarMinMax turns MAX(a, b) and MIN(a, b) - two values, not an aggregate - into GREATEST and LEAST.
func scalarMinMax(s string) string {
	up := strings.ToUpper(s)
	var b strings.Builder
	for i := 0; i < len(s); {
		fn := ""
		if strings.HasPrefix(up[i:], "MAX(") {
			fn = "GREATEST("
		} else if strings.HasPrefix(up[i:], "MIN(") {
			fn = "LEAST("
		}
		if fn != "" && (i == 0 || !isIdent(s[i-1])) {
			// two values or more at the top level of its parentheses: scalar
			depth, comma := 0, false
			for j := i + 3; j < len(s); j++ {
				if s[j] == '(' {
					depth++
				} else if s[j] == ')' {
					if depth--; depth == 0 {
						break
					}
				} else if s[j] == ',' && depth == 1 {
					comma = true
				}
			}
			if comma {
				b.WriteString(fn)
				i += 4
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// qualify names the table on its columns in an expression (excluded.x and functions stay).
func qualify(expr, table string, cols map[string]bool) string {
	var b strings.Builder
	for i := 0; i < len(expr); {
		c := expr[i]
		if c == '\'' { // a string literal, as it is
			j := strings.IndexByte(expr[i+1:], '\'')
			if j < 0 {
				b.WriteString(expr[i:])
				break
			}
			b.WriteString(expr[i : i+j+2])
			i += j + 2
			continue
		}
		if isIdent(c) && (c < '0' || c > '9') && (i == 0 || (!isIdent(expr[i-1]) && expr[i-1] != '.')) {
			j := i
			for j < len(expr) && isIdent(expr[j]) {
				j++
			}
			word := expr[i:j]
			k := j
			for k < len(expr) && expr[k] == ' ' {
				k++
			}
			if cols[strings.ToLower(word)] && (k >= len(expr) || (expr[k] != '(' && expr[k] != '.')) {
				b.WriteString(table + "." + word)
			} else {
				b.WriteString(word)
			}
			i = j
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func isIdent(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
