package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/sarathsp06/janusfs/internal/config"
	"github.com/sarathsp06/janusfs/internal/rules"
)

func newPathsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "paths",
		Short: "List the config and data paths JanusFS uses, and whether each exists",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPaths()
		},
	}
}

func runPaths() error {
	settings, _ := config.SettingsPath()
	mounts, _ := config.MountsPath()
	globalCfg, _ := rules.GlobalDir()

	// Show the configured mount root (settings file wins), falling back to
	// the default when nothing is configured yet.
	cfg := config.Default()
	_ = config.ApplyFile(&cfg)
	mountRoot := cfg.MountRoot
	mountRootNote := ""
	if mountRoot == "" {
		mountRoot = config.DefaultMountRoot()
		mountRootNote = "default, not configured"
	}

	rows := []struct{ label, path, note string }{
		{"settings", settings, ""},
		{"mounts registry", mounts, ""},
		{"global rules", globalCfg, ""},
		{"mount root", mountRoot, mountRootNote},
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, r := range rows {
		if r.path == "" {
			continue
		}
		note := existsMark(r.path)
		if r.note != "" {
			note = r.note
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.label, r.path, note)
	}
	return tw.Flush()
}

// existsMark reports "present" or "absent" for a path, for the paths listing.
func existsMark(path string) string {
	if _, err := os.Stat(path); err == nil {
		return "present"
	}
	return "absent"
}
