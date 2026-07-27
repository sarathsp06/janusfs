package patterns

import (
	"strings"
	"testing"
)

// TestBuiltinCorpus is the builtin-pattern corpus of true positives and known
// false-positive traps. Each case names a builtin, a haystack, and the substring
// that must (or must not) end up inside a matched span's capture group.
func TestBuiltinCorpus(t *testing.T) {
	cases := []struct {
		name       string
		builtin    string
		input      string
		wantMasked []string // substrings that must appear inside some match
		wantClean  []string // substrings that must NOT appear inside any match
	}{
		{
			name:       "env-value basic",
			builtin:    "env-value",
			input:      "API_KEY=supersecret\nexport DB_PASS = hunter2\n# COMMENT=notreal",
			wantMasked: []string{"supersecret", "hunter2"},
		},
		{
			name:    "env-value does not mask the key name",
			builtin: "env-value",
			input:   "API_KEY=supersecret",
			// The key itself must survive un-masked spans (group 1 is only
			// the value); check by asserting the match's captured text
			// excludes "API_KEY".
			wantClean: []string{"API_KEY=supersecret"},
		},
		{
			name:       "aws-key access key id",
			builtin:    "aws-key",
			input:      "id = AKIAABCDEFGHIJKLMNOP",
			wantMasked: []string{"AKIAABCDEFGHIJKLMNOP"},
		},
		{
			name:       "aws-key secret access key",
			builtin:    "aws-key",
			input:      "aws_secret_access_key = wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY12",
			wantMasked: []string{"wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY12"},
		},
		{
			name:    "aws-key false-positive trap: short random-looking string",
			builtin: "aws-key",
			input:   "id = AKIASHORT",
			// Too short to match the 16-char suffix requirement.
			wantClean: []string{"AKIASHORT"},
		},
		{
			name:       "private-key PEM block",
			builtin:    "private-key",
			input:      "before\n-----BEGIN RSA PRIVATE KEY-----\nMIIBAAKCAQ==\n-----END RSA PRIVATE KEY-----\nafter",
			wantMasked: []string{"MIIBAAKCAQ=="},
		},
		{
			name:    "private-key false-positive trap: mention without body",
			builtin: "private-key",
			input:   "this file discusses -----BEGIN PRIVATE KEY----- format but has no end marker",
			// No matching END marker means no PEM block match at all.
			wantClean: []string{"this file discusses"},
		},
		{
			name:       "jwt three segments",
			builtin:    "jwt",
			input:      "token: eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dGhpc2lzYXNpZ25hdHVyZQ",
			wantMasked: []string{"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dGhpc2lzYXNpZ25hdHVyZQ"},
		},
		{
			name:    "jwt false-positive trap: two segments only",
			builtin: "jwt",
			input:   "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0",
			wantClean: []string{
				"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0",
			},
		},
		{
			name:       "db-uri user:pass",
			builtin:    "db-uri",
			input:      "postgres://admin:s3cr3t@db.internal:5432/app",
			wantMasked: []string{"admin:s3cr3t"},
		},
		{
			name:    "db-uri false-positive trap: no credentials",
			builtin: "db-uri",
			input:   "postgres://db.internal:5432/app",
			wantClean: []string{
				"postgres://db.internal:5432/app",
			},
		},
		{
			name:       "github-token",
			builtin:    "github-token",
			input:      "ghp_1234567890123456789012345678901234AB",
			wantMasked: []string{"ghp_1234567890123456789012345678901234AB"},
		},
		{
			name:    "github-token false-positive trap: wrong prefix",
			builtin: "github-token",
			input:   "glp_1234567890123456789012345678901234AB",
			wantClean: []string{
				"glp_1234567890123456789012345678901234AB",
			},
		},
		{
			name:       "generic-secret password field",
			builtin:    "generic-secret",
			input:      `password: "hunters2ndaccount"`,
			wantMasked: []string{"hunters2ndaccount"},
		},
		{
			name:    "generic-secret false-positive trap: too short",
			builtin: "generic-secret",
			input:   `password: "ab"`,
			wantClean: []string{
				`password: "ab"`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps, ok := LookupBuiltin(tc.builtin)
			if !ok {
				t.Fatalf("builtin %q not found", tc.builtin)
			}

			var matchedSpans []string
			for _, p := range ps {
				if p.Regex == nil {
					continue
				}
				for _, m := range p.Regex.FindAllStringSubmatch(tc.input, -1) {
					idx := p.GroupIndex
					if idx >= len(m) {
						idx = 0
					}
					matchedSpans = append(matchedSpans, m[idx])
				}
			}

			for _, want := range tc.wantMasked {
				found := false
				for _, span := range matchedSpans {
					if strings.Contains(span, want) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected some match span to contain %q; spans=%v", want, matchedSpans)
				}
			}

			for _, unwanted := range tc.wantClean {
				for _, span := range matchedSpans {
					if strings.Contains(span, unwanted) {
						t.Errorf("match span %q unexpectedly contains full false-positive text %q", span, unwanted)
					}
				}
			}
		})
	}
}

