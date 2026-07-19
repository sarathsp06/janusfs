//go:build darwin

package health

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func checkMacFUSE() MacFUSEStatus {
	s := MacFUSEStatus{}

	paths := []string{
		"/Library/Filesystems/macfuse.fs",
		"/Library/Filesystems/osxfuse.fs",
	}
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			s.Installed = true
			break
		}
	}
	if !s.Installed {
		return s
	}

	cmd := exec.Command("kextstat", "-l")
	out, err := cmd.Output()
	if err == nil {
		output := string(out)
		if strings.Contains(output, "com.github.macfuse") || strings.Contains(output, "com.osxfuse") {
			s.Loaded = true
		}
	}

	verCmd := exec.Command("pkgutil", "--pkg-info", "com.github.macfuse.pkg.MacFUSE")
	if verOut, err := verCmd.Output(); err == nil {
		for _, line := range strings.Split(string(verOut), "\n") {
			if strings.HasPrefix(line, "version: ") {
				s.Version = strings.TrimPrefix(line, "version: ")
				break
			}
		}
	}

	return s
}

func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
