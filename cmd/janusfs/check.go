package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

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
	var matches bool
	var pick bool
	cmd := &cobra.Command{
		Use:   "check [path]",
		Short: "Statically analyze .janusfs.yml for conflicts",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			switch {
			case len(args) == 1:
				dir = args[0]
			case pick:
				d, err := pickCheckDir()
				if err != nil || d == "" {
					return err
				}
				dir = d
			}
			return runCheck(dir, jsonOut, secrets, matches)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	cmd.Flags().BoolVar(&secrets, "secrets", false, "also warn about likely secret files/content that currently resolve Allowed")
	cmd.Flags().BoolVar(&matches, "matches", false, "also list files and directories that currently resolve Hidden or Masked")
	cmd.Flags().BoolVarP(&pick, "interactive", "i", false, "fuzzy-pick a directory to check from active mount sources")
	return cmd
}

// pickCheckDir fuzzy-picks a directory to check from the sources of active
// mounts. Returns "" if the user cancelled.
func pickCheckDir() (string, error) {
	if !interactive {
		return "", errors.New("check -i needs a terminal")
	}
	seen := map[string]bool{}
	var dirs []string
	for _, m := range collectMountListings() {
		if m.Src != "" && !seen[m.Src] {
			seen[m.Src] = true
			dirs = append(dirs, m.Src)
		}
	}
	if len(dirs) == 0 {
		fmt.Println("No mount sources to check.")
		return "", nil
	}
	idx, err := pickOne("check", dirs)
	if err != nil || idx < 0 {
		return "", err
	}
	return dirs[idx], nil
}

func runCheck(dir string, jsonOut bool, secrets bool, matches bool) error {
	report, err := check.RunWithOptions(dir, check.Options{Secrets: secrets, Matches: matches})
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
	if matches {
		printCheckMatches(report.Matches)
	}
	if report.HasErrors() {
		return errSilentNonZero
	}
	return nil
}

// printCheckReport prints findings grouped by file (Run already
// sorts by severity then file then line), each with file:line and a
// suggested fix where one exists. Only warnings and errors are shown — info
// findings (redundancies, etc.) are suppressed since they're not actionable.
func printCheckMatches(matches []check.Match) {
	fmt.Println()
	fmt.Println("Policy matches:")
	if len(matches) == 0 {
		fmt.Println("  none")
		return
	}
	for _, decision := range []string{"HIDDEN", "MASKED"} {
		printedHeader := false
		for _, m := range matches {
			if m.Decision != decision {
				continue
			}
			if !printedHeader {
				fmt.Println()
				fmt.Println(decision)
				printedHeader = true
			}
			extra := m.RuleRef
			if len(m.PatternNames) > 0 {
				extra = strings.TrimSpace(extra + "  [" + strings.Join(m.PatternNames, " ") + "]")
			}
			if extra == "" {
				fmt.Printf("  %s\n", m.Path)
			} else {
				fmt.Printf("  %-32s %s\n", m.Path, extra)
			}
		}
	}
}

func printCheckReport(report check.Report) {
	var findings []check.Finding
	for _, f := range report.Findings {
		if f.Severity != check.SeverityInfo {
			findings = append(findings, f)
		}
	}

	if len(findings) == 0 {
		fmt.Printf("%s No problems found across %d files, %d directories.\n", symGood(), report.FileCount, report.DirCount)
		return
	}

	lastFile := ""
	for _, f := range findings {
		if f.File != lastFile {
			fmt.Printf("\n%s\n", cBold(f.File))
			lastFile = f.File
		}
		loc := ""
		if f.Line > 0 {
			loc = fmt.Sprintf(":%d", f.Line)
		}
		sym, sev := symWarn(), cWarn(f.Severity.String())
		if f.Severity == check.SeverityError {
			sym, sev = symBad(), cBad(f.Severity.String())
		}
		fmt.Printf("  %s [%s]%s %s\n", sym, sev, cDim(loc), f.Message)
		if f.Suggestion != "" {
			fmt.Printf("      %s %s\n", cDim("suggestion:"), f.Suggestion)
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
