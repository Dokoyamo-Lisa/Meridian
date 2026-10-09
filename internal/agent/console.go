package agent

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"meridian/internal/proto"
	"meridian/internal/seal"
	"meridian/internal/ws"
)

// The supervisor's console: a root shell on this server in the panel's browser page. The panel asks
// for one with an action (proto.ActionConsole); the agent starts the shell on a terminal of its own
// and dials the panel back with a WebSocket for that session - the same way as everything else it
// sends the panel: signed, TLS checked for the panel's name, through a relay when it uses one. Nothing
// typed or shown is kept or logged. Turned off on this server by the file /etc/meridian-agent/no-console
// or MERIDIAN_NO_CONSOLE=1 in the agent's environment.

const (
	consoleMax     = 4                // shells at a time
	consoleIdle    = 30 * time.Minute // without a key pressed
	consoleLongest = 8 * time.Hour
	consoleFrame   = 64 << 10
	consoleOff     = "/etc/meridian-agent/no-console"
)

var consoleIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// consoleAllowed says whether this server lets the panel open consoles.
func consoleAllowed() bool {
	if os.Getenv("MERIDIAN_NO_CONSOLE") == "1" {
		return false
	}
	_, err := os.Stat(consoleOff)
	return errors.Is(err, os.ErrNotExist)
}

type consoles struct {
	mu   sync.Mutex
	open map[string]bool
}

// startConsole opens the shell for a session and connects it to the panel; it returns at once.
func (a *Agent) startConsole(args map[string]any) (string, error) {
	if !consoleAllowed() {
		return "", errors.New("the console is turned off on this server (" + consoleOff + " or MERIDIAN_NO_CONSOLE=1)")
	}
	id, _ := args["session"].(string)
	if !consoleIDRE.MatchString(id) {
		return "", errors.New("not a console session")
	}
	cols, rows := intArg(args, "cols", 80, 20, 500), intArg(args, "rows", 24, 5, 300)
	a.cons.mu.Lock()
	if a.cons.open == nil {
		a.cons.open = map[string]bool{}
	}
	if len(a.cons.open) >= consoleMax {
		a.cons.mu.Unlock()
		return "", fmt.Errorf("%d consoles are open on this server already - close one", consoleMax)
	}
	if a.cons.open[id] {
		a.cons.mu.Unlock()
		return "already open", nil
	}
	a.cons.open[id] = true
	a.cons.mu.Unlock()
	sh, err := startShell(cols, rows)
	if err != nil {
		a.endConsole(id)
		return "", fmt.Errorf("starting a shell: %w", err)
	}
	go a.runConsole(id, sh)
	return "console started", nil
}

func (a *Agent) endConsole(id string) {
	a.cons.mu.Lock()
	delete(a.cons.open, id)
	a.cons.mu.Unlock()
}

func intArg(args map[string]any, k string, def, lo, hi int) int {
	f, ok := args[k].(float64)
	if !ok {
		return def
	}
	return min(max(int(f), lo), hi)
}

// runConsole joins the shell and the panel until either ends.
func (a *Agent) runConsole(id string, sh *shell) {
	defer a.endConsole(id)
	defer sh.close()
	ctx, cancel := context.WithTimeout(context.Background(), consoleLongest)
	defer cancel()
	conn, err := a.client.dialConsole(ctx, id)
	if err != nil {
		slog.Warn("console: cannot reach the panel", "err", err)
		return
	}
	defer conn.Close()
	conn.SetIdle(consoleIdle)
	go func() { // the shell's output to the panel
		buf := make([]byte, 32<<10)
		for {
			n, err := sh.pty.Read(buf)
			if n > 0 {
				if conn.Write(append([]byte{proto.ConsoleData}, buf[:n]...)) != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		code := sh.wait()
		b, _ := json.Marshal(map[string]any{"exit": code})
		_ = conn.Write(append([]byte{proto.ConsoleEnd}, b...))
		conn.Close()
	}()
	go func() { // the panel ends the session with the context
		<-ctx.Done()
		conn.Close()
	}()
	for { // what is typed in the browser, and its window size
		msg, err := conn.Read()
		if err != nil || len(msg) == 0 {
			return
		}
		switch msg[0] {
		case proto.ConsoleData:
			if _, err := sh.pty.Write(msg[1:]); err != nil {
				return
			}
		case proto.ConsoleSize:
			if len(msg) == 5 {
				sh.resize(int(binary.BigEndian.Uint16(msg[1:3])), int(binary.BigEndian.Uint16(msg[3:5])))
			}
		}
	}
}

// dialConsole opens the session's WebSocket to the panel, the way the agent reaches it.
func (c *Client) dialConsole(ctx context.Context, id string) (*ws.Conn, error) {
	var last error
	for _, route := range c.link.routes() {
		dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		conn, err := c.consoleOver(dctx, route, id)
		cancel()
		if err == nil {
			return conn, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("no way to the panel")
	}
	return nil, last
}

func (c *Client) consoleOver(ctx context.Context, route, id string) (*ws.Conn, error) {
	nc, err := c.link.dial(ctx, route)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = nc.SetDeadline(dl)
	}
	if c.link.secure {
		tc := tls.Client(nc, c.link.tlsFor())
		if err := tc.HandshakeContext(ctx); err != nil {
			nc.Close()
			return nil, err
		}
		nc = tc
	}
	path := proto.ConsolePath + id
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.panel+path, nil)
	if err != nil {
		nc.Close()
		return nil, err
	}
	ts, nonce := time.Now().Unix(), seal.Nonce()
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set(seal.HeaderServer, strconv.FormatInt(c.serverID, 10))
	req.Header.Set(seal.HeaderTime, strconv.FormatInt(ts, 10))
	req.Header.Set(seal.HeaderNonce, nonce)
	req.Header.Set(seal.HeaderSign, c.keys.Sign(http.MethodGet, path, ts, nonce, nil))
	conn, resp, err := ws.Client(nc, req, consoleFrame)
	if err != nil {
		nc.Close()
		if resp != nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return nil, fmt.Errorf("the panel answered %s: %s", resp.Status, b)
		}
		return nil, err
	}
	_ = nc.SetDeadline(time.Time{})
	return conn, nil
}
