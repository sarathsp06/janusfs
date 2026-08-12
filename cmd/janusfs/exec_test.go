package main

import (
	"bytes"
	"strings"
	"testing"
)

// runExecArgs drives the exec cobra command with the given args and returns
// (stdout+stderr merged into help/error text, RunE error). It exists so tests
// can exercise the pre-"--" flag scan (the load-bearing bit for --sandbox)
// without shelling out to a real binary or invoking os.Exit on the happy path.
func runExecArgs(t *testing.T, args []string) (string, error) {
	t.Helper()
	cmd := newExecCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func TestExecFlagParsing(t *testing.T) {
	t.Run("help short flag is honored despite DisableFlagParsing", func(t *testing.T) {
		out, err := runExecArgs(t, []string{"-h"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "--sandbox") {
			t.Fatalf("help text should mention --sandbox, got: %s", out)
		}
	})

	t.Run("help long flag is honored despite DisableFlagParsing", func(t *testing.T) {
		out, err := runExecArgs(t, []string{"--help"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "sandbox-exec") {
			t.Fatalf("help text should describe sandbox-exec, got: %s", out)
		}
	})

	t.Run("missing -- with no args errors with a usage hint", func(t *testing.T) {
		_, err := runExecArgs(t, []string{})
		if err == nil {
			t.Fatal("expected error for missing command")
		}
		if !strings.Contains(err.Error(), "command to run is required") {
			t.Fatalf("expected 'command to run is required' error, got: %v", err)
		}
	})

	t.Run("empty target after -- errors", func(t *testing.T) {
		_, err := runExecArgs(t, []string{"--"})
		if err == nil {
			t.Fatal("expected error for empty command after --")
		}
		if !strings.Contains(err.Error(), "command to run is required") {
			t.Fatalf("expected 'command to run is required' error, got: %v", err)
		}
	})

	t.Run("unknown flag before -- is rejected", func(t *testing.T) {
		_, err := runExecArgs(t, []string{"--nope", "--", "echo", "hi"})
		if err == nil {
			t.Fatal("expected error for unrecognized flag before --")
		}
		if !strings.Contains(err.Error(), "unrecognized flag") {
			t.Fatalf("expected 'unrecognized flag' error, got: %v", err)
		}
	})

	// --sandbox variants: proved accepted by passing the flag-scan and
	// failing later on the "command to run is required" check. Testing the
	// happy path directly would exec a real child and call os.Exit.
	for _, flag := range []string{"--sandbox", "--sandbox=true", "--sandbox=false"} {
		t.Run("accepted flag: "+flag, func(t *testing.T) {
			_, err := runExecArgs(t, []string{flag, "--"})
			if err == nil {
				t.Fatal("expected error for empty command after --")
			}
			if strings.Contains(err.Error(), "unrecognized flag") {
				t.Fatalf("%s should be recognized, got: %v", flag, err)
			}
			if !strings.Contains(err.Error(), "command to run is required") {
				t.Fatalf("expected 'command to run is required', got: %v", err)
			}
		})
	}

	t.Run("--sandbox= (empty value) is rejected", func(t *testing.T) {
		_, err := runExecArgs(t, []string{"--sandbox=", "--", "echo", "hi"})
		if err == nil || !strings.Contains(err.Error(), "unrecognized flag") {
			t.Fatalf("expected 'unrecognized flag' for --sandbox=, got: %v", err)
		}
	})

	t.Run("multiple own flags before -- (all recognized)", func(t *testing.T) {
		_, err := runExecArgs(t, []string{"--sandbox", "--sandbox=false", "--"})
		if err == nil || !strings.Contains(err.Error(), "command to run is required") {
			t.Fatalf("expected 'command to run is required', got: %v", err)
		}
	})
}
