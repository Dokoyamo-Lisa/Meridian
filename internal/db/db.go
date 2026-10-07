// Package db opens the panel's SQLite database and runs its migrations.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"sync"

	_ "modernc.org/sqlite"
)

// DB wraps *sql.DB. SQLite has one writer at a time; Write serialises writers in-process so
// they queue instead of failing with SQLITE_BUSY.
type DB struct {
	*sql.DB
	wmu sync.Mutex
}

func Open(path string) (*DB, error) {
	q := url.Values{}
	for _, p := range []string{"busy_timeout(10000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	sqldb, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	sqldb.SetMaxOpenConns(8)
	sqldb.SetMaxIdleConns(8)
	d := &DB{DB: sqldb}
	if err := d.migrate(); err != nil {
		sqldb.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) migrate() error {
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var v int
	if err := d.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		err := d.Write(context.Background(), func(tx *sql.Tx) error {
			if _, err := tx.Exec(migrations[i]); err != nil {
				return err
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

// Write runs fn in a write transaction. fn must not call Write itself.
func (d *DB) Write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	d.wmu.Lock()
	defer d.wmu.Unlock()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Exec1 runs a single write statement through the writer queue.
func (d *DB) Exec1(query string, args ...any) (sql.Result, error) {
	var res sql.Result
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		res, err = tx.Exec(query, args...)
		return err
	})
	return res, err
}
