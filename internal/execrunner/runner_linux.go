//go:build linux

// On Linux, janusfs exec gives the child process tree a filtered view of the
// project at the project's OWN absolute path, using a private mount
// namespace, instead of the path-rewriting darwin uses (see runner.go's
// package doc, built only on darwin). Every process outside the namespace
// keeps reading the real filesystem directly, at native speed, and never
// enters FUSE.
//
// This file is Stage 1: the launcher. It discovers the source tree, then
// re-execs the janusfs binary with CLONE_NEWNS|CLONE_NEWUSER so the clone(2)
// that creates the child also creates its private mount and user namespaces —
// unshare(2) cannot do this from within a running Go program, because it only
// affects the calling OS thread, and the Go runtime migrates goroutines
// across threads freely. Stage 2 (cmd/janusfs/nsmount_linux.go, invoked as
// `janusfs __nsmount`) runs inside those namespaces and does the actual
// mounting.
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

	"github.com/sarathsp06/janusfs/internal/nsexec"
)

// Run is the Linux entry point for `janusfs exec -- <command> [args...]`.
func Run(ctx context.Context, targetArgs []string) (int, error) {
	if len(targetArgs) == 0 {
		return 125, errors.New("exec: no command specified to execute")
	}

	if err := nsexec.Supported(); err != nil {
		return 125, fmt.Errorf("exec: %w", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return 125, fmt.Errorf("exec: failed to get current working directory: %w", err)
	}

	src, err := discoverSourceRoot(cwd)
	if err != nil {
		return 125, err
	}

	self, err := os.Executable()
	if err != nil {
		return 125, fmt.Errorf("exec: resolving own executable path: %w", err)
	}

	// Scrub JANUSFS_* from the child's environment, same as the darwin path:
	// the child must not be able to read or influence JanusFS configuration.
	env := os.Environ()
	scrubbedEnv := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "JANUSFS_") {
			scrubbedEnv = append(scrubbedEnv, kv)
		}
	}

	nsArgs := append([]string{"__nsmount", "--src", src, "--"}, targetArgs...)
	cmd := exec.CommandContext(ctx, self, nsArgs...)
	cmd.Dir = cwd // no CWD hijack needed: the same path is valid on both sides
	cmd.Env = scrubbedEnv
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout // no stream rewriting needed: paths already match
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNS | syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
		// Required for an unprivileged process to write its own gid_map: the
		// kernel refuses a gid mapping unless setgroups is first disabled for
		// this process, to prevent an unprivileged user from using a
		// permissive gid_map to gain membership in groups they don't belong
		// to on the host.
		GidMappingsEnableSetgroups: false,
	}

	if err := cmd.Start(); err != nil {
		return 125, fmt.Errorf("exec: failed to start namespaced process: %w", err)
	}

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

	waitErr := cmd.Wait()
	signal.Stop(sigChan)
	close(sigChan)

	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 1, waitErr
	}
	return 0, nil
}

// discoverSourceRoot walks up from cwd looking for a directory containing
// .janusfs.yml, and refuses rather than defaulting to cwd when
// it finds neither — defaulting would provision an unpoliced view over
// whatever directory happens to be current (a user's entire home directory,
// in the worst case), which is the opposite of what this tool exists to
// prevent. Unlike the darwin path, this never talks to a daemon: on Linux
// `janusfs exec` needs no daemon and must work with none running.
func discoverSourceRoot(cwd string) (string, error) {
	curr := cwd
	for {
		currAbs, err := filepath.Abs(curr)
		if err != nil {
			break
		}
		if _, err := os.Stat(filepath.Join(currAbs, ".janusfs.yml")); err == nil {
			return currAbs, nil
		}
		parent := filepath.Dir(currAbs)
		if parent == currAbs {
			break
		}
		curr = parent
	}
	return "", fmt.Errorf("exec: no JanusFS policy found for %s — refusing to mount an unpoliced tree\nRemedy: run `janusfs init` here, or in the project root above this directory", cwd)
}
