package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRunPatterns_TextIncludesRegexes(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runPatterns(false); err != nil {
			t.Errorf("runPatterns(false) error = %v", err)
		}
	})
	for _, want := range []string{"env-value", "generic-secret", "(?im)\\b(?:password", "whole-file"} {
		if !strings.Contains(out, want) {
			t.Fatalf("patterns output missing %q:\n%s", want, out)
		}
	}
}

func TestRunPatterns_JSONIncludesRegexes(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runPatterns(true); err != nil {
			t.Errorf("runPatterns(true) error = %v", err)
		}
	})
	var infos []struct {
		Name      string   `json:"name"`
		Regexes   []string `json:"regexes"`
		WholeFile bool     `json:"wholeFile"`
	}
	if err := json.Unmarshal([]byte(out), &infos); err != nil {
		t.Fatalf("JSON output should parse: %v\noutput: %s", err, out)
	}
	byName := map[string]struct {
		Name      string   `json:"name"`
		Regexes   []string `json:"regexes"`
		WholeFile bool     `json:"wholeFile"`
	}{}
	for _, info := range infos {
		byName[info.Name] = info
	}
	if len(byName["aws-key"].Regexes) != 2 {
		t.Fatalf("aws-key regexes = %+v, want two regexes", byName["aws-key"].Regexes)
	}
	if !byName["whole-file"].WholeFile || len(byName["whole-file"].Regexes) != 0 {
		t.Fatalf("whole-file = %+v, want sentinel with no regexes", byName["whole-file"])
	}
}
