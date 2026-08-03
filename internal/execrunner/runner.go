//go:build darwin

// On Linux, janusfs exec uses a private mount namespace instead (see
// runner_linux.go): the child sees a filtered view at the source's own path,
// with no path rewriting needed, because the kernel — not a string
// substitution — is what makes the two paths the same path. Everything in
// this file (CWD hijacking and argv rewriting) exists only to simulate that
// path parity on a platform (macOS) with no per-process mount
// namespaces, and is therefore darwin-only.
package execrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sarathsp06/janusfs/internal/control"
)

// daemonRequest, daemonResponse, and mountStatus are local names for the
// shared control-socket protocol types (internal/control), kept as aliases so
// the rest of this file (and its tests) read unchanged. The protocol used to
// be declared a second time here, independently of cmd/janusfs's copy; the two
// could drift silently. internal/control is now the single source of truth
// both import.
type (
	daemonRequest  = control.Request
	daemonResponse = control.Response
	mountStatus    = control.MountStatus
)

// callDaemon dials the daemon control socket and sends one request. Kept as a
// thin, package-local name (rather than calling control.Call directly at every
// use below) so this file's diff against its pre-consolidation form stays
// small; it carries no logic of its own.
func callDaemon(req daemonRequest) (daemonResponse, error) {
	return control.Call(req)
}

func findSourceAndMount(cwd string) (string, string, error) {
	// 1. Get live mounts from daemon
	resp, err := callDaemon(daemonRequest{Cmd: "list"})
	if err != nil {
		return "", "", fmt.Errorf("exec: JanusFS daemon is not running.\nRemedy: Please start the daemon first by running 'janusfs daemon'")
	}
	if !resp.OK {
		return "", "", fmt.Errorf("exec: daemon error listing mounts: %s", resp.Error)
	}

	activeMounts := make(map[string]string) // Src -> Mountpoint
	for _, m := range resp.Mounts {
		srcAbs, err1 := filepath.Abs(m.Src)
		mpAbs, err2 := filepath.Abs(m.Mountpoint)
		if err1 == nil && err2 == nil {
			activeMounts[srcAbs] = mpAbs
		}
	}

	// 2. Walk upwards from cwd
	curr := cwd
	var foundSrc string
	for {
		currAbs, err := filepath.Abs(curr)
		if err != nil {
			break
		}

		// Check if active mount matches this ancestor
		if mp, exists := activeMounts[currAbs]; exists {
			return currAbs, mp, nil
		}

		// Check for a JanusFS policy file.
		if foundSrc == "" {
			if _, err := os.Stat(filepath.Join(currAbs, ".janusfs.yml")); err == nil {
				foundSrc = currAbs
			}
		}

		parent := filepath.Dir(currAbs)
		if parent == currAbs {
			break
		}
		curr = parent
	}

	// Refuse to guess: defaulting to cwd would provision an unpoliced mount over
	// whatever directory the caller happened to be in — for a user running this
	// from their home directory, that means mounting their entire home tree with
	// an empty policy, the opposite of what this tool exists to do.
	if foundSrc == "" {
		return "", "", fmt.Errorf("exec: no JanusFS policy found for %s — refusing to mount an unpoliced tree\nRemedy: run `janusfs init` here, or in the project root above this directory", cwd)
	}

	// Now provision/request mount for foundSrc
	mountResp, err := callDaemon(daemonRequest{Cmd: "mount", Src: foundSrc})
	if err != nil {
		return "", "", fmt.Errorf("exec: JanusFS daemon is not running.\nRemedy: Please start the daemon first by running 'janusfs daemon'")
	}
	if !mountResp.OK {
		return "", "", fmt.Errorf("exec: failed to mount source tree %q: %s", foundSrc, mountResp.Error)
	}

	if len(mountResp.Mounts) == 0 {
		return "", "", fmt.Errorf("exec: daemon returned empty mount list for %q", foundSrc)
	}

	return foundSrc, mountResp.Mounts[0].Mountpoint, nil
}

