//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"github.com/sarathsp06/janusfs/internal/config"
	"github.com/sarathsp06/janusfs/internal/engine"
	"github.com/sarathsp06/janusfs/internal/logging"
	"github.com/sarathsp06/janusfs/internal/mount"
	"github.com/sarathsp06/janusfs/internal/provider"
)

// registerPlatformCommands adds the Linux-only __nsmount hidden command
// (Stage 2 of the namespace-exec path) to root. The darwin build has its own
// no-op implementation (platform_other.go) since __nsmount does not exist
// there.
func registerPlatformCommands(root *cobra.Command) {
	root.AddCommand(newNSMountCmd())
}

// newNSMountCmd is Stage 2 of the Linux namespace-exec path (see
// internal/execrunner/runner_linux.go's package doc for Stage 1). It runs
// ONLY inside the fresh mount+user namespace runner_linux.go's re-exec
// created: mounts the filtered view over the source path, runs the target
// command, and tears down on exit.
//
// Never invoked directly by a user — cobra's "--" convention is used instead
// of DisableFlagParsing (unlike the darwin `exec` command) purely because
// this command has no argument-forwarding ambiguity to resolve: the launcher
// always calls it as `__nsmount --src <path> -- <command> [args...]`.
func newNSMountCmd() *cobra.Command {
	var src string
	cmd := &cobra.Command{
		Use:    "__nsmount --src <path> -- <command> [args...]",
		Hidden: true, // Stage 2 of janusfs exec; never invoked by hand
		RunE: func(cmd *cobra.Command, args []string) error {
			exitCode, err := runNSMount(cmd.Context(), src, args)
			if err != nil {
				fmt.Fprintln(os.Stderr, err.Error())
			}
			os.Exit(exitCode)
			return nil
		},
	}
	cmd.Flags().StringVar(&src, "src", "", "source tree to mount over itself inside this namespace (required)")
	return cmd
}

