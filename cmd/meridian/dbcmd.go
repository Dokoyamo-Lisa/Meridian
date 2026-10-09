package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"meridian/internal/db"
)

// meridian db status|to-postgres|to-sqlite: which database a panel uses, and moving its data from
// SQLite to PostgreSQL and back. The panel must be stopped while the data moves; what it moved
// from is kept next to the data until it is deleted by hand.
func dbCmd(args []string) {
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	fs := flag.NewFlagSet("db "+args[0], flag.ExitOnError)
	data := fs.String("data", env("MERIDIAN_DATA", "/var/lib/meridian"), "data directory")
	rest := parseInterspersed(fs, args[1:])
	switch args[0] {
	case "status":
		dbStatus(*data)
	case "to-postgres":
		if len(rest) != 1 {
			fatal("db to-postgres", errors.New("give the PostgreSQL database's address: meridian db to-postgres postgres://meridian@/meridian?host=/var/run/postgresql"))
		}
		dbToPostgres(*data, rest[0])
	case "to-sqlite":
		dbToSQLite(*data)
	default:
		usage()
		os.Exit(2)
	}
}

func dbStatus(data string) {
	u, err := db.DataURL(data)
	if err != nil {
		fatal("database", err)
	}
	d := openDB(data)
	defer d.Close()
	v, err := d.SchemaVersion(context.Background())
	if err != nil {
		fatal("database", err)
	}
	where := filepath.Join(data, db.FileName)
	if u != "" {
		where = db.Describe(u)
	}
	fmt.Printf("%s - %s, schema %d of %d\n", d.ServerVersion(), where, v, db.Version())
}

// progress prints a line per table moved.
func progress(table string, rows, orphans int) {
	if rows == 0 && orphans == 0 {
		return
	}
	line := fmt.Sprintf("  %-20s %8d rows", table, rows)
	if orphans > 0 {
		line += fmt.Sprintf(" (%d left behind: they referred to rows deleted long ago)", orphans)
	}
	fmt.Println(line)
}

func dbToPostgres(data, url string) {
	if err := db.CheckURL(url); err != nil {
		fatal("db to-postgres", err)
	}
	if u, _ := db.DataURL(data); u != "" {
		fatal("db to-postgres", fmt.Errorf("this panel uses %s already", db.Describe(u)))
	}
	release, err := lockData(data)
	if errors.Is(err, errLocked) {
		fatal("db to-postgres", errors.New("the panel is running - stop it first: sudo systemctl stop meridian"))
	} else if err != nil {
		fatal("db to-postgres", err)
	}
	defer release()
	src := openDB(data) // SQLite, brought to this version
	defer src.Close()
	dst, err := db.OpenPostgres(url)
	if err != nil {
		fatal("db to-postgres", err)
	}
	defer dst.Close()
	fmt.Printf("Moving the data from %s to %s:\n", filepath.Join(data, db.FileName), db.Describe(url))
	start := time.Now()
	if err := db.Copy(context.Background(), src, dst, progress); err != nil {
		fatal("db to-postgres", fmt.Errorf("%w - nothing changed: the panel still uses SQLite (empty the PostgreSQL database before trying again)", err))
	}
	src.Close()
	// from now on the panel opens PostgreSQL; the SQLite file stays, renamed, until deleted by hand
	conf := filepath.Join(data, db.URLFile)
	if err := writePrivate(conf, []byte(url+"\n"), data); err != nil {
		fatal("db to-postgres", err)
	}
	kept := filepath.Join(data, db.FileName+".moved-to-postgres-"+time.Now().UTC().Format("20060102-150405"))
	for _, ext := range []string{"", "-wal", "-shm"} {
		if err := os.Rename(filepath.Join(data, db.FileName)+ext, kept+ext); err != nil && !errors.Is(err, os.ErrNotExist) {
			fatal("db to-postgres", err)
		}
	}
	fmt.Printf("Done in %s. The panel now uses PostgreSQL (%s).\n", time.Since(start).Round(time.Second), conf)
	fmt.Printf("The SQLite database is kept as %s - delete it once all is well (it holds keys too).\n", kept)
	fmt.Println("Start the panel: sudo systemctl start meridian")
}

func dbToSQLite(data string) {
	u, err := db.DataURL(data)
	if err != nil {
		fatal("db to-sqlite", err)
	}
	if u == "" {
		fatal("db to-sqlite", errors.New("this panel uses SQLite already"))
	}
	if os.Getenv("MERIDIAN_DATABASE_URL") != "" {
		fatal("db to-sqlite", errors.New("MERIDIAN_DATABASE_URL names the database: remove it from the panel's environment first"))
	}
	release, err := lockData(data)
	if errors.Is(err, errLocked) {
		fatal("db to-sqlite", errors.New("the panel is running - stop it first: sudo systemctl stop meridian"))
	} else if err != nil {
		fatal("db to-sqlite", err)
	}
	defer release()
	path := filepath.Join(data, db.FileName)
	if _, err := os.Stat(path); err == nil {
		fatal("db to-sqlite", fmt.Errorf("%s exists - move it away first", path))
	}
	src, err := db.OpenPostgres(u)
	if err != nil {
		fatal("db to-sqlite", err)
	}
	defer src.Close()
	fmt.Printf("Moving the data from %s to %s:\n", db.Describe(u), path)
	start := time.Now()
	restore := privateUmask()
	err = src.Snapshot(context.Background(), path)
	restore()
	if err != nil {
		for _, ext := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(path + ext)
		}
		fatal("db to-sqlite", fmt.Errorf("%w - nothing changed: the panel still uses PostgreSQL", err))
	}
	_ = chownLike(path, data)
	kept := filepath.Join(data, db.URLFile+".moved-to-sqlite-"+time.Now().UTC().Format("20060102-150405"))
	if err := os.Rename(filepath.Join(data, db.URLFile), kept); err != nil {
		fatal("db to-sqlite", err)
	}
	fmt.Printf("Done in %s. The panel now uses SQLite (%s).\n", time.Since(start).Round(time.Second), path)
	fmt.Printf("PostgreSQL's data is left as it was; its address is kept in %s.\n", kept)
	fmt.Println("Start the panel: sudo systemctl start meridian")
}

// writePrivate writes a file only its owner reads, owned like the data directory.
func writePrivate(path string, b []byte, data string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := chownLike(tmp, data); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// importRestored puts a restored backup's SQLite database into PostgreSQL (a panel that runs on
// PostgreSQL restores into it), keeping the file renamed.
func importRestored(d *db.DB, data string) error {
	path := filepath.Join(data, db.FileName)
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	// what PostgreSQL held is kept first, as a SQLite file (a restore can be undone with it)
	before := path + ".before-restore-" + time.Now().UTC().Format("20060102-150405")
	restore := privateUmask()
	err := d.Snapshot(context.Background(), before)
	restore()
	if err != nil {
		return fmt.Errorf("keeping the data it replaces: %w", err)
	}
	_ = chownLike(before, data)
	if err := d.ImportSQLite(context.Background(), path, nil); err != nil {
		return err
	}
	kept := path + ".imported-" + time.Now().UTC().Format("20060102-150405")
	for _, ext := range []string{"", "-wal", "-shm"} {
		if err := os.Rename(path+ext, kept+ext); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
