//go:build linux

// On Linux, janusfs exec gives the child process tree a filtered view of the
// project at the project's OWN absolute path, using a private mount
// namespace. Every process outside the namespace keeps reading the real
// filesystem directly, at native speed, and never enters FUSE. This is the
// only platform janusfs exec supports; runner_other.go refuses everywhere
// else.
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
	"strings"
	"syscall"

	"github.com/sarathsp06/janusfs/internal/nsexec"
)

// Run is the Linux entry point for `janusfs exec [--net=none] -- <command>
// [args...]`. plan comes from Discover, which the CLI has already run so it
// could resolve the network mode and show the banner's facts.
func Run(ctx context.Context, plan Plan, targetArgs []string, opts Options) (int, error) {
	if len(targetArgs) == 0 {
		return 125, errors.New("exec: no command specified to execute")
	}
	if plan.Src == "" {
		return 125, errors.New("exec: no source root discovered")
	}

	if err := nsexec.Supported(); err != nil {
		return 125, fmt.Errorf("exec: %w", err)
	}

	cwd := plan.Cwd
	src := plan.Src

	// Launched from inside another session? Read before the scrub below
	// removes the marker. Allowed, and said so in the banner.
	opts.Nested = os.Getenv("JANUSFS_EXEC") == "1"

	// The banner precedes any warning so the warning reads in context ("the
	// boundary is up; here is the one thing it cannot protect you from").
	if !opts.Quiet {
		WriteBanner(os.Stderr, plan, opts)
	}
	warnGitStagingHazards(src)

	self, err := os.Executable()
	if err != nil {
		return 125, fmt.Errorf("exec: resolving own executable path: %w", err)
	}

	// Scrub JANUSFS_* from the child's environment: the child must not be
	// able to read or influence JanusFS configuration.
	env := os.Environ()
	scrubbedEnv := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "JANUSFS_") {
			scrubbedEnv = append(scrubbedEnv, kv)
		}
	}

	nsArgs := []string{"__nsmount", "--src", src}
	if opts.DenyNetwork {
		nsArgs = append(nsArgs, "--deny-network")
	}
	if opts.Quiet {
		nsArgs = append(nsArgs, "--quiet")
	}
	nsArgs = append(nsArgs, "--")
	nsArgs = append(nsArgs, targetArgs...)
	cmd := exec.CommandContext(ctx, self, nsArgs...)
	cmd.Dir = cwd // no CWD hijack needed: the same path is valid on both sides
	cmd.Env = scrubbedEnv
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout // no stream rewriting needed: paths already match
	cmd.Stderr = os.Stderr
	cloneFlags := syscall.CLONE_NEWNS | syscall.CLONE_NEWUSER
	if opts.DenyNetwork {
		// A fresh network namespace starts with only a (down) loopback
		// interface and no route to anything else, so the child cannot reach
		// any external host. Stage 2 brings loopback up. Creating it here, in
		// the same clone that already makes the user namespace, means an
		// unprivileged caller can request it: inside the new user namespace
		// the process is uid 0 and owns the netns, so no host privilege is
		// needed.
		cloneFlags |= syscall.CLONE_NEWNET
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(cloneFlags),
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
