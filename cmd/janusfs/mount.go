package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

// shutdownGrace is the FR-2 "≤ 5 s, then force" drain window for a clean
// unmount.
const shutdownGrace = 5 * time.Second

// errDaemonNotRunning is returned by daemonCall when no daemon is listening
// on the control socket, so commands can print a clear "start the daemon"
// hint instead of a raw dial error.
var errDaemonNotRunning = errors.New("no janusfs daemon is running")

func newMountCmd() *cobra.Command {
	var name string
	var noHistory bool

	cmd := &cobra.Command{
		Use:   "mount <src> [mountpoint]",
		Short: "Ask the daemon to mount a sanitized view of <src> (returns immediately)",
		Long: "Hands a mount to the running janusfs daemon and returns; the daemon owns the\n" +
			"mount and keeps it alive across reboots (via `janusfs daemon`) until you run\n" +
			"`janusfs umount`. Start the daemon first with `janusfs daemon`.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := daemonRequest{Cmd: "mount", Src: args[0], Name: name, NoHistory: noHistory}
			if len(args) == 2 {
				req.Mountpoint = args[1]
			}
			return runMount(req)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "leaf name for the derived mountpoint (default: mirror the source's full path)")
	cmd.Flags().BoolVar(&noHistory, "no-history", false, "disable history persistence for this mount")
	return cmd
}

func runMount(req daemonRequest) error {
	resp, err := daemonCall(req)
	if errors.Is(err, errDaemonNotRunning) {
		return fmt.Errorf("mount: no janusfs daemon is running; start it first with: janusfs daemon")
	}
	if err != nil {
		return fmt.Errorf("mount: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("mount: %s", resp.Error)
	}
	fmt.Println(resp.Message)
	for _, m := range resp.Mounts {
		fmt.Printf("  Dashboard: %s\n", m.Dashboard)
	}
	return nil
}

// historyDBPath returns the history SQLite database path for src, under
// ~/.janusfs/history/<basename>-<hash>.db. The hash of the absolute source
// path keeps two same-basename sources (e.g. two dirs named "app") from
// sharing — and corrupting — one DB file now that the daemon holds many
// mounts at once.
func historyDBPath(src string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		abs = src
	}
	sum := sha256.Sum256([]byte(abs))
	name := filepath.Base(abs) + "-" + hex.EncodeToString(sum[:])[:12] + ".db"
	return filepath.Join(home, ".janusfs", "history", name)
}

// logLevel maps the --debug flag to an slog level.
func logLevel(debug bool) slog.Level {
	if debug {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

// logWriter adapts an *slog.Logger to io.Writer so stdlib *log.Logger
// consumers (hanwen/go-fuse's fs.Options logger) route through the single
// configured slog destination (SPEC §15/§21) instead of a bare file
// descriptor.
type logWriter struct {
	logger *slog.Logger
	level  slog.Level
}

func (w logWriter) Write(p []byte) (int, error) {
	w.logger.Log(nil, w.level, string(p))
	return len(p), nil
}

var _ io.Writer = logWriter{}
