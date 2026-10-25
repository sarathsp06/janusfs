package execrunner

import (
	"strings"
	"testing"
)

func TestSessionStatsSummary(t *testing.T) {
	t.Run("empty session", func(t *testing.T) {
		var s SessionStats
		s.Record("lookup", "main.go", "ALLOWED", 0)
		got := s.Summary()
		if !strings.Contains(got, "no masked or hidden paths were touched") || !strings.Contains(got, "1 fs op(s)") {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("counts masked reads and hidden denials by distinct path", func(t *testing.T) {
		var s SessionStats
		s.Record("open", ".env", "MASKED", 0)   // open is not a read; not counted as a masked read
		s.Record("read", ".env", "MASKED", 100) // counted once per path...
		s.Record("read", ".env", "MASKED", 100) // ...however many reads
		s.Record("read", "config/app.yml", "MASKED", 5)
		s.Record("open", "id_rsa", "HIDDEN", 0)
		s.Record("read", "id_rsa", "HIDDEN", 0)
		s.Record("resolve", "x", "PANIC", 0)
		got := s.Summary()
		for _, want := range []string{
			"2 masked file(s) read (.env, config/app.yml)",
			"1 hidden path(s) denied (id_rsa)",
			"1 internal fault(s) failed closed",
			"7 fs op(s)",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("summary %q lacks %q", got, want)
			}
		}
	})

	t.Run("truncates long path lists", func(t *testing.T) {
		var s SessionStats
		for _, p := range []string{"d", "c", "b", "a", "e"} {
			s.Record("read", p, "MASKED", 1)
		}
		got := s.Summary()
		if !strings.Contains(got, "(a, b, c, +2 more)") {
			t.Fatalf("got %q", got)
		}
	})
}

func TestWriteBanner(t *testing.T) {
	plan := Plan{Src: "/home/me/app", PolicyPath: "/home/me/app/.janusfs.yml"}
	var buf strings.Builder
	WriteBanner(&buf, plan, Options{DenyNetwork: true, NetSource: ".janusfs.yml exec.net"})
	got := buf.String()
	if !strings.HasPrefix(got, "janusfs exec: boundary up — /home/me/app (policy .janusfs.yml)\n") {
		t.Errorf("first line: %q", got)
	}
	if strings.Contains(got, "nested") {
		t.Errorf("non-nested banner must not mention nesting: %q", got)
	}
	buf.Reset()
	WriteBanner(&buf, plan, Options{Nested: true})
	if !strings.Contains(buf.String(), "nested: already inside a janusfs exec boundary") {
		t.Errorf("nested banner: %q", buf.String())
	}
}

func TestNetDescription(t *testing.T) {
	if got := NetDescription(true, ".janusfs.yml exec.net"); got != "none (loopback only) · from .janusfs.yml exec.net" {
		t.Errorf("got %q", got)
	}
	if got := NetDescription(false, ""); !strings.HasPrefix(got, "host (not isolated") {
		t.Errorf("got %q", got)
	}
}
