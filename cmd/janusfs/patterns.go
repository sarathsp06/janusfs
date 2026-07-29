package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/sarathsp06/janusfs/internal/patterns"
)

func newPatternsCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "patterns",
		Short: "List built-in .janusfs.yml mask pattern names and regexes",
		Long: "Print the reserved built-in pattern names accepted in .janusfs.yml mask rules, along\n" +
			"with what each one masks and the exact RE2 regex source JanusFS uses.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPatterns(jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	return cmd
}

func runPatterns(jsonOut bool) error {
	infos := patterns.Builtins()
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(infos); err != nil {
			return fmt.Errorf("patterns: encoding JSON: %w", err)
		}
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tMASKS\tREGEX"); err != nil {
		return fmt.Errorf("patterns: writing output: %w", err)
	}
	for _, info := range infos {
		regex := "—"
		if len(info.Regexes) > 0 {
			regex = strings.Join(info.Regexes, " ; ")
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", info.Name, info.Description, regex); err != nil {
			return fmt.Errorf("patterns: writing output: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("patterns: writing output: %w", err)
	}
	return nil
}
