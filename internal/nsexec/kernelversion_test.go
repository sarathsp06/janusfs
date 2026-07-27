package nsexec

import "testing"

func TestParseKernelVersion(t *testing.T) {
	cases := []struct {
		release   string
		wantMajor int
		wantMinor int
		wantOK    bool
	}{
		{"5.15.0-91-generic", 5, 15, true},
		{"6.8.0-1015-aws", 6, 8, true},
		{"4.18.0-425.3.1.el8.x86_64", 4, 18, true},
		{"4.17.19-8", 4, 17, true},
		{"6.6.30", 6, 6, true},
		{"garbage", 0, 0, false},
		{"5", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, c := range cases {
		major, minor, ok := parseKernelVersion(c.release)
		if ok != c.wantOK {
			t.Errorf("parseKernelVersion(%q) ok = %v, want %v", c.release, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if major != c.wantMajor || minor != c.wantMinor {
			t.Errorf("parseKernelVersion(%q) = (%d, %d), want (%d, %d)", c.release, major, minor, c.wantMajor, c.wantMinor)
		}
	}
}

func TestNullTerminatedString(t *testing.T) {
	cases := []struct {
		in   []byte
		want string
	}{
		{[]byte("5.15.0\x00\x00\x00"), "5.15.0"},
		{[]byte("no-null-here"), "no-null-here"},
		{[]byte{0, 0, 0}, ""},
		{[]byte{}, ""},
	}
	for _, c := range cases {
		if got := nullTerminatedString(c.in); got != c.want {
			t.Errorf("nullTerminatedString(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
