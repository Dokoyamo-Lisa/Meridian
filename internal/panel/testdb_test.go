package panel

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"meridian/internal/db"
)

// testDB opens a test's database: SQLite in its folder, or - with MERIDIAN_TEST_PG set to a
// PostgreSQL URL whose role may create databases - a new PostgreSQL database, dropped afterwards.
func testDB(t *testing.T, dir string) *db.DB {
	t.Helper()
	base := os.Getenv("MERIDIAN_TEST_PG")
	if base == "" {
		d, err := db.Open(filepath.Join(dir, "meridian.db"))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "mt_" + hex.EncodeToString(b)
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE DATABASE ` + name); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	u.Path = "/" + name
	d, err := db.OpenPostgres(u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		d.Close()
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
		admin.Close()
	})
	return d
}
