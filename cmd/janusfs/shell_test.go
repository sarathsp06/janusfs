package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestShellCommand(t *testing.T) {
	t.Run("help describes the boundary and the prompt marker", func(t *testing.T) {
		cmd := newShellCmd()
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetArgs([]string{"--help"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"JANUSFS_EXEC=1", "--net", "Linux only"} {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("help lacks %q", want)
			}
		}
	})

	t.Run("unknown flag is rejected in shell's own words", func(t *testing.T) {
		cmd := newShellCmd()
		cmd.SetArgs([]string{"--bogus"})
		err := cmd.Execute()
		if err == nil || !strings.HasPrefix(err.Error(), "shell: ") || strings.Contains(err.Error(), "before the command") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("login shell falls back to /bin/sh", func(t *testing.T) {
		t.Setenv("SHELL", "")
		if got := loginShell(); got != "/bin/sh" {
			t.Errorf("got %q", got)
		}
		t.Setenv("SHELL", "/usr/bin/fish")
		if got := loginShell(); got != "/usr/bin/fish" {
			t.Errorf("got %q", got)
		}
	})
}
