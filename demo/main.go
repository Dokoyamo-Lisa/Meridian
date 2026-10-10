// Command meridian-demo runs Meridian's public demo: a panel that anyone may look around in and
// nobody can change, with servers and people that do not exist.
//
// The panel is an ordinary release with a database of its own. setup fills it through the API with
// the demo's servers, protocols, users and plans and writes the month before; run then plays every
// server's agent - load, users online, traffic, pings - through the agents' own protocol. In front,
// the reverse proxy lets every visitor in with a read-only API token, turns away anything that
// would change something, and gives the panel no visitor's address (see README.md).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// setupTime is when the demo began: the people's start dates and the hosts' history count from it.
var setupTime int64

// state is what setup leaves for run: the demo agents' tokens and the hosts' boot times.
type state struct {
	SetupAt int64            `json:"setup_at"`
	Boot    map[int64]int64  `json:"boot"`
	Tokens  map[int64]string `json:"tokens"`
}

const usage = `meridian-demo - Rosélune's public demo: servers that do not exist, on a panel nobody can change

  meridian-demo setup -panel URL -data DIR -url PUBLIC_URL -state FILE
      fill a new, empty panel with the demo's servers, protocols, users, plans and monitors, and the
      month before today; MERIDIAN_TOKEN is a full API token (meridian token --data DIR)
  meridian-demo run -panel URL -state FILE [-data DIR]
      play every server's agent: load, users online, traffic and pings, until stopped, and keep
      the demo's dates current (with -data); MERIDIAN_TOKEN is a read-only API token
      (meridian token --data DIR --scope read)

See demo/README.md.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	token := os.Getenv("MERIDIAN_TOKEN")
	if token == "" {
		fatal("MERIDIAN_TOKEN is not set")
	}
	switch os.Args[1] {
	case "setup":
		fs := flag.NewFlagSet("setup", flag.ExitOnError)
		panel := fs.String("panel", "http://127.0.0.1:8090", "the demo panel, as the demo agents reach it")
		data := fs.String("data", "", "the panel's data directory (its SQLite database is written to directly)")
		public := fs.String("url", "", "the address visitors use, e.g. https://demo.example.com")
		file := fs.String("state", "", "where to keep the demo agents' tokens (readable only by you)")
		fs.Parse(os.Args[2:])
		if *data == "" || *public == "" || *file == "" {
			fatal("setup needs -data, -url and -state")
		}
		st, err := setup(ctx, newAPI(*panel, token), *panel, *data, *public)
		if err != nil {
			fatal(err.Error())
		}
		b, _ := json.MarshalIndent(st, "", "  ")
		if err := os.WriteFile(*file, append(b, '\n'), 0o600); err != nil {
			fatal(err.Error())
		}
		slog.Info("demo set up", "servers", len(st.Tokens), "state", *file)
	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		panel := fs.String("panel", "http://127.0.0.1:8090", "the demo panel")
		file := fs.String("state", "", "the file setup wrote")
		data := fs.String("data", "", "the panel's data directory: keeps the demo's dates current (optional)")
		fs.Parse(os.Args[2:])
		raw, err := os.ReadFile(*file)
		if err != nil {
			fatal(err.Error())
		}
		var st state
		if err := json.Unmarshal(raw, &st); err != nil {
			fatal("the state file: " + err.Error())
		}
		if err := run(ctx, newAPI(*panel, token), *panel, *data, &st); err != nil {
			fatal(err.Error())
		}
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "meridian-demo:", msg)
	os.Exit(1)
}
