package db

import (
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// The PostgreSQL schema is SQLite's, read back: the migrations run on an empty in-memory SQLite
// database, and each table's columns, keys, checks and indexes are written out for PostgreSQL.

// Column is one column of a table, as both databases know it.
type Column struct {
	Name    string
	Type    string // INTEGER, REAL or TEXT (SQLite's declared type)
	NotNull bool
	Default string // as SQLite has it, "" = none
	PK      int    // position in the primary key, 0 = not in it
}

// FK is a foreign key.
type FK struct {
	Column, Parent, ParentColumn, OnDelete string
}

// Table is a table's shape.
type Table struct {
	Name    string
	Columns []Column
	FKs     []FK
	IDKey   bool // its primary key is one INTEGER column named id: new rows count up by themselves
	checks  []string
	indexes []string // PostgreSQL CREATE INDEX statements
}

type schema struct {
	tables []Table // parents before the tables that refer to them
	ddl    []string
}

var (
	schemaOnce sync.Once
	schemaVal  *schema
	schemaErr  error
)

// pgSchema reads the schema back from SQLite (once).
func pgSchema() (*schema, error) {
	schemaOnce.Do(func() { schemaVal, schemaErr = readSchema() })
	return schemaVal, schemaErr
}

// Tables are the panel's tables in an order that puts every table after the tables it refers to
// (the copier's order).
func Tables() ([]Table, error) {
	s, err := pgSchema()
	if err != nil {
		return nil, err
	}
	return s.tables, nil
}

// idTable says whether a table's new rows get their id by themselves (an insert returns it).
func idTable(name string) bool {
	s, err := pgSchema()
	if err != nil {
		return false
	}
	for _, t := range s.tables {
		if t.Name == name {
			return t.IDKey
		}
	}
	return false
}

var reCheck = regexp.MustCompile(`(?i)\bCHECK\s*\(`)

func readSchema() (*schema, error) {
	mem, err := sql.Open("sqlite", "file:meridian-schema?mode=memory&cache=private")
	if err != nil {
		return nil, err
	}
	defer mem.Close()
	mem.SetMaxOpenConns(1) // an in-memory database lives in its one connection
	m := &DB{DB: mem, Dialect: SQLite}
	if err := m.migrate(); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	rows, err := mem.Query(`SELECT name, sql FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name != 'schema_version' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	type raw struct{ name, sql string }
	var list []raw
	for rows.Next() {
		var r raw
		if err := rows.Scan(&r.name, &r.sql); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, r)
	}
	rows.Close()
	var tables []Table
	for _, r := range list {
		t, err := readTable(mem, r.name, r.sql)
		if err != nil {
			return nil, fmt.Errorf("schema of %s: %w", r.name, err)
		}
		tables = append(tables, t)
	}
	// the triggers SQLite has must be the ones written for PostgreSQL below
	var triggers []string
	trows, err := mem.Query(`SELECT name FROM sqlite_master WHERE type = 'trigger' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	for trows.Next() {
		var n string
		_ = trows.Scan(&n)
		triggers = append(triggers, n)
	}
	trows.Close()
	if strings.Join(triggers, ",") != "nodes_id_floor,subs_id_floor" {
		return nil, fmt.Errorf("schema: SQLite has the triggers %v - PostgreSQL's must be written for them", triggers)
	}
	tables, err = parentsFirst(tables)
	if err != nil {
		return nil, err
	}
	s := &schema{tables: tables}
	for _, t := range tables {
		s.ddl = append(s.ddl, t.createSQL())
	}
	for _, t := range tables {
		s.ddl = append(s.ddl, t.indexes...)
	}
	for _, t := range tables {
		for _, fk := range t.FKs {
			s.ddl = append(s.ddl, fmt.Sprintf(`ALTER TABLE %s ADD CONSTRAINT %s_%s_fkey FOREIGN KEY (%s) REFERENCES %s (%s) ON DELETE %s`,
				t.Name, t.Name, fk.Column, fk.Column, fk.Parent, fk.ParentColumn, fk.OnDelete))
		}
	}
	// SQLite's json_extract reads what is not JSON as nothing; PostgreSQL's cast would fail the query
	s.ddl = append(s.ddl, `CREATE FUNCTION meridian_json(t text) RETURNS jsonb LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN
  RETURN t::jsonb;
EXCEPTION WHEN others THEN
  RETURN NULL;
END $$`)
	// a deleted protocol's or user's id is never given out again (migration 10): the highest one
	// deleted is kept in id_floor
	s.ddl = append(s.ddl, `CREATE FUNCTION meridian_id_floor() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO id_floor (name, last) VALUES (TG_TABLE_NAME, OLD.id)
    ON CONFLICT (name) DO UPDATE SET last = GREATEST(id_floor.last, excluded.last);
  RETURN OLD;
END $$`,
		`CREATE TRIGGER nodes_id_floor AFTER DELETE ON nodes FOR EACH ROW EXECUTE FUNCTION meridian_id_floor()`,
		`CREATE TRIGGER subs_id_floor AFTER DELETE ON subs FOR EACH ROW EXECUTE FUNCTION meridian_id_floor()`)
	return s, nil
}

func readTable(mem *sql.DB, name, createSQL string) (Table, error) {
	t := Table{Name: name}
	rows, err := mem.Query(`SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info(?) ORDER BY cid`, name)
	if err != nil {
		return t, err
	}
	pks := 0
	for rows.Next() {
		var c Column
		var def sql.NullString
		if err := rows.Scan(&c.Name, &c.Type, &c.NotNull, &def, &c.PK); err != nil {
			rows.Close()
			return t, err
		}
		c.Type = strings.ToUpper(strings.TrimSpace(c.Type))
		switch c.Type {
		case "INTEGER", "REAL", "TEXT":
		default:
			rows.Close()
			return t, fmt.Errorf("column %s: type %q has no PostgreSQL equivalent here", c.Name, c.Type)
		}
		c.Default = def.String
		if c.PK > 0 {
			pks++
		}
		t.Columns = append(t.Columns, c)
	}
	rows.Close()
	for _, c := range t.Columns {
		if pks == 1 && c.PK == 1 && c.Name == "id" && c.Type == "INTEGER" {
			t.IDKey = true
		}
	}
	frows, err := mem.Query(`SELECT "from", "table", "to", on_delete FROM pragma_foreign_key_list(?) ORDER BY id, seq`, name)
	if err != nil {
		return t, err
	}
	for frows.Next() {
		var fk FK
		var to sql.NullString
		if err := frows.Scan(&fk.Column, &fk.Parent, &to, &fk.OnDelete); err != nil {
			frows.Close()
			return t, err
		}
		fk.ParentColumn = to.String
		if fk.ParentColumn == "" {
			fk.ParentColumn = "id"
		}
		t.FKs = append(t.FKs, fk)
	}
	frows.Close()
	// CHECK constraints, from the table's own SQL
	for _, loc := range reCheck.FindAllStringIndex(createSQL, -1) {
		depth, start := 0, loc[1]-1
		for i := start; i < len(createSQL); i++ {
			if createSQL[i] == '(' {
				depth++
			} else if createSQL[i] == ')' {
				if depth--; depth == 0 {
					t.checks = append(t.checks, "CHECK "+createSQL[start:i+1])
					break
				}
			}
		}
	}
	// indexes: UNIQUE constraints and created ones; a NOCASE key column becomes lower(column)
	irows, err := mem.Query(`SELECT name, "unique", origin, partial FROM pragma_index_list(?)`, name)
	if err != nil {
		return t, err
	}
	type idx struct {
		name    string
		unique  bool
		origin  string
		partial bool
	}
	var idxs []idx
	for irows.Next() {
		var x idx
		if err := irows.Scan(&x.name, &x.unique, &x.origin, &x.partial); err != nil {
			irows.Close()
			return t, err
		}
		idxs = append(idxs, x)
	}
	irows.Close()
	sort.Slice(idxs, func(i, j int) bool { return idxs[i].name < idxs[j].name })
	for _, x := range idxs {
		if x.origin == "pk" {
			continue
		}
		crows, err := mem.Query(`SELECT name, coll FROM pragma_index_xinfo(?) WHERE key = 1 ORDER BY seqno`, x.name)
		if err != nil {
			return t, err
		}
		var cols, names []string
		for crows.Next() {
			var c, coll sql.NullString
			if err := crows.Scan(&c, &coll); err != nil {
				crows.Close()
				return t, err
			}
			if !c.Valid {
				crows.Close()
				return t, fmt.Errorf("index %s is on an expression - write it for PostgreSQL", x.name)
			}
			names = append(names, c.String)
			if strings.EqualFold(coll.String, "NOCASE") {
				cols = append(cols, "lower("+c.String+")")
			} else {
				cols = append(cols, c.String)
			}
		}
		crows.Close()
		iname := x.name
		if x.origin == "u" { // sqlite_autoindex_<table>_<n>: named as PostgreSQL would
			iname = name + "_" + strings.Join(names, "_") + "_key"
		}
		st := "CREATE "
		if x.unique {
			st += "UNIQUE "
		}
		st += fmt.Sprintf("INDEX %s ON %s (%s)", iname, name, strings.Join(cols, ", "))
		if x.partial {
			var isql string
			if err := mem.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, x.name).Scan(&isql); err != nil {
				return t, err
			}
			i := strings.Index(strings.ToUpper(isql), " WHERE ")
			if i < 0 {
				return t, fmt.Errorf("index %s: no WHERE in %q", x.name, isql)
			}
			st += " WHERE " + strings.TrimSpace(isql[i+7:])
		}
		t.indexes = append(t.indexes, st)
	}
	return t, nil
}

// pgType is a column's PostgreSQL type.
func pgType(sqliteType string) string {
	switch sqliteType {
	case "INTEGER":
		return "BIGINT"
	case "REAL":
		return "DOUBLE PRECISION"
	}
	return "TEXT"
}

func (t Table) createSQL() string {
	var parts []string
	npk := 0
	for _, c := range t.Columns {
		col := c.Name + " " + pgType(c.Type)
		if t.IDKey && c.Name == "id" {
			col += " GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY"
		} else {
			if c.NotNull {
				col += " NOT NULL"
			}
			if c.Default != "" && !strings.EqualFold(c.Default, "NULL") {
				col += " DEFAULT " + c.Default
			}
		}
		parts = append(parts, col)
		if c.PK > 0 {
			npk++
		}
	}
	if !t.IDKey {
		// the key's columns in their order
		cols := make([]string, npk)
		for _, c := range t.Columns {
			if c.PK > 0 {
				cols[c.PK-1] = c.Name
			}
		}
		if len(cols) > 0 {
			parts = append(parts, "PRIMARY KEY ("+strings.Join(cols, ", ")+")")
		}
	}
	parts = append(parts, t.checks...)
	return "CREATE TABLE " + t.Name + " (\n  " + strings.Join(parts, ",\n  ") + "\n)"
}

// parentsFirst orders tables so every table comes after the ones it refers to.
func parentsFirst(tables []Table) ([]Table, error) {
	byName := map[string]Table{}
	for _, t := range tables {
		byName[t.Name] = t
	}
	done := map[string]bool{}
	var out []Table
	var visit func(name string, path []string) error
	visit = func(name string, path []string) error {
		if done[name] {
			return nil
		}
		for _, p := range path {
			if p == name {
				return fmt.Errorf("schema: the tables refer to each other in a circle: %v", append(path, name))
			}
		}
		t, ok := byName[name]
		if !ok {
			return fmt.Errorf("schema: a foreign key refers to a table that does not exist: %s", name)
		}
		for _, fk := range t.FKs {
			if fk.Parent != name {
				if err := visit(fk.Parent, append(path, name)); err != nil {
					return err
				}
			}
		}
		done[name] = true
		out = append(out, t)
		return nil
	}
	for _, t := range tables {
		if err := visit(t.Name, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}
