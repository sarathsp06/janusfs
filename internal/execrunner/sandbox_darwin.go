//go:build darwin

// Seatbelt (sandbox-exec) confinement, opt-in via --sandbox, gives janusfs
// exec on macOS a real kernel-enforced deny boundary: it denies read and
// write of the real source subtree while leaving the disjoint mountpoint
// (the filtered view) fully usable. It cannot do what the Linux mount
// namespace does — rewrite bytes — so Masked files are still served by
// FUSE; this only stops the child reaching the real, unmasked path.
package execrunner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const sandboxExecPath = "/usr/bin/sandbox-exec"

// sandboxAvailable reports whether sandbox-exec can be invoked. --sandbox
// must fail closed when it can't confirm this, never silently run the child
// unsandboxed.
func sandboxAvailable() error {
	info, err := os.Stat(sandboxExecPath)
	if err != nil {
		return fmt.Errorf("--sandbox requires %s, which was not found: %w", sandboxExecPath, err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("--sandbox requires %s to be an executable file", sandboxExecPath)
	}
	return nil
}

// canonicalizeWithFirmlinkTwin resolves p to its canonical form and, if that
// form sits under an APFS firmlink root, also returns the firmlink twin
// path. Seatbelt matches the resolved path, not the string a caller passed
// in, so a deny rule must name every form a path can be reached by: a path
// reachable through the un-denied form is a silent, fail-open bypass, and
// whether Seatbelt collapses firmlinks on its own is unverified — denying
// both forms is the safe assumption.
func canonicalizeWithFirmlinkTwin(p string) ([]string, error) {
	canon, err := filepath.EvalSymlinks(p)
	if err != nil {
		return nil, fmt.Errorf("resolving canonical path for %q: %w", p, err)
	}

	targets := []string{canon}

	const dataVolume = "/System/Volumes/Data"
	firmlinkRoots := []string{"/Users", "/Applications", "/Library", "/private/var"}

	if strings.HasPrefix(canon, dataVolume+"/") {
		targets = append(targets, strings.TrimPrefix(canon, dataVolume))
	} else {
		for _, root := range firmlinkRoots {
			if canon == root || strings.HasPrefix(canon, root+"/") {
				targets = append(targets, dataVolume+canon)
				break
			}
		}
	}

	return targets, nil
}

// canonicalDenyTargets computes the read+write deny set for the real source
// tree (src, plus its firmlink twin).
func canonicalDenyTargets(src string) ([]string, error) {
	return canonicalizeWithFirmlinkTwin(src)
}

// canonicalReadOnlyDenyTargets computes the read-only deny set: ~/.janusfs,
// denied as cheap defense-in-depth so a confined child can't read JanusFS's
// own config/state (mount registry, settings) even though it isn't the
// source tree being protected. Returns nil, nil if home is empty or
// ~/.janusfs does not exist — this is hardening, not a load-bearing control,
// so its absence is not an error.
func canonicalReadOnlyDenyTargets(home string) ([]string, error) {
	if home == "" {
		return nil, nil
	}
	janusHome := filepath.Join(home, ".janusfs")
	if _, err := os.Stat(janusHome); err != nil {
		return nil, nil
	}
	return canonicalizeWithFirmlinkTwin(janusHome)
}

// sandboxProfile renders a Seatbelt profile: allow everything by default,
// deny read+write of denyReadWrite (the real source tree), deny read-only of
// denyReadOnly (~/.janusfs), then — last, so it wins over every deny above —
// explicitly re-allow read+write of mustAllow (the mountpoint the agent is
// actually meant to use).
//
// The explicit re-allow exists because denyReadOnly is not guaranteed
// disjoint from the mountpoint: the default mount root is
// ~/.janusfs/mounts/..., so denying ~/.janusfs for hardening would also deny
// the mountpoint itself when the user hasn't customized --root. Rather than
// special-case that one collision, mustAllow is asserted last and wins by
// construction (Seatbelt profiles are last-match-wins) regardless of which
// deny rule would otherwise have covered it — including ones added later.
func sandboxProfile(denyReadWrite, denyReadOnly, mustAllow []string) (string, error) {
	if len(denyReadWrite) == 0 {
		return "", fmt.Errorf("sandbox profile: no deny targets given")
	}
	if len(mustAllow) == 0 {
		return "", fmt.Errorf("sandbox profile: no mountpoint given to re-allow")
	}

	quote := func(paths []string) (string, error) {
		var quoted []string
		for _, t := range paths {
			if strings.ContainsAny(t, "\"\n") {
				// Can't come from a real mount path, but an unescaped quote
				// or newline would corrupt the profile and fail open —
				// refuse rather than risk emitting an unintended
				// (allow default)-only profile.
				return "", fmt.Errorf("sandbox profile: deny path %q contains a quote or newline", t)
			}
			quoted = append(quoted, fmt.Sprintf("(subpath %q)", t))
		}
		return strings.Join(quoted, " "), nil
	}

	rwClause, err := quote(denyReadWrite)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	fmt.Fprintf(&b, "(deny file-read* %s)\n", rwClause)
	fmt.Fprintf(&b, "(deny file-write* %s)\n", rwClause)

	if len(denyReadOnly) > 0 {
		roClause, err := quote(denyReadOnly)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "(deny file-read* %s)\n", roClause)
	}

	allowClause, err := quote(mustAllow)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "(allow file-read* %s)\n", allowClause)
	fmt.Fprintf(&b, "(allow file-write* %s)\n", allowClause)

	return b.String(), nil
}
