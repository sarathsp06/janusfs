package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sarathsp06/janusfs/internal/config"
)

func newInstallCmd() *cobra.Command {
	var root string
	var globalRules bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "One-time setup: choose a mount root and save it to ~/.janusfs/settings.json",
		Long: "Configure the mount root — the folder under which every mount appears as a\n" +
			"sanitized mirror of its source's full path. Saved to ~/.janusfs/settings.json\n" +
			"so `janusfs mount <src>` needs no --mount-root thereafter.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInstall(root, globalRules)
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "mount root to configure (skips the interactive prompt)")
	cmd.Flags().BoolVar(&globalRules, "global-rules", false, "also write secure-default rules to ~/.janusfs/config")
	return cmd
}

func runInstall(root string, globalRules bool) error {
	def := config.DefaultMountRoot()
	if root == "" {
		root = promptWithDefault("Mount root (mounts appear as sanitized mirrors under here)", def)
	}
	root = expandHome(root)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("install: creating mount root %q: %w", root, err)
	}
	if err := config.SaveSettings(root); err != nil {
		return fmt.Errorf("install: %w", err)
	}

	p, _ := config.SettingsPath()
	fmt.Printf("Mount root set to %s (saved to %s).\n", root, p)
	fmt.Printf("`janusfs mount <src>` now mounts a sanitized mirror at %s/<full-path-of-src>.\n", root)

	if globalRules {
		fmt.Println()
		return runInitGlobal(false)
	}
	fmt.Println("Tip: `janusfs init --global` writes machine-wide secure-default rules.")
	return nil
}

// promptWithDefault reads one line from stdin, returning def if empty. When
// stdin isn't a terminal, it takes def without blocking.
func promptWithDefault(label, def string) string {
	if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return def
	}
	fmt.Printf("%s [%s]: ", label, def)
	sc := bufio.NewScanner(os.Stdin)
	if sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			return line
		}
	}
	return def
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
