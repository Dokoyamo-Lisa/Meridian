package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
)

// CheckFile makes sure a file is a sound Meridian database this version can run: an SQLite file
// whose integrity check passes, with Meridian's tables, from this version or an older one. It opens
// the file read-only and immutable: nothing is written next to it.
func CheckFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	head := make([]byte, 16)
	_, err = io.ReadFull(f, head)
	f.Close()
	if err != nil || string(head) != "SQLite format 3\x00" {
		return errors.New("this is not a Meridian backup (not an SQLite database)")
	}
	d, err := sql.Open("sqlite", "file:"+(&url.URL{Path: path}).EscapedPath()+"?mode=ro&immutable=1")
	if err != nil {
		return err
	}
	defer d.Close()
	var ok string
	if err := d.QueryRow(`PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
		if ok == "" {
			ok = fmt.Sprint(err)
		}
		return fmt.Errorf("the database is damaged (%s)", ok)
	}
	var version int
	if err := d.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version); err != nil {
		return errors.New("this is not a Meridian backup")
	}
	var servers int
	if err := d.QueryRow(`SELECT COUNT(*) FROM servers`).Scan(&servers); err != nil {
		return errors.New("this is not a Meridian backup")
	}
	if version > Version() {
		return errors.New("the backup comes from a newer Meridian - upgrade the panel first")
	}
	return nil
}
