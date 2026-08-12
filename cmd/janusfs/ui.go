package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/mattn/go-isatty"
)

// colorEnabled reports whether ANSI color should be emitted to stdout: only on
// a real terminal, and never when NO_COLOR is set (https://no-color.org).
// Computed once at startup — janusfs is a short-lived CLI, stdout doesn't change
// under it.
var colorEnabled = func() bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	return isatty.IsTerminal(os.Stdout.Fd())
}()

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
)

func paint(code, s string) string {
	if !colorEnabled {
		return s
	}
	return code + s + ansiReset
}

func cGood(s string) string { return paint(ansiGreen, s) }
func cBad(s string) string  { return paint(ansiRed, s) }
func cWarn(s string) string { return paint(ansiYellow, s) }
func cDim(s string) string  { return paint(ansiDim, s) }
func cBold(s string) string { return paint(ansiBold, s) }

// Status glyphs, already colored. Plain ASCII-safe symbols so they degrade
// cleanly when color is off.
func symGood() string { return cGood("✓") }
func symBad() string  { return cBad("✗") }
func symWarn() string { return cWarn("⚠") }

// interactive reports whether an interactive picker can run: both stdin and
// stdout must be a terminal (not piped or redirected). Callers gate on this
// before offering fuzzy selection so scripted invocations fail fast with a
// hint instead of hanging on a prompt.
var interactive = isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())

// pickOne shows items and returns the chosen index, or -1 if the user
// cancelled. It shells out to fzf when it's on PATH (real fuzzy search) and
// falls back to a numbered menu otherwise. Callers must check `interactive`
// first — this does not.
func pickOne(prompt string, items []string) (int, error) {
	if fzf, err := exec.LookPath("fzf"); err == nil {
		return pickWithFzf(fzf, prompt, items)
	}
	return pickWithMenu(prompt, items)
}

// pickWithFzf pipes "index\tdisplay" lines to fzf and reads back the chosen
// line's index. --with-nth=2.. hides the index column from the UI; the index
// survives the round-trip so the caller maps it back to its own data without
// re-parsing the display string.
func pickWithFzf(fzf, prompt string, items []string) (int, error) {
	var in strings.Builder
	for i, it := range items {
		fmt.Fprintf(&in, "%d\t%s\n", i, it)
	}
	cmd := exec.Command(fzf, "--prompt="+prompt+"> ", "--height=40%", "--reverse",
		"--delimiter=\t", "--with-nth=2..")
	cmd.Stdin = strings.NewReader(in.String())
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		// fzf exits 130 when the user cancels (Esc / Ctrl-C) — not an error.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 130 {
			return -1, nil
		}
		return -1, fmt.Errorf("fzf: %w", err)
	}
	idxStr, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\t")
	idx, err := strconv.Atoi(idxStr)
	if err != nil || idx < 0 || idx >= len(items) {
		return -1, nil
	}
	return idx, nil
}

// pickWithMenu is the no-fzf fallback: a numbered list read from stdin.
func pickWithMenu(prompt string, items []string) (int, error) {
	for i, it := range items {
		fmt.Printf("  %s %s\n", cDim(fmt.Sprintf("%d)", i+1)), it)
	}
	fmt.Printf("%s [1-%d, blank to cancel]: ", prompt, len(items))
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return -1, nil
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(items) {
		return -1, fmt.Errorf("invalid selection %q", line)
	}
	return n - 1, nil
}
