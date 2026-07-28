package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/sarathsp06/janusfs/internal/check"
)

// errSilentNonZero signals "exit 1, but the report already printed
// everything the user needs to see": the findings are the output, not a Go
// error string. main's top-level error printer special-cases an
// empty message to avoid an extra blank "janusfs: " line.
var errSilentNonZero = errors.New("")

// newCheckCmd statically lints the config tree rooted at [path] (default cwd)
// for the things that indicate a real mistake: regex and glob errors,
// directory-mask rewrites, negation attempts a hidden ancestor blocks, and
// negations blocked by the global rule floor. It deliberately does NOT report a
// pattern that merely matches no files today — a defensive rule covering files
// that don't exist yet is intended, not a bug. The global rule directory is
// always included in the scan, since it participates in every decision.
// --secrets adds an opt-in heuristic scan for likely secret files/content that
// still resolve Allowed; it is a warning aid, not a proof of complete coverage.
func newCheckCmd() *cobra.Command {
	var jsonOut bool
	var secrets bool
	cmd := &cobra.Command{
		Use:   "check [path]",
		Short: "Statically analyze .janusignore/.janusmask for conflicts",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return runCheck(dir, jsonOut, secrets)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	cmd.Flags().BoolVar(&secrets, "secrets", false, "also warn about likely secret files/content that currently resolve Allowed")
	return cmd
}

func runCheck(dir string, jsonOut bool, secrets bool) error {
	report, err := check.RunWithOptions(dir, check.Options{Secrets: secrets})
	if err != nil {
		return fmt.Errorf("check: %w", err)
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		var findings []check.Finding
		for _, f := range report.Findings {
			if f.Severity != check.SeverityInfo {
				findings = append(findings, f)
			}
		}
		report.Findings = findings
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

// printCheckReport prints findings grouped by file (Run already
// sorts by severity then file then line), each with file:line and a
// suggested fix where one exists. Only warnings and errors are shown — info
// findings (redundancies, etc.) are suppressed since they're not actionable.
func printCheckReport(report check.Report) {
	var findings []check.Finding
	for _, f := range report.Findings {
		if f.Severity != check.SeverityInfo {
			findings = append(findings, f)
		}
	}

	if len(findings) == 0 {
		fmt.Printf("No problems found across %d files, %d directories.\n", report.FileCount, report.DirCount)
		return
	}

	lastFile := ""
	for _, f := range findings {
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

	errs, warns := 0, 0
	for _, f := range findings {
		switch f.Severity {
		case check.SeverityError:
			errs++
		case check.SeverityWarn:
			warns++
		}
	}
	fmt.Printf("\n%d error(s), %d warning(s) across %d files, %d directories.\n",
		errs, warns, report.FileCount, report.DirCount)
}
