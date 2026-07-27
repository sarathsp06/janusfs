//go:build linux

// Package nsexec is the Linux private-mount-namespace machinery behind
// `janusfs exec`: a capability preflight (Supported), used by
// internal/execrunner/runner_linux.go and cmd/janusfs/nsmount_linux.go before
// any namespace machinery runs, and by `janusfs doctor` to explain a failure
// in terms a user can act on rather than a confusing EPERM deep inside a
// mount call.
package nsexec

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Supported reports whether this kernel and configuration can host an
// unprivileged private-namespace mount, and if not, why.
func Supported() error {
	if err := checkUnprivilegedUserns(); err != nil {
		return err
	}
	if err := checkMaxUserNamespaces(); err != nil {
		return err
	}
	if err := checkKernelVersion(); err != nil {
		return err
	}
	if err := checkDevFuse(); err != nil {
		return err
	}
	return nil
}

// checkUnprivilegedUserns rejects only an explicit "0" (disabled). Debian and
// some hardened kernels expose this knob and default it to 0; a stock
// upstream kernel doesn't expose it at all, and its absence means
// unprivileged user namespaces are simply always allowed — not a failure.
func checkUnprivilegedUserns() error {
	const path = "/proc/sys/kernel/unprivileged_userns_clone"
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if strings.TrimSpace(string(data)) == "0" {
		return fmt.Errorf("nsexec: unprivileged user namespaces are disabled on this kernel (%s=0)\nRemedy: ask an administrator to run `sudo sysctl -w kernel.unprivileged_userns_clone=1`, or mount without `janusfs exec` (the disjoint, non-path-preserving model)", path)
	}
	return nil
}

func checkMaxUserNamespaces() error {
	const path = "/proc/sys/user/max_user_namespaces"
	data, err := os.ReadFile(path)
	if err != nil {
		return nil // knob doesn't exist on this kernel; nothing to check
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return nil
	}
	if v == 0 {
		return fmt.Errorf("nsexec: user namespaces are disabled on this system (%s=0)\nRemedy: ask an administrator to raise this limit, or mount without `janusfs exec` (the disjoint, non-path-preserving model)", path)
	}
	return nil
}

// checkKernelVersion requires >= 4.18, when FUSE became mountable inside an
// unprivileged user namespace (FS_USERNS_MOUNT). Best-effort: an
// unparseable release string doesn't block, since a false negative here
// (refusing on a kernel that would actually work) is worse than a false
// positive (a clearer failure later, at the actual mount call).
func checkKernelVersion() error {
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		return nil
	}
	release := nullTerminatedString(uts.Release[:])
	major, minor, ok := parseKernelVersion(release)
	if !ok {
		return nil
	}
	if major < 4 || (major == 4 && minor < 18) {
		return fmt.Errorf("nsexec: kernel %s predates 4.18, which is required for FUSE mounts inside an unprivileged user namespace (FS_USERNS_MOUNT)\nRemedy: upgrade the kernel, or mount without `janusfs exec` (the disjoint, non-path-preserving model)", release)
	}
	return nil
}

func checkDevFuse() error {
	f, err := os.OpenFile("/dev/fuse", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("nsexec: /dev/fuse is not accessible: %w\nRemedy: install fuse3 (or your distribution's FUSE package) and ensure the kernel module is loaded (`sudo modprobe fuse`)", err)
	}
	f.Close()
	return nil
}
