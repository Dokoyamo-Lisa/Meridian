// Command meridian runs the Meridian panel.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"meridian/internal/db"
	"meridian/internal/panel"
	"meridian/web"

	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	case "reset-password":
		resetPassword(os.Args[2:])
	case "reset-site-access":
		resetSiteAccess(os.Args[2:])
	case "backup":
		backup(os.Args[2:])
	case "token":
		createToken(os.Args[2:])
	case "mcp":
		mcpBridge(os.Args[2:])
	case "openapi":
		os.Stdout.Write(panel.OpenAPIJSON())
	case "version", "-v", "--version":
		fmt.Println("meridian", panel.Version)
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `Meridian panel

usage:
  meridian serve [flags]                 run the panel
      --listen ADDR          plain HTTP address (default :8080; ignored with --domain)
      --domain NAME          serve HTTPS on :443 with an automatic Let's Encrypt certificate
                             (port 80 must be reachable for the certificate check)
      --email ADDR           contact for Let's Encrypt expiry notices (optional)
      --data DIR             data directory (default /var/lib/meridian)
      --agent-dir DIR        directory with meridian-agent-linux-{amd64,arm64} (default DATA/agent)
      --trusted-proxies CIDR,...  reverse proxies whose X-Forwarded-For is believed
  meridian reset-password [--data DIR] USERNAME
                                         print a new password for an account (turns off its 2FA)
  meridian reset-site-access [--data DIR]
                                         turn off the rule for who may open the site (after being
                                         locked out of the panel by country); restart the panel after
  meridian backup [--data DIR] FILE      write a consistent copy of the database to FILE
  meridian token [--data DIR] [--name NAME] [--scope full|read] [--days N]
                                         print a new API token for scripts and AI agents (default:
                                         full access for 1 day); revoke it in Settings › API & MCP
  meridian mcp --url PANEL_URL           MCP stdio bridge for local AI clients; the API token is
                                         read from MERIDIAN_TOKEN
  meridian openapi                       print the API's OpenAPI document
  meridian version

Commands that open the database (reset-password, reset-site-access, backup, token) run as the
panel's user: sudo -u meridian meridian ...

Every flag can also be set as an environment variable: MERIDIAN_LISTEN, MERIDIAN_DOMAIN,
MERIDIAN_EMAIL, MERIDIAN_DATA, MERIDIAN_AGENT_DIR, MERIDIAN_TRUSTED_PROXIES.`)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", env("MERIDIAN_LISTEN", ":8080"), "plain HTTP address")
	domain := fs.String("domain", env("MERIDIAN_DOMAIN", ""), "serve HTTPS for this domain with Let's Encrypt")
	email := fs.String("email", env("MERIDIAN_EMAIL", ""), "Let's Encrypt contact address")
	data := fs.String("data", env("MERIDIAN_DATA", "/var/lib/meridian"), "data directory")
	agentDir := fs.String("agent-dir", env("MERIDIAN_AGENT_DIR", ""), "directory with meridian-agent-linux-{amd64,arm64}")
	trusted := fs.String("trusted-proxies", env("MERIDIAN_TRUSTED_PROXIES", ""), "comma-separated CIDRs of reverse proxies")
	fs.Parse(args)

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err := os.MkdirAll(*data, 0o700); err != nil {
		fatal("data dir", err)
	}
	_ = os.Chmod(*data, 0o700) // holds the database (keys, tokens): owner only
	if *agentDir == "" {
		*agentDir = filepath.Join(*data, "agent")
	}
	var proxies []netip.Prefix
	for _, c := range strings.Split(*trusted, ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		pf, err := netip.ParsePrefix(c)
		if err != nil {
			fatal("trusted proxies", err)
		}
		proxies = append(proxies, pf)
	}
	*domain = strings.ToLower(strings.TrimSpace(*domain))

	d, err := db.Open(filepath.Join(*data, "meridian.db"))
	if err != nil {
		fatal("database", err)
	}
	_ = os.Chmod(filepath.Join(*data, "meridian.db"), 0o600)
	if err := bootstrapOwner(d, *data); err != nil {
		fatal("bootstrap", err)
	}
	p, err := panel.New(panel.Config{Listen: *listen, DataDir: *data, TrustedProxies: proxies, WebFS: web.FS(),
		AgentDir: *agentDir}, d)
	if err != nil {
		fatal("panel", err)
	}
	if *domain != "" {
		if err := p.DefaultPublicURL("https://" + *domain); err != nil {
			fatal("settings", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go p.Run(ctx)

	newServer := func(addr string, h http.Handler) *http.Server {
		return &http.Server{Addr: addr, Handler: h,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       60 * time.Second,
			WriteTimeout:      15 * time.Minute, // agents long-poll and download cores through the mirror
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    64 << 10,
			ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
		}
	}
	var servers []*http.Server
	errc := make(chan error, 2)
	if *domain == "" {
		srv := newServer(*listen, p.Handler())
		servers = append(servers, srv)
		slog.Info("meridian panel listening", "addr", *listen, "data", *data, "version", panel.Version)
		go func() { errc <- srv.ListenAndServe() }()
	} else {
		m := &autocert.Manager{
			Prompt: autocert.AcceptTOS,
			// the panel's domain, and the status page's own domain when one is set in the panel
			HostPolicy: func(_ context.Context, host string) error {
				host = strings.ToLower(host)
				if host == *domain || (host != "" && host == p.StatusDomain()) {
					return nil
				}
				return fmt.Errorf("acme/autocert: host %q is not this panel's domain", host)
			},
			Cache: autocert.DirCache(filepath.Join(*data, "acme")),
			Email: *email,
		}
		tlsSrv := newServer(":443", p.Handler())
		tlsSrv.TLSConfig = m.TLSConfig()
		tlsSrv.TLSConfig.MinVersion = tls.VersionTLS12
		httpSrv := newServer(":80", m.HTTPHandler(nil)) // certificate checks; everything else goes to HTTPS
		servers = append(servers, tlsSrv, httpSrv)
		slog.Info("meridian panel listening", "https", "https://"+*domain, "data", *data, "version", panel.Version)
		go func() { errc <- tlsSrv.ListenAndServeTLS("", "") }()
		go func() { errc <- httpSrv.ListenAndServe() }()
	}
	select {
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal("listen", err)
		}
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(sctx)
	}
}

// bootstrapOwner creates the supervisor account on first start, with its password in a note file.
func bootstrapOwner(d *db.DB, data string) error {
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	user := env("MERIDIAN_ADMIN_USER", "admin")
	pw := os.Getenv("MERIDIAN_ADMIN_PASSWORD")
	if pw != "" && (len(pw) < 10 || len(pw) > 72) {
		return errors.New("MERIDIAN_ADMIN_PASSWORD must be 10 to 72 characters")
	}
	if pw == "" {
		pw = randomPassword()
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	if err != nil {
		return err
	}
	if _, err := d.Exec1(`INSERT INTO accounts (username, display_name, role, password_hash, created_at)
		VALUES (?, 'Owner', 'owner', ?, ?)`, user, string(h), time.Now().Unix()); err != nil {
		return err
	}
	// the password goes only into a note readable by the panel's user, never to the output: under
	// systemd that is the journal, which keeps it long after the note is gone
	note := filepath.Join(data, "initial-admin.txt")
	if err := os.WriteFile(note, []byte(fmt.Sprintf("username: %s\npassword: %s\n", user, pw)), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", note, err)
	}
	fmt.Fprintf(os.Stderr, "\n  Supervisor account %q created - its password is in %s until the first sign-in\n\n", user, note)
	return nil
}

func resetPassword(args []string) {
	fs := flag.NewFlagSet("reset-password", flag.ExitOnError)
	data := fs.String("data", env("MERIDIAN_DATA", "/var/lib/meridian"), "data directory")
	fs.Parse(args)
	if fs.NArg() != 1 {
		usage()
		os.Exit(2)
	}
	d := openDB(*data)
	pw := randomPassword()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	if err != nil {
		fatal("hash", err)
	}
	var res sql.Result
	res, err = d.Exec1(`UPDATE accounts SET password_hash = ?, totp_secret = '', totp_last = 0 WHERE username = ?`, string(h), fs.Arg(0))
	if err != nil {
		fatal("update", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		fatal("reset", fmt.Errorf("no account named %q", fs.Arg(0)))
	}
	_, _ = d.Exec1(`DELETE FROM sessions WHERE account_id = (SELECT id FROM accounts WHERE username = ?)`, fs.Arg(0))
	fmt.Printf("New password for %s: %s\n(two-factor sign-in was turned off and all sessions ended for this account)\n", fs.Arg(0), pw)
}

// openDB opens the panel's database for a host command. As root it refuses a database that belongs
// to the panel's own user: files SQLite created now would be root's, and the panel could no longer
// open them.
func openDB(data string) *db.DB {
	path := filepath.Join(data, "meridian.db")
	st, err := os.Stat(path)
	if err != nil {
		fatal("database", fmt.Errorf("%s not found - is --data right? (the default is /var/lib/meridian)", path))
	}
	if uid, ok := fileOwner(st); ok && os.Geteuid() == 0 && uid != 0 {
		fatal("database", errors.New("run this as the panel's user, not root: sudo -u meridian "+strings.Join(os.Args, " ")))
	}
	d, err := db.Open(path)
	if err != nil {
		fatal("database", err)
	}
	return d
}

// createToken prints a new API token for the supervisor - for scripts and AI agents deploying or
// configuring the panel. Only the token goes to stdout, so TOKEN=$(...) captures it.
func createToken(args []string) {
	fs := flag.NewFlagSet("token", flag.ExitOnError)
	data := fs.String("data", env("MERIDIAN_DATA", "/var/lib/meridian"), "data directory")
	name := fs.String("name", "deploy", "where the token is used")
	scope := fs.String("scope", "full", "full or read")
	days := fs.Int("days", 1, "lifetime in days (0 = never expires)")
	fs.Parse(args)
	if fs.NArg() != 0 {
		usage()
		os.Exit(2)
	}
	tok, err := panel.CreateTokenOnHost(openDB(*data), *name, *scope, *days)
	if err != nil {
		fatal("token", err)
	}
	life := "never expires"
	if *days > 0 {
		life = fmt.Sprintf("expires in %d day(s)", *days)
	}
	fmt.Fprintf(os.Stderr, "API token %q, %s access, %s - shown once; revoke it in Settings › API & MCP.\n", *name, *scope, life)
	fmt.Println(tok)
}

// resetSiteAccess turns the site's country rule off - the way back in for a supervisor whose
// country the rule no longer admits. The servers' country rule is left as it is.
func resetSiteAccess(args []string) {
	fs := flag.NewFlagSet("reset-site-access", flag.ExitOnError)
	data := fs.String("data", env("MERIDIAN_DATA", "/var/lib/meridian"), "data directory")
	fs.Parse(args)
	if fs.NArg() != 0 {
		usage()
		os.Exit(2)
	}
	d := openDB(*data)
	var raw string
	if err := d.QueryRow(`SELECT value FROM settings WHERE key = 'access'`).Scan(&raw); err != nil {
		fmt.Println("There is no rule for who may open the site - nothing to reset.")
		return
	}
	var a map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		fatal("reset", err)
	}
	var site map[string]any
	_ = json.Unmarshal(a["site"], &site)
	if site == nil {
		site = map[string]any{}
	}
	site["mode"] = "off"
	a["site"], _ = json.Marshal(site)
	b, _ := json.Marshal(a)
	if _, err := d.Exec1(`UPDATE settings SET value = ? WHERE key = 'access'`, string(b)); err != nil {
		fatal("reset", err)
	}
	fmt.Println("The rule for who may open the site is off (the servers' country rule is unchanged).")
	fmt.Println("Restart the panel to apply it: sudo systemctl restart meridian - proxies keep running meanwhile.")
}

// backup writes a consistent snapshot of the database, safe while the panel runs.
func backup(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	data := fs.String("data", env("MERIDIAN_DATA", "/var/lib/meridian"), "data directory")
	fs.Parse(args)
	if fs.NArg() != 1 {
		usage()
		os.Exit(2)
	}
	out, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		fatal("backup", err)
	}
	if _, err := os.Stat(out); err == nil {
		fatal("backup", fmt.Errorf("%s already exists", out))
	}
	d := openDB(*data)
	restore := privateUmask() // the copy holds keys and tokens: owner only
	_, err = d.Exec(`VACUUM INTO ?`, out)
	restore()
	if err != nil {
		fatal("backup", err)
	}
	_ = os.Chmod(out, 0o600)
	fmt.Printf("Backup written to %s\nIt contains every key and credential - store it like a password.\nRestore: stop the panel, copy it to %s, start the panel.\n",
		out, filepath.Join(*data, "meridian.db"))
}

// mcpBridge connects a local MCP client (stdio) to the panel's HTTP MCP endpoint.
func mcpBridge(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	panelURL := fs.String("url", env("MERIDIAN_URL", ""), "panel URL, e.g. https://panel.example.com")
	allowHTTP := fs.Bool("allow-http", false, "allow a plain http:// panel URL outside localhost (the token would travel unencrypted)")
	fs.Parse(args)
	token := os.Getenv("MERIDIAN_TOKEN")
	if *panelURL == "" || token == "" {
		fmt.Fprintln(os.Stderr, "meridian mcp: set --url (or MERIDIAN_URL) and the MERIDIAN_TOKEN environment variable")
		os.Exit(2)
	}
	u, err := url.Parse(strings.TrimRight(*panelURL, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		fatal("mcp", errors.New("--url must look like https://panel.example.com"))
	}
	if u.Scheme == "http" && !*allowHTTP {
		host := u.Hostname()
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			fatal("mcp", errors.New("refusing to send the token over plain http:// - use https:// or pass --allow-http"))
		}
	}
	endpoint := u.String() + "/mcp"
	client := &http.Client{Timeout: 120 * time.Second}
	var outMu sync.Mutex
	var version string
	write := func(b []byte) {
		outMu.Lock()
		defer outMu.Unlock()
		os.Stdout.Write(bytes.TrimSpace(b))
		os.Stdout.Write([]byte("\n"))
	}
	fail := func(id json.RawMessage, msg string) {
		if len(id) == 0 {
			return
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32000, "message": msg}})
		write(b)
	}
	in := bufio.NewReaderSize(os.Stdin, 1<<20)
	var wg sync.WaitGroup
	for {
		line, err := in.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			msg := append([]byte(nil), bytes.TrimSpace(line)...)
			var head struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(msg, &head)
			wg.Add(1)
			go func() {
				defer wg.Done()
				req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(msg))
				if err != nil {
					fail(head.ID, err.Error())
					return
				}
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				if version != "" {
					req.Header.Set("MCP-Protocol-Version", version)
				}
				resp, err := client.Do(req)
				if err != nil {
					fail(head.ID, "cannot reach the panel: "+err.Error())
					return
				}
				defer resp.Body.Close()
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
				switch {
				case resp.StatusCode == http.StatusAccepted:
				case resp.StatusCode == http.StatusOK:
					if head.Method == "initialize" {
						var r struct {
							Result struct {
								ProtocolVersion string `json:"protocolVersion"`
							} `json:"result"`
						}
						if json.Unmarshal(body, &r) == nil {
							version = r.Result.ProtocolVersion
						}
					}
					write(body)
				default:
					var e struct {
						Error string `json:"error"`
					}
					_ = json.Unmarshal(body, &e)
					if e.Error == "" {
						e.Error = resp.Status
					}
					fail(head.ID, "panel: "+e.Error)
				}
			}()
			if head.Method == "initialize" {
				wg.Wait() // the protocol version must be known before anything else is sent
			}
		}
		if err != nil {
			break
		}
	}
	wg.Wait()
}

func randomPassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, 0, 18)
	b := make([]byte, 64)
	for len(out) < 18 {
		if _, err := randRead(b); err != nil {
			panic(err)
		}
		for _, c := range b {
			// rejection sampling keeps every character equally likely
			if int(c) < 256-256%len(alphabet) && len(out) < 18 {
				out = append(out, alphabet[int(c)%len(alphabet)])
			}
		}
	}
	return string(out)
}

func fatal(what string, err error) {
	fmt.Fprintf(os.Stderr, "meridian: %s: %v\n", what, err)
	os.Exit(1)
}
