//go:build darwin

package execrunner

import "testing"

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
