package main

import (
	"bytes"
	"strings"
	"testing"
)

// runExecArgs drives the exec cobra command with the given args and returns
// (stdout+stderr merged into help/error text, RunE error). It exists so tests
// can exercise the pre-"--" arg scan without shelling out to a real binary or
// invoking os.Exit on the happy path.
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
		if !strings.Contains(out, "sanitized view") {
			t.Fatalf("help text should describe the sanitized view, got: %s", out)
		}
	})

	t.Run("help long flag is honored despite DisableFlagParsing", func(t *testing.T) {
		out, err := runExecArgs(t, []string{"--help"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "Linux only") {
			t.Fatalf("help text should state JanusFS runs on Linux only, got: %s", out)
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

	t.Run("anything before -- is rejected", func(t *testing.T) {
		_, err := runExecArgs(t, []string{"--sandbox", "--", "echo", "hi"})
		if err == nil || !strings.Contains(err.Error(), "unrecognized flag") {
			t.Fatalf("expected 'unrecognized flag' before --, got: %v", err)
		}
	})
}

func TestParseExecOwnArgs(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		defaultMode string
		wantDN      bool
		wantErr     bool
	}{
		{"no flags uses host default", nil, "host", false, false},
		{"no flags honors none default from config", nil, "none", true, false},
		{"flag overrides none default with host", []string{"--net=host"}, "none", false, false},
		{"flag overrides host default with none", []string{"--net=none"}, "host", true, false},
		{"invalid config default rejected", nil, "bridge", false, true},
		{"unknown net value", []string{"--net=bridge"}, "host", false, true},
		{"unknown flag", []string{"--sandbox"}, "host", false, true},
		{"space form is not accepted", []string{"--net", "none"}, "host", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dn, err := parseExecOwnArgs(tc.args, tc.defaultMode)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err == nil && dn != tc.wantDN {
				t.Fatalf("denyNetwork = %v, want %v", dn, tc.wantDN)
			}
		})
	}
}
