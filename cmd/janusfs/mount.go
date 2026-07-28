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

	"github.com/sarathsp06/janusfs/internal/control"
)

// shutdownGrace is how long a clean unmount is given before it is forced.
var shutdownGrace = 5 * time.Second

// forceUnmountSettle is the short wait after the force-unmount fallback before
// shutdown gives up on the serve loop.
var forceUnmountSettle = 2 * time.Second

func newMountCmd() *cobra.Command {
	var name string
	var noHistory bool

	cmd := &cobra.Command{
		Use:   "mount <src> [mountpoint]",
		Short: "Ask the daemon to mount a sanitized view of <src> (returns immediately)",
		Long: "Hands a mount to the running janusfs daemon and returns; the daemon owns the\n" +
			"mount and keeps it alive across reboots (via `janusfs daemon`) until you run\n" +
			"`janusfs umount`. With no [mountpoint], the mountpoint mirrors <src>'s full\n" +
			"path under the mount root; pass an empty [mountpoint] to mount at a short path\n" +
			"of your choosing. Start the daemon first with `janusfs daemon`.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := daemonRequest{Cmd: "mount", Src: args[0], Label: name, NoHistory: noHistory}
			if len(args) == 2 {
				req.Mountpoint = args[1]
			}
			return runMount(req)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "friendly label for this mount, shown in the dashboard (does not change the path)")
	cmd.Flags().BoolVar(&noHistory, "no-history", false, "disable history persistence for this mount")
	return cmd
}

func newUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update [src|mountpoint|configpath]",
		Short: "Reload .janusignore/.janusmask rules for a mount (or all mounts) without remounting",
		Long: "Recompiles the rule set from disk and clears the redaction cache so edits to\n" +
			".janusignore/.janusmask take effect. The argument may be a mount's source path,\n" +
			"its mountpoint, or any file inside either tree (e.g. the config file you just\n" +
			"edited); the daemon resolves it to the owning mount. With no argument, reloads\n" +
			"every mount.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := daemonRequest{Cmd: "reload"}
			if len(args) == 1 {
				if abs, err := filepath.Abs(args[0]); err == nil {
					req.Mountpoint = abs
				} else {
					req.Mountpoint = args[0]
				}
			}
			resp, err := callDaemon("update", req)
			if err != nil {
				return err
			}
			fmt.Println(resp.Message)
			return nil
		},
	}
}

// callDaemon sends req to the daemon and normalizes the three failure shapes
// every client command shares: a not-running daemon becomes the start hint, a
// transport error is wrapped, and a daemon-reported !OK becomes its message —
// each prefixed with name. On success it returns the response for the caller to
// render. umount deliberately does NOT use this: its not-running branch falls
// back to a real OS-level unmount rather than printing a hint.
func callDaemon(name string, req daemonRequest) (daemonResponse, error) {
	resp, err := control.Call(req)
	if errors.Is(err, errDaemonNotRunning) {
		return resp, fmt.Errorf("%s: %s", name, hintStartDaemon)
	}
	if err != nil {
		return resp, fmt.Errorf("%s: %w", name, err)
	}
	if !resp.OK {
		return resp, fmt.Errorf("%s: %s", name, resp.Error)
	}
	return resp, nil
}

func runMount(req daemonRequest) error {
	resp, err := callDaemon("mount", req)
	if err != nil {
		return err
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
// configured slog destination instead of a bare file
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
