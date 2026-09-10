//go:build linux

package health

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func checkMacFUSE() MacFUSEStatus {
	s := MacFUSEStatus{}
	// On Linux, FUSE is usable if /dev/fuse is accessible.
	if _, err := os.Stat("/dev/fuse"); err == nil {
		s.Installed = true
		s.Loaded = true
	}
	return s
}

func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// platformWarnings diagnoses the two Linux prerequisites `janusfs exec`
// depends on — a usable /dev/fuse and unprivileged user namespaces — and
// names the exact remedy, including the container case, where the fix is a
// flag on the container runtime rather than anything inside the box.
func platformWarnings() []string {
	var w []string

	inContainer := false
	if _, err := os.Stat("/.dockerenv"); err == nil {
		inContainer = true
	} else if _, err := os.Stat("/run/.containerenv"); err == nil {
		inContainer = true
	}

	if _, err := os.Stat("/dev/fuse"); err != nil {
		if inContainer {
			w = append(w, "/dev/fuse is missing and this looks like a container: re-run the container with `--device /dev/fuse` (docker) or an equivalent device mapping")
		} else {
			w = append(w, "/dev/fuse is missing: install the fuse3 package (and load the fuse kernel module) before mounting")
		}
	}

	if _, err := exec.LookPath("fusermount3"); err != nil {
		if _, err := exec.LookPath("fusermount"); err != nil {
			w = append(w, "neither fusermount3 nor fusermount is on PATH: install fuse3; unprivileged mounts and unmount recovery need it")
		}
	}

	// Debian/Ubuntu gate unprivileged user namespaces behind this sysctl;
	// on kernels without the file, absence means no such gate exists.
	if data, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		if strings.TrimSpace(string(data)) == "0" {
			w = append(w, "unprivileged user namespaces are disabled (kernel.unprivileged_userns_clone=0): `janusfs exec` cannot create its mount namespace; enable with `sysctl -w kernel.unprivileged_userns_clone=1`")
		}
	}
	if data, err := os.ReadFile("/proc/sys/user/max_user_namespaces"); err == nil {
		if strings.TrimSpace(string(data)) == "0" {
			w = append(w, "user namespaces are disabled (user.max_user_namespaces=0): `janusfs exec` cannot run; raise the sysctl"+containerHint(inContainer))
		}
	}

	return w
}

func containerHint(inContainer bool) string {
	if inContainer {
		return " on the host, or run the container with `--privileged`/a userns-enabled runtime"
	}
	return ""
}
