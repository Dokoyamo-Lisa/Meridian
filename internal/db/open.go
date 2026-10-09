package db

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// A data directory names its PostgreSQL database in database.url (or MERIDIAN_DATABASE_URL); without
// one the panel uses SQLite, meridian.db in the directory - as every panel did before 1.0.
const (
	URLFile  = "database.url"
	FileName = "meridian.db"
)

// DataURL is the PostgreSQL database a data directory uses ("" = SQLite).
func DataURL(dir string) (string, error) {
	if u := strings.TrimSpace(os.Getenv("MERIDIAN_DATABASE_URL")); u != "" {
		return u, CheckURL(u)
	}
	b, err := os.ReadFile(filepath.Join(dir, URLFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	u := strings.TrimSpace(string(b))
	if u == "" {
		return "", nil
	}
	return u, CheckURL(u)
}

// CheckURL accepts a postgres:// (or postgresql://) URL.
func CheckURL(u string) error {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "postgres" && p.Scheme != "postgresql") {
		return errors.New("the database address must be a postgres:// URL, e.g. postgres://meridian@/meridian?host=/var/run/postgresql")
	}
	return nil
}

// Describe names a database for messages - never its password.
func Describe(u string) string {
	if u == "" {
		return "SQLite"
	}
	p, err := url.Parse(u)
	if err != nil {
		return "PostgreSQL"
	}
	host := p.Host
	if host == "" {
		host = p.Query().Get("host")
	}
	if host == "" {
		host = "the local socket"
	}
	return fmt.Sprintf("PostgreSQL %s on %s", strings.TrimPrefix(p.Path, "/"), host)
}

// OpenData opens a data directory's database: PostgreSQL when it names one, else SQLite.
func OpenData(dir string) (*DB, error) {
	u, err := DataURL(dir)
	if err != nil {
		return nil, err
	}
	if u != "" {
		return OpenPostgres(u)
	}
	return Open(filepath.Join(dir, FileName))
}

// ServerVersion is the database server's version, for the panel's settings page.
func (d *DB) ServerVersion() string {
	var v string
	if d.Dialect == Postgres {
		if d.QueryRow(`SHOW server_version`).Scan(&v) == nil {
			v, _, _ = strings.Cut(v, " ")
			return "PostgreSQL " + v
		}
		return "PostgreSQL"
	}
	if d.QueryRow(`SELECT sqlite_version()`).Scan(&v) == nil {
		return "SQLite " + v
	}
	return "SQLite"
}