func Run(ctx context.Context, targetArgs []string, sandbox bool) (int, error) {
	if len(targetArgs) == 0 {
		return 125, fmt.Errorf("exec: no command specified to execute")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return 125, fmt.Errorf("exec: failed to get current working directory: %w", err)
	}

	src, mountpoint, err := findSourceAndMount(cwd)
	if err != nil {
		return 125, err
	}

	// Poll readiness up to 2,000 ms
	ready := false
	start := time.Now()
	for time.Since(start) < 2000*time.Millisecond {
		if _, err := os.Stat(filepath.Join(mountpoint, ".janusfs")); err == nil {
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !ready {
		return 125, fmt.Errorf("exec: mount point %q failed to become ready within 2,000 ms", mountpoint)
	}

	// Hijack CWD
	hijackedCWD := mountpoint
	realCWD, err := filepath.Abs(cwd)
	if err == nil {
		srcAbs, err1 := filepath.Abs(src)
		mpAbs, err2 := filepath.Abs(mountpoint)
		if err1 == nil && err2 == nil {
			if realCWD == srcAbs {
				hijackedCWD = mpAbs
			} else if strings.HasPrefix(realCWD, srcAbs+string(filepath.Separator)) {
				rel, err := filepath.Rel(srcAbs, realCWD)
				if err == nil {
					hijackedCWD = filepath.Join(mpAbs, rel)
				}
			}
		}
	}

	// Forward argument path translation
	finalArgs := make([]string, len(targetArgs))
	for i, arg := range targetArgs {
		finalArgs[i] = string(ReplacePaths([]byte(arg), []byte(src), []byte(mountpoint)))
	}

	// Scrub environment
	env := os.Environ()
	var scrubbedEnv []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, "JANUSFS_") {
			scrubbedEnv = append(scrubbedEnv, kv)
		}
	}

	// Seatbelt confinement (opt-in): wrap finalArgs so the child process
	// tree cannot read or write the real source at its own path, while the
	// disjoint mountpoint above stays fully usable. This is additive to
	// everything above (mount discovery, CWD hijack, argv rewrite, env
	// scrub) — none of that changes.
	if sandbox {
		if err := sandboxAvailable(); err != nil {
			// Fail closed: a user who asked for confinement and didn't get
			// it is the worst outcome, so refuse rather than run the child
			// unsandboxed.
			return 125, fmt.Errorf("exec: %w", err)
		}

		denyRW, err := canonicalDenyTargets(src)
		if err != nil {
			return 125, fmt.Errorf("exec: --sandbox: %w", err)
		}

		var denyRO []string
		if home, herr := os.UserHomeDir(); herr == nil {
			denyRO, err = canonicalReadOnlyDenyTargets(home)
			if err != nil {
				return 125, fmt.Errorf("exec: --sandbox: %w", err)
			}
		}

		mustAllow, err := canonicalizeWithFirmlinkTwin(mountpoint)
		if err != nil {
			return 125, fmt.Errorf("exec: --sandbox: %w", err)
		}

		profile, err := sandboxProfile(denyRW, denyRO, mustAllow)
		if err != nil {
			return 125, fmt.Errorf("exec: --sandbox: %w", err)
		}

		sandboxArgs := append([]string{"-p", profile, "--"}, finalArgs...)
		finalArgs = append([]string{sandboxExecPath}, sandboxArgs...)
	}

	// Set up command
	cmd := exec.CommandContext(ctx, finalArgs[0], finalArgs[1:]...)
	cmd.Dir = hijackedCWD
	cmd.Env = scrubbedEnv
	cmd.Stdin = os.Stdin

	// Preserve stdout/stderr exactly. In particular, keep TTY identity for
	// interactive CLIs; macOS path parity cannot be made correct by rewriting
	// output bytes after the fact.
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Start command
	if err := cmd.Start(); err != nil {
		return 125, fmt.Errorf("exec: failed to start process: %w", err)
	}

	// Signal forwarding
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for {
			select {
			case sig, ok := <-sigChan:
				if !ok {
					return
				}
				if cmd.Process != nil {
					_ = cmd.Process.Signal(sig)
				}
			case <-ctx.Done():
				if cmd.Process != nil {
					_ = cmd.Process.Signal(syscall.SIGTERM)
				}
				return
			}
		}
	}()

	// Wait for process completion
	waitErr := cmd.Wait()

	// Clean up signal listener
	signal.Stop(sigChan)
	close(sigChan)

	if waitErr != nil {
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			return exitError.ExitCode(), nil
		}
		return 1, waitErr
	}

	return 0, nil
}
