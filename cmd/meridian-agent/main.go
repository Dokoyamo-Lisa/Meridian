// Command meridian-agent runs on each server and keeps it in line with the Meridian panel.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"meridian/internal/agent"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
		cfg, err := agent.LoadConfig()
		if err != nil {
			fatal(fmt.Errorf("not installed (%v) - run the install command from the panel", err))
		}
		a, err := agent.New(cfg)
		if err != nil {
			fatal(err)
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		slog.Info("meridian-agent starting", "version", agent.Version, "panel", cfg.Panel)
		if err := a.Run(ctx); err != nil {
			fatal(err)
		}
	case "install":
		fs := flag.NewFlagSet("install", flag.ExitOnError)
		panel := fs.String("panel", "", "panel URL")
		token := fs.String("token", "", "server token from the panel (prefer the MERIDIAN_TOKEN environment variable)")
		apiPort := fs.Int("api-port", 0, "where the agent's two loopback-only ports start (default 50000; kept on reinstall)")
		fs.Parse(os.Args[2:])
		if *token == "" {
			*token = os.Getenv("MERIDIAN_TOKEN")
		}
		if err := agent.Install(*panel, *token, *apiPort); err != nil {
			fatal(err)
		}
	case "uninstall":
		if os.Geteuid() != 0 {
			fatal(fmt.Errorf("run as root"))
		}
		agent.Uninstall(true)
	case "version", "-v", "--version":
		fmt.Println("meridian-agent", agent.Version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  MERIDIAN_TOKEN=TOKEN meridian-agent install --panel URL
  meridian-agent run
  meridian-agent uninstall
  meridian-agent version`)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "meridian-agent:", err)
	os.Exit(1)
}
