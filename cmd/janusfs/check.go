package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/sarathsp06/janusfs/internal/check"
	"github.com/spf13/cobra"
)

// errSilentNonZero signals "exit 1, but the report already printed
// everything the user needs to see" (FR-28/FR-33: findings are the output,
// not a Go error string). main's top-level error printer special-cases an
// empty message to avoid an extra blank "janusfs: " line.
var errSilentNonZero = errors.New("")

// newCheckCmd implements FR-28: statically lint the config tree rooted at
// [path] (default cwd) — regex/glob errors, zero-match globs, FR-9
// directory-mask rewrites, FR-8 hidden-dir negation attempts, and
// exact-duplicate rules — plus the global rule directory
// (docs/SPEC_AMENDMENTS.md 2026-07-17), always included in the scan.
func newCheckCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "check [path]",
		Short: "Statically analyze .janusignore/.janusmask for conflicts",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return runCheck(dir, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	return cmd
}

func runCheck(dir string, jsonOut bool) error {
	report, err := check.Run(dir)
	if err != nil {
		return fmt.Errorf("check: %w", err)
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return fmt.Errorf("check: encoding JSON: %w", err)
		}
		if report.HasErrors() {
			return errSilentNonZero
		}
		return nil
	}

	printCheckReport(report)
	if report.HasErrors() {
		return errSilentNonZero
	}
	return nil
}

// printCheckReport implements FR-33: findings grouped by file (Run already
// sorts by severity then file then line), each with file:line and a
// suggested fix where one exists.
func printCheckReport(report check.Report) {
	if len(report.Findings) == 0 {
		fmt.Printf("No findings across %d files, %d directories.\n", report.FileCount, report.DirCount)
		return
	}

	lastFile := ""
	for _, f := range report.Findings {
		if f.File != lastFile {
			fmt.Printf("\n%s\n", f.File)
			lastFile = f.File
		}
		loc := ""
		if f.Line > 0 {
			loc = fmt.Sprintf(":%d", f.Line)
		}
		fmt.Printf("  [%s]%s %s\n", f.Severity, loc, f.Message)
		if f.Suggestion != "" {
			fmt.Printf("    suggestion: %s\n", f.Suggestion)
		}
	}

	errs, warns, infos := 0, 0, 0
	for _, f := range report.Findings {
		switch f.Severity {
		case check.SeverityError:
			errs++
		case check.SeverityWarn:
			warns++
		default:
			infos++
		}
	}
	fmt.Printf("\n%d error(s), %d warning(s), %d info across %d files, %d directories.\n",
		errs, warns, infos, report.FileCount, report.DirCount)
}
