package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"meridian/internal/backup"
	"meridian/internal/db"
)

// Backups in the panel: a ZIP to download (it holds every key: the supervisor's password is asked
// first), copies on a schedule to a WebDAV folder or an S3 bucket (always encrypted with the
// operator's passphrase), and restoring one - uploaded, or from the remote - which the panel stages
// and puts in place as it restarts; the servers keep running meanwhile.

// backupConf is the backup settings (settings key "backup"). Passwords, keys and the passphrase
// never leave the panel.
type backupConf struct {
	Schedule   string `json:"schedule"` // off | daily | weekly
	Hour       int    `json:"hour"`     // panel time
	Weekday    int    `json:"weekday"`  // 0 = Sunday
	Keep       int    `json:"keep"`     // remote backups kept
	Passphrase string `json:"passphrase"`
	Kind       string `json:"kind"` // "" | webdav | s3
	WebDAV     struct {
		URL      string `json:"url"`
		User     string `json:"user"`
		Password string `json:"password"`
	} `json:"webdav"`
	S3 struct {
		Endpoint  string `json:"endpoint"`
		Region    string `json:"region"`
		Bucket    string `json:"bucket"`
		Prefix    string `json:"prefix"`
		AccessKey string `json:"access_key"`
		SecretKey string `json:"secret_key"`
		PathStyle bool   `json:"path_style"`
	} `json:"s3"`
	LastRun   int64  `json:"last_run"`
	LastOK    int64  `json:"last_ok"`
	LastName  string `json:"last_name"`
	LastError string `json:"last_error"`
}

func (p *Panel) backupConf() backupConf {
	c := backupConf{Schedule: "off", Hour: 4, Keep: 14}
	var raw string
	if p.db.QueryRow(`SELECT value FROM settings WHERE key = 'backup'`).Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	return c
}

