package rules

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writePolicy(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, PolicyFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadExecPolicy(t *testing.T) {
	t.Run("missing file is the zero policy", func(t *testing.T) {
		p, err := ReadExecPolicy(t.TempDir())
		if err != nil || p.Net != "" {
			t.Fatalf("got %+v, %v; want zero policy, nil", p, err)
		}
	})

	t.Run("missing section is the zero policy", func(t *testing.T) {
		dir := t.TempDir()
		writePolicy(t, dir, "version: 1\nhide: ['*.pem']\n")
		p, err := ReadExecPolicy(dir)
		if err != nil || p.Net != "" {
			t.Fatalf("got %+v, %v; want zero policy, nil", p, err)
		}
	})

	t.Run("net none is read", func(t *testing.T) {
		dir := t.TempDir()
		writePolicy(t, dir, "version: 1\nexec:\n  net: none\n")
		p, err := ReadExecPolicy(dir)
		if err != nil || p.Net != "none" {
			t.Fatalf("got %+v, %v; want net=none", p, err)
		}
	})

	t.Run("invalid net value is refused", func(t *testing.T) {
		dir := t.TempDir()
		writePolicy(t, dir, "version: 1\nexec:\n  net: bridge\n")
		_, err := ReadExecPolicy(dir)
		if !errors.Is(err, ErrInvalidExecNet) {
			t.Fatalf("err = %v; want ErrInvalidExecNet", err)
		}
	})

	t.Run("unparseable yaml is an error", func(t *testing.T) {
		dir := t.TempDir()
		writePolicy(t, dir, "version: [\n")
		if _, err := ReadExecPolicy(dir); err == nil {
			t.Fatal("expected a parse error")
		}
	})
}
