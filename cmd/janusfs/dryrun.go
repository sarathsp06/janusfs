package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/sarathsp06/janusfs/internal/check"
	"github.com/sarathsp06/janusfs/internal/execrunner"
)

// printDryRun renders `janusfs exec --dry-run` for a person: the facts in the
// order they matter when deciding whether to launch an agent — what is
// protected, whether this host can actually enforce it, what the agent will
// and will not see, and the one hazard enforcement cannot close.
func printDryRun(w io.Writer, ins execrunner.Inspection, note string) {
	fmt.Fprintf(w, "%s nothing was run — this is what `janusfs exec` would do\n\n", cBold("janusfs exec --dry-run:"))
	fmt.Fprintf(w, "  %-10s %s\n", "source", ins.Src)
	fmt.Fprintf(w, "  %-10s %s\n", "policy", ins.PolicyPath)
	fmt.Fprintf(w, "  %-10s %s\n", "run from", ins.Cwd)
	fmt.Fprintf(w, "  %-10s %s\n", "network", execrunner.NetDescription(ins.NetMode == "none", ins.NetSource))
	if note != "" {
		fmt.Fprintf(w, "  %-10s %s\n", "", cWarn(strings.TrimPrefix(note, "exec: note: ")))
	}
	if ins.Enforcement.Available {
		fmt.Fprintf(w, "  %-10s %s\n", "enforce", cGood("ready")+cDim(" (private mount namespace + FUSE on this host)"))
	} else {
		fmt.Fprintf(w, "  %-10s %s\n", "enforce", cBad("NOT available here")+" — "+firstLine(ins.Enforcement.Reason))
		for _, l := range remedyLines(ins.Enforcement.Reason) {
			fmt.Fprintf(w, "  %-10s %s\n", "", cDim(l))
		}
	}

	fmt.Fprintf(w, "\n  %-10s %d file(s) in %d dir(s): %s hidden, %s masked\n", "files",
		ins.FileCount, ins.DirCount, cBad(fmt.Sprint(len(ins.Hidden))), cWarn(fmt.Sprint(len(ins.Masked))))
	printMatchGroup(w, "hidden", cBad("✗"), ins.Src, ins.Hidden)
	printMatchGroup(w, "masked", cWarn("~"), ins.Src, ins.Masked)

	if len(ins.GitStageable) > 0 {
		fmt.Fprintf(w, "\n  %s %d masked file(s) are stageable by git — `git add` inside the view stages the masked bytes:\n", symWarn(), len(ins.GitStageable))
		for _, p := range capList(ins.GitStageable, 10) {
			fmt.Fprintf(w, "    %s\n", p)
		}
		fmt.Fprintln(w, "    Keep them out of the agent's commits (gitignore them, or commit from outside the view).")
	}

	if errs := errorFindings(ins.Findings); len(errs) > 0 {
		fmt.Fprintf(w, "\n  %s %d policy error(s) — affected paths fail closed (Hidden). Run `janusfs check` for details:\n", symBad(), len(errs))
		for _, f := range capList(errs, 5) {
			fmt.Fprintf(w, "    %s\n", f)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, cDim("Drop --dry-run to launch. `janusfs explain <path>` traces one file's decision."))
}

// printMatchGroup lists one decision's matches. Rule references are shown
// relative to src — ".janusfs.yml:3" rather than the full absolute path — so
// the line stays readable; a global-config rule keeps its absolute path,
// which is the information that matters in that case.
func printMatchGroup(w io.Writer, label, sym, src string, matches []check.Match) {
	if len(matches) == 0 {
		return
	}
	const max = 12
	for i, m := range matches {
		if i == max {
			fmt.Fprintf(w, "    %s ... and %d more (`janusfs check --matches` lists all)\n", cDim(" "), len(matches)-max)
			break
		}
		line := m.Path
		if len(m.PatternNames) > 0 {
			line += cDim(" [" + strings.Join(m.PatternNames, ", ") + "]")
		}
		if m.RuleRef != "" {
			line += cDim("  ← " + strings.TrimPrefix(m.RuleRef, src+"/"))
		}
		lbl := ""
		if i == 0 {
			lbl = label
		}
		fmt.Fprintf(w, "  %-10s %s %s\n", lbl, sym, line)
	}
}

func errorFindings(fs []check.Finding) []string {
	var out []string
	for _, f := range fs {
		if f.Severity == check.SeverityError {
			out = append(out, f.Message)
		}
	}
	return out
}

func capList(items []string, max int) []string {
	if len(items) <= max {
		return items
	}
	return append(append([]string{}, items[:max]...), fmt.Sprintf("... and %d more", len(items)-max))
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

func remedyLines(s string) []string {
	_, rest, ok := strings.Cut(s, "\n")
	if !ok {
		return nil
	}
	return strings.Split(strings.TrimSpace(rest), "\n")
}
