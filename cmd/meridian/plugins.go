package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"meridian/internal/panel"
)

// pluginsCmd manages plugins from the panel's host - the way back when a plugin keeps the panel from
// working: list them, turn one off or on, or remove it. A running panel follows within seconds;
// `meridian serve --no-plugins` starts it without any.
func pluginsCmd(args []string) {
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	sub := args[0]
	fs := flag.NewFlagSet("plugins "+sub, flag.ExitOnError)
	data := fs.String("data", env("MERIDIAN_DATA", "/var/lib/meridian"), "data directory")
	yes := fs.Bool("yes", false, "agree to everything the plugin asks for (enable)")
	pos := parseInterspersed(fs, args[1:])
	switch sub {
	case "list":
		if len(pos) != 0 {
			usage()
			os.Exit(2)
		}
		list, err := panel.PluginsOnHost(openDB(*data), *data)
		if err != nil {
			fatal("plugins", err)
		}
		if len(list) == 0 {
			fmt.Println("No plugins are installed.")
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tVERSION\tON\tNAME\tLAST PROBLEM")
		for _, p := range list {
			on := "off"
			if p.Enabled {
				on = "on"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.ID, p.Version, on, p.Name, p.LastError)
		}
		tw.Flush()
	case "enable", "disable", "remove":
		if len(pos) != 1 {
			usage()
			os.Exit(2)
		}
		id := pos[0]
		d := openDB(*data)
		switch sub {
		case "enable":
			list, err := panel.PluginsOnHost(d, *data)
			if err != nil {
				fatal("plugins", err)
			}
			var p *panel.PluginOnHost
			for i := range list {
				if list[i].ID == id {
					p = &list[i]
				}
			}
			if p == nil {
				fatal("plugins", fmt.Errorf("there is no plugin %q - meridian plugins list shows them", id))
			}
			if !*yes {
				fmt.Fprintf(os.Stderr, "Turning on %s (%s) lets it do this:\n", p.Name, p.ID)
				for _, w := range p.Warnings {
					fmt.Fprintf(os.Stderr, "  - %s\n", w)
				}
				fmt.Fprintf(os.Stderr, "It asks for: %s\nTurn it on only if you trust where it comes from. To agree, run it again with --yes.\n", strings.Join(p.Asks, ", "))
				os.Exit(1)
			}
			if err := panel.SetPluginOnHost(d, *data, id, true); err != nil {
				fatal("plugins", err)
			}
			fmt.Printf("The plugin %s is on.\n", id)
		case "disable":
			if err := panel.SetPluginOnHost(d, *data, id, false); err != nil {
				fatal("plugins", err)
			}
			fmt.Printf("The plugin %s is off.\n", id)
		case "remove":
			if err := panel.RemovePluginOnHost(d, *data, id); err != nil {
				fatal("plugins", err)
			}
			fmt.Printf("The plugin %s is removed, with its files and its own data.\n", id)
		}
		fmt.Println("A running panel follows within a few seconds - no restart needed.")
	default:
		usage()
		os.Exit(2)
	}
}

// parseInterspersed parses flags wherever they are among the arguments (meridian plugins disable ID
// --data DIR) and returns the other arguments.
func parseInterspersed(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			os.Exit(2)
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// envOn says whether an environment variable is set to something that means yes.
func envOn(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}
