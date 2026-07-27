package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/sarathsp06/janusfs/internal/identity"
)

func newExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec -- <command> [args...]",
		Short: "Execute an untrusted agent command inside a policy-enforced sandbox",
		Long: "Prepares a policy-enforced scope for the target project (current directory),\n" +
			"registers the newly spawned agent process identity with the running JanusFS daemon,\n" +
			"and executes the command directly under the security policy.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExec(cmd.Context(), args)
		},
	}
	return cmd
}

func runExec(ctx context.Context, args []string) error {
	src, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("resolving project root: %w", err)
	}

	// 1. Contact the daemon to ensure the project is mounted and get FUSE mountpoint
	resp, err := daemonCall(daemonRequest{Cmd: "mount", Src: src})
	if err != nil {
		return fmt.Errorf("janusfs exec requires a running daemon: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("ensuring project mount: %s", resp.Error)
	}
	if len(resp.Mounts) == 0 {
		return fmt.Errorf("failed to obtain project mountpoint from daemon")
	}
	mountpoint := resp.Mounts[0].Mountpoint

	// 2. Fetch or compute our child's process identity details
	pid := os.Getpid()
	startTime, err := identity.GetProcessStartTime(pid)
	if err != nil {
		return fmt.Errorf("fetching process start time: %w", err)
	}
	ppidChain, err := identity.GetPPIDChain(pid)
	if err != nil {
		return fmt.Errorf("generating PPID chain hash: %w", err)
	}
	bootUUID, err := identity.GetBootUUID()
	if err != nil {
		return fmt.Errorf("fetching boot UUID: %w", err)
	}

	// Register current PID with daemon
	regResp, err := daemonCall(daemonRequest{
		Cmd:            "register_agent",
		AgentPID:       pid,
		AgentStartTime: startTime,
		AgentPPIDHash:  ppidChain,
		AgentBootUUID:  bootUUID,
	})
	if err != nil {
		return fmt.Errorf("registering process with daemon: %w", err)
	}
	if !regResp.OK {
		return fmt.Errorf("registration rejected by daemon: %s", regResp.Error)
	}

	// 3. Platform-specific execution
	if runtime.GOOS == "linux" {
		if os.Getenv("JANUSFS_IN_NS") == "1" {
			// Inside the namespace!
			// Write uid_map and gid_map (this maps 0 to our actual user)
			uid := os.Getenv("JANUSFS_ORIG_UID")
			gid := os.Getenv("JANUSFS_ORIG_GID")
			if uid != "" && gid != "" {
				_ = os.WriteFile("/proc/self/uid_map", []byte(fmt.Sprintf("0 %s 1\n", uid)), 0644)
				// Prior to writing gid_map, write "deny" to setgroups as required for unprivileged user namespaces.
				_ = os.WriteFile("/proc/self/setgroups", []byte("deny"), 0644)
				_ = os.WriteFile("/proc/self/gid_map", []byte(fmt.Sprintf("0 %s 1\n", gid)), 0644)
			}
			// Perform bind mount of mountpoint over src
			err := syscall.Mount(mountpoint, src, "", syscall.MS_BIND, "")
			if err != nil {
				return fmt.Errorf("performing namespace bind mount %s -> %s: %w", mountpoint, src, err)
			}
			// Exec the final command!
			execPath, err := exec.LookPath(args[0])
			if err != nil {
				return err
			}
			return syscall.Exec(execPath, args, os.Environ())
		} else {
			// Outside the namespace: spawn ourselves inside a new mount/user namespace!
			cmdSelf := exec.Command(os.Args[0], os.Args[1:]...)
			cmdSelf.Env = append(os.Environ(),
				"JANUSFS_IN_NS=1",
				fmt.Sprintf("JANUSFS_ORIG_UID=%d", os.Getuid()),
				fmt.Sprintf("JANUSFS_ORIG_GID=%d", os.Getgid()),
			)
			cmdSelf.Stdin = os.Stdin
			cmdSelf.Stdout = os.Stdout
			cmdSelf.Stderr = os.Stderr
			cmdSelf.SysProcAttr = &syscall.SysProcAttr{
				Cloneflags: syscall.CLONE_NEWNS | syscall.CLONE_NEWUSER,
			}
			// Wait for the sandboxed process to finish
			err := cmdSelf.Run()
			if err != nil {
				if exitError, ok := err.(*exec.ExitError); ok {
					os.Exit(exitError.ExitCode())
				}
				return err
			}
			return nil
		}
	} else {
		// On macOS: execute in the FUSE mountpoint directly
		err := os.Chdir(mountpoint)
		if err != nil {
			return fmt.Errorf("chdir to FUSE mountpoint %s: %w", mountpoint, err)
		}
		execPath, err := exec.LookPath(args[0])
		if err != nil {
			return err
		}
		return syscall.Exec(execPath, args, os.Environ())
	}
}
