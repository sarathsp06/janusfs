package main

import (
	"fmt"
	"os"
	"path/filepath"
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

// newPathCmd prints the mountpoint for a mounted source, so it can be used in
// shells: `cd "$(janusfs path ~/projects)"`.
func newPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path <src>",
		Short: "Print the mountpoint for a mounted source (for cd/scripting)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			want, err := filepath.Abs(args[0])
			if err != nil {
				want = args[0]
			}
			resp, err := callDaemon("path", daemonRequest{Cmd: "list"})
			if err != nil {
				return err
			}
			for _, m := range resp.Mounts {
				if abs, _ := filepath.Abs(m.Src); abs == want || m.Mountpoint == want {
					fmt.Println(m.Mountpoint)
					return nil
				}
			}
			return fmt.Errorf("path: %q is not mounted", args[0])
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
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", r.label, r.path, note)
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
