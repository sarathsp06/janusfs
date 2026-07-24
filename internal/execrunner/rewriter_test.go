package execrunner

import (
	"bytes"
	"testing"
)

func TestReplacePaths(t *testing.T) {
	oldPath := []byte("/Users/dev/projects/app")
	newPath := []byte("/Users/dev/.janusfs/mounts/app")

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "exact match",
			input:    "/Users/dev/projects/app",
			expected: "/Users/dev/.janusfs/mounts/app",
		},
		{
			name:     "subdirectory match",
			input:    "/Users/dev/projects/app/src/main.go",
			expected: "/Users/dev/.janusfs/mounts/app/src/main.go",
		},
		{
			name:     "sibling no match (dash suffix)",
			input:    "/Users/dev/projects/app-other",
			expected: "/Users/dev/projects/app-other",
		},
		{
			name:     "sibling no match (alphanumeric suffix)",
			input:    "/Users/dev/projects/app2",
			expected: "/Users/dev/projects/app2",
		},
		{
			name:     "different prefix no match",
			input:    "my/Users/dev/projects/app",
			expected: "my/Users/dev/projects/app",
		},
		{
			name:     "CWD variable assignment match",
			input:    "CWD=/Users/dev/projects/app",
			expected: "CWD=/Users/dev/.janusfs/mounts/app",
		},
		{
			name:     "quoted path match",
			input:    "\"/Users/dev/projects/app/foo\"",
			expected: "\"/Users/dev/.janusfs/mounts/app/foo\"",
		},
		{
			name:     "multiple matches",
			input:    "/Users/dev/projects/app and /Users/dev/projects/app/bar",
			expected: "/Users/dev/.janusfs/mounts/app and /Users/dev/.janusfs/mounts/app/bar",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ReplacePaths([]byte(tc.input), oldPath, newPath)
			if string(got) != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, string(got))
			}
		})
	}
}

func TestStreamRewriter(t *testing.T) {
	var out bytes.Buffer
	oldPath := "/Users/dev/projects/app"
	newPath := "/Users/dev/.janusfs/mounts/app"

	sr := NewStreamRewriter(&out, oldPath, newPath)

	// Write in chunks to verify chunk boundary stitching.
	chunks := []string{
		"some text ",
		"/Users/dev/p",
		"rojects/app",
		"/src/main.go and ",
		"/Users/dev/projects/app-",
		"other sibling.",
	}

	for _, chunk := range chunks {
		n, err := sr.Write([]byte(chunk))
		if err != nil {
			t.Fatalf("unexpected write error: %v", err)
		}
		if n != len(chunk) {
			t.Fatalf("expected to write %d bytes, wrote %d", len(chunk), n)
		}
	}

	if err := sr.Flush(); err != nil {
		t.Fatalf("unexpected flush error: %v", err)
	}

	expected := "some text /Users/dev/.janusfs/mounts/app/src/main.go and /Users/dev/projects/app-other sibling."
	if out.String() != expected {
		t.Errorf("expected %q, got %q", expected, out.String())
	}
}