// runNSMount performs, in order, the three things that must happen exactly
// once and in this sequence inside the fresh namespace: make the mount tree
// private, establish the filtered view, then run the target command.
func runNSMount(ctx context.Context, src string, targetArgs []string) (int, error) {
	if src == "" {
		return 125, errors.New("nsmount: --src is required")
	}
	if len(targetArgs) == 0 {
		return 125, errors.New("nsmount: no command specified to execute")
	}

	logger := logging.New("nsmount")

	// Step 1, and it MUST be first: make the whole mount tree recursively
	// private. A new mount namespace inherits the parent's mounts as SHARED on
	// most distributions, so without this, every mount made below — the
	// shadow bind mount and the FUSE mount alike — propagates back out to the
	// host mount table, silently defeating the entire isolation model (the
	// host would see the FUSE mount appear over the user's real project
	// directory). This is the single most damaging thing that can go wrong
	// here, and it fails silently if skipped.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return 125, fmt.Errorf("nsmount: making the mount tree recursively private: %w", err)
	}

	// Step 2: bind-mount src to a private shadow path BEFORE the FUSE server
	// mounts over src itself, and back the adapter by the shadow path instead
	// of src.
	//
	// Why: the adapter's backing file access is still path-based (the
	// descriptor-relative backing layer doesn't exist yet). If it read
	// through `src` after `src` becomes the FUSE mountpoint, every backing
	// read the server itself performs would re-enter the very mount it is
	// trying to serve — the kernel has no notion of "this call is from the
	// server, let it through" — recursing until the FUSE worker pool is
	// exhausted or the kernel deadlocks. A bind mount is the same content at
	// a different, independent VFS path, so backing reads through the shadow
	// path never touch wherever the FUSE mount ends up living, regardless of
	// what sits there.
	//
	// The bind mount is private to this namespace (step 1 already applied)
	// and needs no explicit cleanup: the whole namespace, and everything
	// mounted within it, is torn down by the kernel the moment this process
	// (and any children sharing it) exits.
	shadow, err := os.MkdirTemp("", "janusfs-nsshadow-")
	if err != nil {
		return 125, fmt.Errorf("nsmount: creating shadow mount point: %w", err)
	}
	if err := unix.Mount(src, shadow, "", unix.MS_BIND, ""); err != nil {
		return 125, fmt.Errorf("nsmount: bind-mounting %s to a private shadow path: %w", src, err)
	}

	base := config.Default()
	if err := config.ApplyFile(&base); err != nil {
		return 125, fmt.Errorf("nsmount: %w", err)
	}
	if err := config.ApplyEnv(&base); err != nil {
		return 125, fmt.Errorf("nsmount: %w", err)
	}

	eng, err := engine.New(shadow)
	if err != nil {
		return 125, fmt.Errorf("nsmount: compiling rule set: %w", err)
	}
	prov := provider.NewRamCache(base.CacheMaxBytes, base.CacheMaxFile, base.RedactBufferMax)

	errLog := log.New(nsmountLogWriter{logger, slog.LevelError}, "", 0)

	mountCtx, cancelMount := context.WithCancel(ctx)
	defer cancelMount()

	adapter := &mount.Adapter{
		Engine:      eng,
		Provider:    prov,
		ErrorLogger: errLog,
		Reload: func() error {
			if err := eng.Reload(shadow); err != nil {
				return err
			}
			prov.InvalidateAll()
			return nil
		},
	}
	ready := make(chan error, 1)
	adapter.OnMounted = func() {
		logger.Info("mounted", "src", src, "shadow", shadow)
		ready <- nil
	}

	mountDone := make(chan error, 1)
	go func() {
		err := adapter.Mount(mountCtx, shadow, src)
		select {
		case ready <- err:
		default:
		}
		mountDone <- err
	}()

	if err := <-ready; err != nil {
		return 125, fmt.Errorf("nsmount: mounting %s: %w", src, err)
	}

	// cwd was already set correctly by Stage 1's cmd.Dir (applied before this
	// process's execve, while the mount table was still an unmodified private
	// copy of the parent's) — read it back rather than re-deriving it, so the
	// target command starts in the same place the user invoked `janusfs exec`
	// from.
	cwd, err := os.Getwd()
	if err != nil {
		return 125, fmt.Errorf("nsmount: failed to get current working directory: %w", err)
	}

	targetCmd := exec.CommandContext(ctx, targetArgs[0], targetArgs[1:]...)
	targetCmd.Dir = cwd
	targetCmd.Env = os.Environ() // already scrubbed of JANUSFS_* by Stage 1
	targetCmd.Stdin = os.Stdin
	targetCmd.Stdout = os.Stdout
	targetCmd.Stderr = os.Stderr

	if err := targetCmd.Start(); err != nil {
		cancelMount()
		return 125, fmt.Errorf("nsmount: failed to start target process: %w", err)
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
				if targetCmd.Process != nil {
					_ = targetCmd.Process.Signal(sig)
				}
			case <-ctx.Done():
				if targetCmd.Process != nil {
					_ = targetCmd.Process.Signal(syscall.SIGTERM)
				}
				return
			}
		}
	}()

	waitErr := targetCmd.Wait()
	signal.Stop(sigChan)
	close(sigChan)

	// Unmount before exiting so adapter.Mount's goroutine returns cleanly;
	// this also flushes the FUSE server's own teardown path. Best-effort:
	// this whole namespace (and its mounts) is reclaimed by the kernel the
	// instant this process exits regardless.
	cancelMount()
	<-mountDone

	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 1, waitErr
	}
	return 0, nil
}

// nsmountLogWriter adapts an *slog.Logger to io.Writer for go-fuse's
// stdlib *log.Logger diagnostic sink, matching the convention used by the
// daemon-owned mount path (cmd/janusfs/mount.go's logWriter).
type nsmountLogWriter struct {
	logger *slog.Logger
	level  slog.Level
}

func (w nsmountLogWriter) Write(p []byte) (int, error) {
	w.logger.Log(nil, w.level, string(p))
	return len(p), nil
}
