package execrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type daemonRequest struct {
	Cmd        string `json:"cmd"`
	Src        string `json:"src,omitempty"`
	Mountpoint string `json:"mountpoint,omitempty"`
	Label      string `json:"label,omitempty"`
	NoHistory  bool   `json:"no_history,omitempty"`
}

type mountStatus struct {
	Src        string `json:"src"`
	Label      string `json:"label,omitempty"`
	Mountpoint string `json:"mountpoint"`
	Dashboard  string `json:"dashboard"`
}

type daemonResponse struct {
	OK      bool          `json:"ok"`
	Error   string        `json:"error,omitempty"`
	Message string        `json:"message,omitempty"`
	Mounts  []mountStatus `json:"mounts,omitempty"`
}

func callDaemon(req daemonRequest) (daemonResponse, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return daemonResponse{}, err
	}
	sock := filepath.Join(home, ".janusfs", "daemon.sock")
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return daemonResponse{}, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return daemonResponse{}, err
	}
	var resp daemonResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return daemonResponse{}, err
	}
	return resp, nil
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

		// Check for .janusignore or .janusmask
		if foundSrc == "" {
			if _, err := os.Stat(filepath.Join(currAbs, ".janusignore")); err == nil {
				foundSrc = currAbs
			} else if _, err := os.Stat(filepath.Join(currAbs, ".janusmask")); err == nil {
				foundSrc = currAbs
			}
		}

		parent := filepath.Dir(currAbs)
		if parent == currAbs {
			break
		}
		curr = parent
	}

	// If no source is found, default to cwd
	if foundSrc == "" {
		foundSrc = cwd
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

func Run(ctx context.Context, targetArgs []string) (int, error) {
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

	// Streaming outputs
	stdoutRewriter := NewStreamRewriter(os.Stdout, mountpoint, src)
	stderrRewriter := NewStreamRewriter(os.Stderr, mountpoint, src)

	// Determine the relative path for Dir if we chroot
	relDir := "/"
	if realCWD, err := filepath.Abs(cwd); err == nil {
		if srcAbs, err1 := filepath.Abs(src); err1 == nil {
			if realCWD != srcAbs && strings.HasPrefix(realCWD, srcAbs+string(filepath.Separator)) {
				if rel, err := filepath.Rel(srcAbs, realCWD); err == nil {
					relDir = "/" + filepath.ToSlash(rel)
				}
			}
		}
	}

	attempts := []struct {
		name        string
		sysProcAttr *syscall.SysProcAttr
		dir         string
	}{
		{
			name:        "Mount namespaces + Chroot",
			sysProcAttr: getSysProcAttr(mountpoint, true),
			dir:         relDir,
		},
		{
			name:        "Mount namespaces",
			sysProcAttr: getSysProcAttr(mountpoint, false),
			dir:         hijackedCWD,
		},
		{
			name:        "Standard fallback",
			sysProcAttr: nil,
			dir:         hijackedCWD,
		},
	}

	var cmd *exec.Cmd
	var startErr error

	for _, attempt := range attempts {
		// skip non-fallback attempts on non-Linux platforms (where getSysProcAttr returns nil)
		if attempt.sysProcAttr == nil && attempt.name != "Standard fallback" {
			continue
		}

		cmd = exec.CommandContext(ctx, finalArgs[0], finalArgs[1:]...)
		cmd.Dir = attempt.dir
		cmd.Env = scrubbedEnv
		cmd.Stdin = os.Stdin
		cmd.Stdout = stdoutRewriter
		cmd.Stderr = stderrRewriter
		cmd.SysProcAttr = attempt.sysProcAttr

		startErr = cmd.Start()
		if startErr == nil {
			break
		}
	}

	if startErr != nil {
		return 125, fmt.Errorf("exec: failed to start process: %w", startErr)
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

	// Flush streaming rewriters
	_ = stdoutRewriter.Close()
	_ = stderrRewriter.Close()

	if waitErr != nil {
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			return exitError.ExitCode(), nil
		}
		return 1, waitErr
	}

	return 0, nil
}