func TestReservedNames(t *testing.T) {
	for _, name := range []string{"env-value", "aws-key", "private-key", "jwt", "db-uri", "github-token", "generic-secret", "whole-file"} {
		if !IsReserved(name) {
			t.Errorf("expected %q to be reserved", name)
		}
	}
	if IsReserved("not-a-builtin") {
		t.Error("unexpected reserved name")
	}
}

func TestParsePatternRefBuiltin(t *testing.T) {
	ps, err := ParsePatternRef("env-value")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Name != "env-value" || !ps[0].Builtin {
		t.Fatalf("unexpected result: %+v", ps)
	}
}

func TestParsePatternRefAWSKeyExpandsToTwoRegexes(t *testing.T) {
	ps, err := ParsePatternRef("aws-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 {
		t.Fatalf("expected aws-key to expand to 2 patterns, got %d", len(ps))
	}
}

func TestParsePatternRefWholeFile(t *testing.T) {
	ps, err := ParsePatternRef("whole-file")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || !ps[0].WholeFile || ps[0].Regex != nil {
		t.Fatalf("unexpected whole-file pattern: %+v", ps[0])
	}
}

func TestParsePatternRefCustomRegex(t *testing.T) {
	ps, err := ParsePatternRef(`/foo(bar)/`)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Builtin || ps[0].GroupIndex != 1 {
		t.Fatalf("unexpected custom pattern: %+v", ps[0])
	}
}

func TestParsePatternRefCustomRegexNoGroup(t *testing.T) {
	ps, err := ParsePatternRef(`/foobar/`)
	if err != nil {
		t.Fatal(err)
	}
	if ps[0].GroupIndex != 0 {
		t.Fatalf("expected group index 0 for group-less regex, got %d", ps[0].GroupIndex)
	}
}

func TestParsePatternRefInvalidCustomRegex(t *testing.T) {
	if _, err := ParsePatternRef(`/[/`); err == nil {
		t.Fatal("expected compile error for invalid regex")
	}
}

func TestParsePatternRefUnknownBuiltin(t *testing.T) {
	if _, err := ParsePatternRef("not-a-builtin"); err == nil {
		t.Fatal("expected error for unknown builtin name")
	}
}

func TestParsePatternRefMalformedCustom(t *testing.T) {
	if _, err := ParsePatternRef(`/unterminated`); err == nil {
		t.Fatal("expected error for missing closing slash")
	}
}

func TestPreFilters(t *testing.T) {
	cases := []struct {
		builtin string
		input   string
		want    bool
	}{
		{"env-value", "API_KEY=supersecret", true},
		{"env-value", "plain text no equals", false},
		{"aws-key", "id = AKIAABCDEFGHIJKLMNOP", true},
		{"aws-key", "id = ASIAABCDEFGHIJKLMNOP", true},
		{"aws-key", "id = ABIAABCDEFGHIJKLMNOP", true},
		{"aws-key", "id = ACCAABCDEFGHIJKLMNOP", true},
		{"aws-key", "aws_secret_access_key = wJalr", true},
		{"aws-key", "id = AKIASHORT", true}, // pre-filter for AKIA matches prefix
		{"aws-key", "just some random text", false},
		{"private-key", "-----BEGIN RSA PRIVATE KEY-----", true},
		{"private-key", "just a comment", false},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9", true},
		{"jwt", "not_a_jwt", false},
		{"db-uri", "postgres://admin", true},
		{"db-uri", "localhost", false},
		{"github-token", "ghp_12345", true},
		{"github-token", "gho_12345", true},
		{"github-token", "ghu_12345", true},
		{"github-token", "ghs_12345", true},
		{"github-token", "ghr_12345", true},
		{"github-token", "glp_12345", false},
		{"generic-secret", "password: hello", true},
		{"generic-secret", "passwd=hello", true},
		{"generic-secret", "secret: hello", true},
		{"generic-secret", "token: hello", true},
		{"generic-secret", "api-key: hello", true},
		{"generic-secret", "api_key: hello", true},
		{"generic-secret", "apikey: hello", true},
		{"generic-secret", "password hello", false}, // missing colon/equals
		{"generic-secret", "just random text", false},
	}

	for _, tc := range cases {
		ps, ok := LookupBuiltin(tc.builtin)
		if !ok {
			t.Fatalf("builtin %q not found", tc.builtin)
		}
		got := false
		for _, p := range ps {
			if p.PreFilter == nil {
				got = true
				break
			}
			if p.PreFilter([]byte(tc.input)) {
				got = true
			}
		}
		if got != tc.want {
			t.Errorf("PreFilter(%q) for input %q: got %t, want %t", tc.builtin, tc.input, got, tc.want)
		}
	}
}
