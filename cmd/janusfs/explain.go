package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sarathsp06/janusfs/internal/engine"
	"github.com/sarathsp06/janusfs/internal/rules"
)

// newExplainCmd adds a diagnostics subcommand in the same spirit as `check` and
// `doctor`, but answering "why does this one path resolve the way it does" directly,
// rather than only as a byproduct of a full-tree lint. It resolves path
// (relative to [root], default cwd) through the same internal/engine used
// by the (future) mount, and prints every rule that contributed to the
// final Decision, in the order they were evaluated (global level first,
// then shallowest to deepest .janusignore/.janusmask).
func newExplainCmd() *cobra.Command {
	var jsonOut bool
	var root string

	cmd := &cobra.Command{
		Use:   "explain <path>",
		Short: "Explain why a path resolves to Allowed/Masked/Hidden",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExplain(root, args[0], jsonOut)
		},
	}
	cmd.Flags().StringVar(&root, "root", ".", "mount root to resolve path against")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	return cmd
}

// explainResult is the JSON shape for `janusfs explain --json`.
type explainResult struct {
	Path         string             `json:"path"`
	Decision     string             `json:"decision"`
	RuleRef      string             `json:"ruleRef,omitempty"`
	PatternNames []string           `json:"patternNames,omitempty"`
	Poisoned     bool               `json:"poisoned,omitempty"`
	Trace        []rules.TraceEntry `json:"trace,omitempty"`
}

func runExplain(root, target string, jsonOut bool) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("explain: resolving root %q: %w", root, err)
	}
	fi, err := os.Stat(rootAbs)
	if err != nil {
		return fmt.Errorf("explain: root %q: %w", root, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("explain: root %q must be a directory", root)
	}

	targetAbs := target
	if !filepath.IsAbs(targetAbs) {
		targetAbs = filepath.Join(rootAbs, target)
	}
	targetAbs, err = filepath.Abs(targetAbs)
	if err != nil {
		return fmt.Errorf("explain: resolving %q: %w", target, err)
	}
	relPath, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || relPath == ".." || strings.HasPrefix(relPath, "../") {
		return fmt.Errorf("explain: %q is not under root %q", target, root)
	}

	isDir := false
	if targetFi, err := os.Stat(targetAbs); err == nil {
		isDir = targetFi.IsDir()
	}
	// A nonexistent path is still explainable, since a decision doesn't depend
	// on the target existing — isDir simply defaults to false, the
	// same as any other regular-file resolution.

	eng, err := engine.New(rootAbs)
	if err != nil {
		return fmt.Errorf("explain: %w", err)
	}
	res := eng.Resolve(filepath.ToSlash(relPath), isDir)

	if jsonOut {
		out := explainResult{
			Path: relPath, Decision: res.Decision.String(), RuleRef: res.RuleRef,
			PatternNames: res.PatternNames, Poisoned: res.Poisoned, Trace: res.Trace,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	printExplain(relPath, res)
	return nil
}

func printExplain(relPath string, res engine.Resolution) {
	fmt.Printf("%s -> %s\n", relPath, res.Decision)
	if res.Poisoned {
		fmt.Println("  (forced Hidden: a config error was found while evaluating this path — see `janusfs check`)")
	}
	if len(res.PatternNames) > 0 {
		fmt.Printf("  patterns: %v\n", res.PatternNames)
	}
	if res.RuleRef != "" {
		fmt.Printf("  deciding rule: %s\n", res.RuleRef)
	}
	if len(res.Trace) == 0 {
		fmt.Println("  no rule matched this path at any level; default is Allowed")
		return
	}
	fmt.Println("  evaluation trace (in order applied):")
	for _, t := range res.Trace {
		verdict := "no effect"
		switch t.Kind {
		case "ignore":
			if t.Negated {
				verdict = "re-included (negation)"
				if !t.Matched {
					verdict = "excluded (negation blocked by a global floor or hidden ancestor)"
				}
			} else if t.Matched {
				verdict = "hidden"
			}
		case "mask":
			verdict = "masked"
		case "config_error":
			verdict = "config error — fails closed to Hidden"
		}
		fmt.Printf("    %s:%d %q -> %s\n", t.File, t.LineNo, t.Line, verdict)
	}
}
