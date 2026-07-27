package backing

import "testing"

func TestValidRel(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
	}{
		{"..", false},
		{"a/../b", false},
		{"/abs", false},
		{"a//b", false},
		{"", false},
		{"a/../../etc/passwd", false},
		{"../../../etc/passwd", false},
		{".", true},
		{"a", true},
		{"a/b", true},
		{"a/b/c", true},
		{"..foo", true},
		{"a/..foo/b", true},
		{"foo..", true},
		{"...", true}, // three literal dots is a valid filename component, not ".."
	}
	for _, c := range cases {
		err := validRel(c.rel)
		got := err == nil
		if got != c.want {
			t.Errorf("validRel(%q) valid = %v (err=%v), want %v", c.rel, got, err, c.want)
		}
	}
}
