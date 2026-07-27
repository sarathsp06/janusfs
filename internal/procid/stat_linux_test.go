//go:build linux

package procid

import "testing"

// The comm field (field 2 of /proc/<pid>/stat) is wrapped in parentheses
// and may contain spaces or parentheses itself. parseStatFields must anchor
// on the LAST ')' in the line, not tokenize from the start — otherwise a
// process named "my (weird) cmd" misaligns every subsequent field, and
// ppid + starttime read off the wrong offsets.
func TestParseStatFieldsHandlesCommWithSpacesAndParens(t *testing.T) {
	// Fabricated after a real /proc/self/stat but with a hostile comm.
	// Post-')-space' fields (index 0 = state, index 1 = ppid, index 19 =
	// starttime): S 4242 ... starttime=999888 at index 19.
	line := []byte("1234 (my (weird) cmd with spaces) S 4242 1 1 0 -1 1077936128 " +
		"100 200 0 0 10 20 30 40 20 0 1 0 999888 12345 678 " +
		"18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0 0 0 0 0")
	fields, err := parseStatFields(line)
	if err != nil {
		t.Fatalf("parseStatFields: %v", err)
	}
	if fields[0] != "S" {
		t.Errorf("state = %q, want %q — anchor is off, so the comm was not fully consumed", fields[0], "S")
	}
	if fields[1] != "4242" {
		t.Errorf("ppid = %q, want %q", fields[1], "4242")
	}
	if fields[19] != "999888" {
		t.Errorf("starttime = %q, want %q", fields[19], "999888")
	}
}

func TestParseStatFieldsRejectsMalformed(t *testing.T) {
	if _, err := parseStatFields([]byte("no closing paren here")); err == nil {
		t.Error("expected an error for a line with no ')'")
	}
	if _, err := parseStatFields([]byte("1 (short)")); err == nil {
		t.Error("expected an error for a line with ')' but nothing after it")
	}
}