func (p *Panel) saveBackupConf(c backupConf) error {
	b, _ := json.Marshal(c)
	_, err := p.db.Exec1(`INSERT INTO settings (key, value) VALUES ('backup', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(b))
	return err
}

// remote is where the settings send backups, or nil.
func (c backupConf) remote() backup.Remote {
	switch c.Kind {
	case "webdav":
		return &backup.WebDAV{URL: c.WebDAV.URL, User: c.WebDAV.User, Password: c.WebDAV.Password}
	case "s3":
		return &backup.S3{Endpoint: c.S3.Endpoint, Region: c.S3.Region, Bucket: c.S3.Bucket, Prefix: c.S3.Prefix,
			AccessKey: c.S3.AccessKey, SecretKey: c.S3.SecretKey, PathStyle: c.S3.PathStyle}
	}
	return nil
}

// ---------------------------------------------------------------- making one

// backupName is a backup file's name: the panel's host and the time.
func (p *Panel) backupName(encrypted bool) string {
	host := urlHost(p.settings().PublicURL)
	var b strings.Builder
	for _, r := range strings.ToLower(host) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			b.WriteRune(r)
		}
	}
	name := "meridian-" + nz(strings.Trim(b.String(), ".-"), "panel") + "-" + time.Now().UTC().Format("20060102-150405") + ".zip"
	if encrypted {
		name += ".age"
	}
	return name
}

// writeBackup writes a backup to w, encrypted when passphrase is set.
func (p *Panel) writeBackup(ctx context.Context, w io.Writer, passphrase string) error {
	info := backup.Info{Version: Version, Schema: db.Version(), Panel: p.settings().PublicURL}
	snap := func(path string) error { return p.db.Snapshot(ctx, path) } // a SQLite file, whichever database runs
	if passphrase == "" {
		return backup.Write(w, p.cfg.DataDir, info, snap)
	}
	enc, err := backup.Encrypt(w, passphrase)
	if err != nil {
		return err
	}
	if err := backup.Write(enc, p.cfg.DataDir, info, snap); err != nil {
		return err
	}
	return enc.Close()
}

// backupNow makes an encrypted backup and sends it to the remote, then removes the oldest beyond
// what the settings keep. It returns the backup's name.
func (p *Panel) backupNow(ctx context.Context, by int64) (string, error) {
	p.backupMu.Lock()
	defer p.backupMu.Unlock()
	c := p.backupConf()
	r := c.remote()
	if r == nil {
		return "", errStatus(http.StatusBadRequest, "set where backups go first (WebDAV or S3)")
	}
	if c.Passphrase == "" {
		return "", errStatus(http.StatusBadRequest, "set a passphrase first: backups that leave the panel are encrypted with it")
	}
	tmp, err := os.CreateTemp(p.cfg.DataDir, ".backup-*.age")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	name := p.backupName(true)
	err = p.writeBackup(ctx, tmp, c.Passphrase)
	var size int64
	if err == nil {
		size, err = tmp.Seek(0, io.SeekCurrent)
	}
	if err == nil {
		_, err = tmp.Seek(0, io.SeekStart)
	}
	if err == nil {
		err = r.Put(ctx, name, tmp, size)
	}
	c = p.backupConf() // what may have changed meanwhile
	c.LastRun = now()
	if err != nil {
		c.LastError = err.Error()
		_ = p.saveBackupConf(c)
		p.event(0, "warn", "backup_failed", 0, 0, by, "Backup failed: "+err.Error(), nil)
		return "", err
	}
	c.LastOK, c.LastName, c.LastError = now(), name, ""
	_ = p.saveBackupConf(c)
	removed := 0
	if list, err := r.List(ctx); err == nil && c.Keep > 0 && len(list) > c.Keep {
		for _, o := range list[c.Keep:] {
			if strings.HasSuffix(o.Name, ".zip.age") && r.Delete(ctx, o.Name) == nil {
				removed++
			}
		}
	}
	msg := fmt.Sprintf("Backup %s sent (%s)", name, fmtBytes(size))
	if removed > 0 {
		msg += fmt.Sprintf("; %d older one(s) removed", removed)
	}
	p.event(0, "info", "backup_done", 0, 0, by, msg, nil)
	return name, nil
}

// backupLoop makes the scheduled backups: once a day or a week, at the hour set, panel time.
func (p *Panel) backupLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c := p.backupConf()
		if c.Schedule != "daily" && c.Schedule != "weekly" || c.remote() == nil || c.Passphrase == "" {
			continue
		}
		at := p.localNow()
		if at.Hour() != c.Hour || (c.Schedule == "weekly" && int(at.Weekday()) != c.Weekday) {
			continue
		}
		if c.LastRun > 0 && at.Unix()-c.LastRun < 20*3600 { // made already this time
			continue
		}
		if _, err := p.backupNow(ctx, 0); err != nil {
			slog.Warn("scheduled backup", "err", err)
		}
	}
}

// ---------------------------------------------------------------- API

type backupView struct {
	Schedule      string       `json:"schedule" doc:"off | daily | weekly"`
	Hour          int          `json:"hour" doc:"Hour of the day, panel time"`
	Weekday       int          `json:"weekday" doc:"For weekly: 0 = Sunday ... 6 = Saturday"`
	Keep          int          `json:"keep" doc:"How many backups are kept at the destination; older ones are removed"`
	PassphraseSet bool         `json:"passphrase_set" doc:"A passphrase is saved (never shown)"`
	Kind          string       `json:"kind" doc:"Where backups go: '' (nowhere), webdav or s3"`
	WebDAVURL     string       `json:"webdav_url"`
	WebDAVUser    string       `json:"webdav_user"`
	S3Endpoint    string       `json:"s3_endpoint"`
	S3Region      string       `json:"s3_region"`
	S3Bucket      string       `json:"s3_bucket"`
	S3Prefix      string       `json:"s3_prefix"`
	S3AccessKey   string       `json:"s3_access_key"`
	S3PathStyle   bool         `json:"s3_path_style"`
	SecretSet     bool         `json:"secret_set" doc:"The WebDAV password or S3 secret key is saved (never shown)"`
	LastRun       int64        `json:"last_run"`
	LastOK        int64        `json:"last_ok"`
	LastName      string       `json:"last_name"`
	LastError     string       `json:"last_error,omitempty"`
	Pending       *backup.Info `json:"pending,omitempty" doc:"A restore is staged: the panel puts it in place when it next starts"`
}

type backupInput struct {
	Schedule    *string `json:"schedule"`
	Hour        *int    `json:"hour"`
	Weekday     *int    `json:"weekday"`
	Keep        *int    `json:"keep"`
	Passphrase  *string `json:"passphrase" doc:"At least 12 characters; keep it somewhere safe - without it the backups cannot be opened"`
	Kind        *string `json:"kind" doc:"'' | webdav | s3"`
	WebDAVURL   *string `json:"webdav_url" doc:"The folder, https://..."`
	WebDAVUser  *string `json:"webdav_user"`
	WebDAVPass  *string `json:"webdav_password"`
	S3Endpoint  *string `json:"s3_endpoint" doc:"https://... e.g. https://s3.eu-central-1.amazonaws.com or https://<account>.r2.cloudflarestorage.com"`
	S3Region    *string `json:"s3_region" doc:"e.g. eu-central-1; auto for Cloudflare R2"`
	S3Bucket    *string `json:"s3_bucket"`
	S3Prefix    *string `json:"s3_prefix" doc:"A folder in the bucket, e.g. meridian/"`
	S3AccessKey *string `json:"s3_access_key"`
	S3SecretKey *string `json:"s3_secret_key"`
	S3PathStyle *bool   `json:"s3_path_style" doc:"https://endpoint/bucket/... instead of https://bucket.endpoint/... (MinIO and most services other than AWS)"`
}

type backupList struct {
	Backups []backup.Object `json:"backups" doc:"The panel's backups at the destination, newest first"`
}

type downloadInput struct {
	Password   string `json:"password" doc:"Your password: a backup holds every key and credential"`
	Passphrase string `json:"passphrase" doc:"Encrypt the download with this passphrase (age); empty = a plain ZIP"`
}

type restoreInput struct {
	Password   string `json:"password" doc:"Your password"`
	Name       string `json:"name" doc:"A backup at the destination (from GET /api/backups/remote)"`
	Passphrase string `json:"passphrase" doc:"The backup's passphrase; empty = the one saved in the settings"`
}

type restoreResult struct {
	Restarting bool        `json:"restarting" doc:"The panel restarts now and comes back with the backup; servers keep running"`
	Backup     backup.Info `json:"backup"`
	Message    string      `json:"message"`
}

func (p *Panel) backupViewOf() backupView {
	c := p.backupConf()
	v := backupView{Schedule: c.Schedule, Hour: c.Hour, Weekday: c.Weekday, Keep: c.Keep, PassphraseSet: c.Passphrase != "", Kind: c.Kind,
		WebDAVURL: c.WebDAV.URL, WebDAVUser: c.WebDAV.User, S3Endpoint: c.S3.Endpoint, S3Region: c.S3.Region, S3Bucket: c.S3.Bucket,
		S3Prefix: c.S3.Prefix, S3AccessKey: c.S3.AccessKey, S3PathStyle: c.S3.PathStyle, LastRun: c.LastRun, LastOK: c.LastOK,
		LastName: c.LastName, LastError: c.LastError}
	v.SecretSet = (c.Kind == "webdav" && c.WebDAV.Password != "") || (c.Kind == "s3" && c.S3.SecretKey != "")
	if info, ok := backup.Pending(p.cfg.DataDir); ok {
		v.Pending = &info
	}
	return v
}

func (p *Panel) apiBackups(w http.ResponseWriter, r *http.Request, a *Account) error {
	writeJSON(w, http.StatusOK, p.backupViewOf())
	return nil
}

func httpsField(v, what string) error {
	if v == "" {
		return nil
	}
	if !strings.HasPrefix(v, "https://") || len(v) > 500 {
		return errStatus(http.StatusBadRequest, what+" must be an https:// address (no plain HTTP)")
	}
	return nil
}

func (p *Panel) apiPutBackups(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in backupInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	c := p.backupConf()
	str := func(dst *string, v *string, max int) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
			if len(*dst) > max {
				*dst = (*dst)[:max]
			}
		}
	}
	if in.Schedule != nil {
		switch *in.Schedule {
		case "off", "daily", "weekly":
			c.Schedule = *in.Schedule
		default:
			return errStatus(http.StatusBadRequest, "schedule is off, daily or weekly")
		}
	}
	if in.Hour != nil {
		c.Hour = clamp(*in.Hour, 0, 23)
	}
	if in.Weekday != nil {
		c.Weekday = clamp(*in.Weekday, 0, 6)
	}
	if in.Keep != nil {
		c.Keep = clamp(*in.Keep, 1, 365)
	}
	if in.Passphrase != nil && *in.Passphrase != "" {
		if n := len([]rune(*in.Passphrase)); n < 12 || len(*in.Passphrase) > 200 {
			return errStatus(http.StatusBadRequest, "the passphrase needs at least 12 characters - several words work well")
		}
		c.Passphrase = *in.Passphrase
	}
	if in.Kind != nil {
		switch *in.Kind {
		case "", "webdav", "s3":
			c.Kind = *in.Kind
		default:
			return errStatus(http.StatusBadRequest, "kind is webdav or s3 (or empty for none)")
		}
	}
	str(&c.WebDAV.URL, in.WebDAVURL, 500)
	str(&c.WebDAV.User, in.WebDAVUser, 200)
	if in.WebDAVPass != nil && *in.WebDAVPass != "" {
		c.WebDAV.Password = *in.WebDAVPass
	}
	str(&c.S3.Endpoint, in.S3Endpoint, 500)
	str(&c.S3.Region, in.S3Region, 64)
	str(&c.S3.Bucket, in.S3Bucket, 63)
	str(&c.S3.Prefix, in.S3Prefix, 200)
	str(&c.S3.AccessKey, in.S3AccessKey, 200)
	if in.S3SecretKey != nil && *in.S3SecretKey != "" {
		c.S3.SecretKey = *in.S3SecretKey
	}
	if in.S3PathStyle != nil {
		c.S3.PathStyle = *in.S3PathStyle
	}
	if err := httpsField(c.WebDAV.URL, "the WebDAV folder"); err != nil {
		return err
	}
	if err := httpsField(c.S3.Endpoint, "the S3 endpoint"); err != nil {
		return err
	}
	if c.S3.Prefix != "" && !strings.HasSuffix(c.S3.Prefix, "/") {
		c.S3.Prefix += "/"
	}
	if strings.Contains(c.S3.Prefix, "..") {
		return errStatus(http.StatusBadRequest, "the S3 folder cannot contain ..")
	}
	if c.Schedule != "off" && (c.remote() == nil || c.Passphrase == "") {
		return errStatus(http.StatusBadRequest, "scheduled backups need a destination and a passphrase")
	}
	if err := p.saveBackupConf(c); err != nil {
		return err
	}
	p.event(a.ID, "info", "settings", 0, 0, a.ID, a.Username+" changed the backup settings", nil)
	writeJSON(w, http.StatusOK, p.backupViewOf())
	return nil
}

// apiTestBackups writes a small file to the destination, lists it and removes it again.
func (p *Panel) apiTestBackups(w http.ResponseWriter, r *http.Request, a *Account) error {
	rem := p.backupConf().remote()
	if rem == nil {
		return errStatus(http.StatusBadRequest, "set where backups go first")
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	name := ".meridian-write-test-" + randToken(6)
	if err := rem.Put(ctx, name, strings.NewReader("Meridian checks that it can write here."), 39); err != nil {
		return errStatus(http.StatusBadGateway, err.Error())
	}
	if _, err := rem.List(ctx); err != nil {
		_ = rem.Delete(ctx, name)
		return errStatus(http.StatusBadGateway, err.Error())
	}
	if err := rem.Delete(ctx, name); err != nil {
		return errStatus(http.StatusBadGateway, "writing works but removing does not: "+err.Error())
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "The panel can write, list and remove files there."})
	return nil
}

func (p *Panel) apiRunBackup(w http.ResponseWriter, r *http.Request, a *Account) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour) // a slow upload outlives the request
	defer cancel()
	name, err := p.backupNow(ctx, a.ID)
	if err != nil {
		var se *apiError
		if errors.As(err, &se) {
			return err
		}
		return errStatus(http.StatusBadGateway, err.Error())
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name, "backup": p.backupViewOf()})
	return nil
}

func (p *Panel) apiRemoteBackups(w http.ResponseWriter, r *http.Request, a *Account) error {
	rem := p.backupConf().remote()
	if rem == nil {
		writeJSON(w, http.StatusOK, backupList{Backups: []backup.Object{}})
		return nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	list, err := rem.List(ctx)
	if err != nil {
		return errStatus(http.StatusBadGateway, err.Error())
	}
	if list == nil {
		list = []backup.Object{}
	}
	writeJSON(w, http.StatusOK, backupList{Backups: list})
	return nil
}

// confirmPassword checks the supervisor's password again for what exposes or replaces everything.
func (p *Panel) confirmPassword(a *Account, pw string) error {
	if !p.limiter.allow(fmt.Sprintf("confirm:%d", a.ID), 10, 15*time.Minute) {
		return errStatus(http.StatusTooManyRequests, "too many tries - wait a few minutes")
	}
	if !checkPassword(a.PasswordHash, pw) {
		return errStatus(http.StatusForbidden, "that is not your password")
	}
	return nil
}

// apiDownloadBackup sends a backup as a file: a ZIP, or encrypted when a passphrase is given.
func (p *Panel) apiDownloadBackup(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in downloadInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := p.confirmPassword(a, in.Password); err != nil {
		return err
	}
	if in.Passphrase != "" && len([]rune(in.Passphrase)) < 12 {
		return errStatus(http.StatusBadRequest, "the passphrase needs at least 12 characters")
	}
	name := p.backupName(in.Passphrase != "")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Cache-Control", "no-store")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Hour))
	if err := p.writeBackup(r.Context(), w, in.Passphrase); err != nil {
		slog.Warn("backup download", "err", err) // the headers went out already
		return nil
	}
	p.event(a.ID, "info", "backup_downloaded", 0, 0, a.ID, fmt.Sprintf("%s downloaded a backup (%s) from %s", a.Username, name, p.clientIP(r)), nil)
	return nil
}

// apiRestoreBackup stages a backup - uploaded (multipart: password, passphrase, file) or from the
// destination (JSON) - and restarts the panel into it.
func (p *Panel) apiRestoreBackup(w http.ResponseWriter, r *http.Request, a *Account) error {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(time.Hour)) // uploads take their time
	tmp, err := os.CreateTemp(p.cfg.DataDir, ".restore-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	var password, passphrase, from string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		mr, err := r.MultipartReader()
		if err != nil {
			return errStatus(http.StatusBadRequest, "send the backup as a file upload")
		}
		got := false
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return errStatus(http.StatusBadRequest, "the upload broke off - try again")
			}
			switch part.FormName() {
			case "password", "passphrase":
				b, _ := io.ReadAll(io.LimitReader(part, 256))
				if part.FormName() == "password" {
					password = string(b)
				} else {
					passphrase = string(b)
				}
			case "file":
				if err := p.confirmPassword(a, password); err != nil { // asked before the file is taken in
					return err
				}
				if _, err := io.Copy(tmp, io.LimitReader(part, 8<<30)); err != nil {
					return errStatus(http.StatusBadRequest, "the upload broke off - try again")
				}
				from, got = part.FileName(), true
			}
			part.Close()
		}
		if !got {
			return errStatus(http.StatusBadRequest, "choose a backup file")
		}
	} else {
		var in restoreInput
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if err := p.confirmPassword(a, in.Password); err != nil {
			return err
		}
		c := p.backupConf()
		rem := c.remote()
		if rem == nil {
			return errStatus(http.StatusBadRequest, "no destination is set to restore from")
		}
		passphrase = nz(in.Passphrase, c.Passphrase)
		ctx, cancel := context.WithTimeout(r.Context(), time.Hour)
		defer cancel()
		body, err := rem.Get(ctx, in.Name)
		if err != nil {
			return errStatus(http.StatusBadGateway, err.Error())
		}
		_, err = io.Copy(tmp, io.LimitReader(body, 8<<30))
		body.Close()
		if err != nil {
			return errStatus(http.StatusBadGateway, "downloading the backup failed: "+err.Error())
		}
		from = in.Name
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	info, err := backup.Stage(p.cfg.DataDir, tmp.Name(), passphrase, db.CheckFile)
	if err != nil {
		return errStatus(http.StatusBadRequest, err.Error())
	}
	made := time.Unix(info.CreatedAt, 0).In(p.loc()).Format("2 Jan 2006 15:04")
	p.event(a.ID, "warn", "backup_restore", 0, 0, a.ID, fmt.Sprintf("%s is restoring the backup %s (made %s by Meridian %s) - the panel restarts with it",
		a.Username, truncate(cleanName(from, 120), 120), made, nz(info.Version, "?")), nil)
	writeJSON(w, http.StatusAccepted, restoreResult{Restarting: true, Backup: info,
		Message: "The panel restarts now and comes back with the backup in a few seconds. The servers keep running; they get the restored configuration when they reconnect."})
	go p.restartSoon()
	return nil
}

func (p *Panel) apiDiscardRestore(w http.ResponseWriter, r *http.Request, a *Account) error {
	if err := backup.Discard(p.cfg.DataDir); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, p.backupViewOf())
	return nil
}

// restartSoon stops the panel gracefully a moment after an answer went out; its service starts it
// again (systemd: Restart=always), and a staged restore is put in place then.
func (p *Panel) restartSoon() {
	time.Sleep(time.Second)
	slog.Info("restarting to restore a backup")
	restartSelf()
}

// restartHook replaces how the panel ends itself (tests: a test binary must never stop itself).
var restartHook atomic.Value // func()

// restartSelf ends the panel gracefully; its service starts it again.
func restartSelf() {
	if f, ok := restartHook.Load().(func()); ok && f != nil {
		f()
		return
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
}

// NoteRestore records, in the restored database, that a backup was restored at startup.
func (p *Panel) NoteRestore(info backup.Info) {
	made := time.Unix(info.CreatedAt, 0).In(p.loc()).Format("2 Jan 2006 15:04")
	p.event(0, "warn", "backup_restored", 0, 0, 0, fmt.Sprintf("Restored the backup made %s by Meridian %s - what the panel had before is kept in %s",
		made, nz(info.Version, "?"), filepath.Join(p.cfg.DataDir, "before-restore-*")), nil)
}
