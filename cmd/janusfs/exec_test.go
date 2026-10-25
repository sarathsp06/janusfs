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
		if !strings.Contains(out, "filtered view") {
			t.Fatalf("help text should describe the filtered view, got: %s", out)
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

	t.Run("flags are recognised without a -- separator", func(t *testing.T) {
		own, target := splitExecArgs([]string{"--net=none", "-q", "aider", "--model", "x"})
		if strings.Join(own, " ") != "--net=none -q" || strings.Join(target, " ") != "aider --model x" {
			t.Fatalf("own=%v target=%v", own, target)
		}
		own, target = splitExecArgs([]string{"--dry-run"})
		if len(own) != 1 || len(target) != 0 {
			t.Fatalf("own=%v target=%v", own, target)
		}
		// An explicit -- always wins, even if what precedes it looks like a flag.
		own, target = splitExecArgs([]string{"--quiet", "--", "--net=none"})
		if strings.Join(own, " ") != "--quiet" || strings.Join(target, " ") != "--net=none" {
			t.Fatalf("own=%v target=%v", own, target)
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
		name    string
		args    []string
		want    execInvocation
		wantErr bool
	}{
		{"no flags", nil, execInvocation{}, false},
		{"net flag is captured verbatim", []string{"--net=none"}, execInvocation{netFlag: "none"}, false},
		{"quiet long and short", []string{"--quiet"}, execInvocation{quiet: true}, false},
		{"quiet short", []string{"-q"}, execInvocation{quiet: true}, false},
		{"dry-run with json", []string{"--dry-run", "--json"}, execInvocation{dryRun: true, jsonOut: true}, false},
		{"json without dry-run is rejected", []string{"--json"}, execInvocation{}, true},
		{"unknown flag", []string{"--sandbox"}, execInvocation{}, true},
		{"space form is not accepted", []string{"--net", "none"}, execInvocation{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseExecOwnArgs(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestResolveNetMode(t *testing.T) {
	cases := []struct {
		name       string
		flag       string
		policyNet  string
		cfgNet     string
		wantDeny   bool
		wantSource string
		wantNote   bool
		wantErr    bool
	}{
		{"built-in default is host", "", "", "host", false, "default", false, false},
		{"settings or env none applies", "", "", "none", true, "settings/env", false, false},
		{"repo policy none tightens the default", "", "none", "host", true, ".janusfs.yml exec.net", false, false},
		{"repo policy host cannot loosen settings none", "", "host", "none", true, "settings/env", false, false},
		{"flag none wins over default", "none", "", "host", true, "--net flag", false, false},
		{"flag host wins over settings none", "host", "", "none", false, "--net flag", false, false},
		{"flag host loosening repo none is noted", "host", "none", "host", false, "--net flag", true, false},
		{"flag none matching repo none is not noted", "none", "none", "host", true, "--net flag", false, false},
		{"invalid flag value", "bridge", "", "host", false, "", false, true},
		{"invalid settings value", "", "", "bridge", false, "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deny, source, note, err := resolveNetMode(tc.flag, tc.policyNet, tc.cfgNet)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if deny != tc.wantDeny || source != tc.wantSource || (note != "") != tc.wantNote {
				t.Fatalf("got deny=%v source=%q note=%q", deny, source, note)
			}
		})
	}
}

func TestExecRequiresCommandAndPointsAtShell(t *testing.T) {
	_, err := runExecArgs(t, []string{"--quiet"})
	if err == nil || !strings.Contains(err.Error(), "janusfs shell") {
		t.Fatalf("expected the missing-command error to mention `janusfs shell`, got: %v", err)
	}
}
