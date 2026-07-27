// Package control is the one home for the daemon control-socket protocol: the
// request/response wire types and the client-side dial-and-call helper. Both
// cmd/janusfs (the daemon itself, and every short-lived CLI client) and
// internal/execrunner (janusfs exec, which talks to the daemon without going
// through cmd/janusfs's own command plumbing) need this protocol, and it used
// to be declared twice — once unexported in package main, once duplicated in
// execrunner — which let the two definitions silently drift apart. This
// package is the single source of truth both sides now import.
package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// ErrDaemonNotRunning is returned by Call when no daemon is listening on the
// control socket, so callers can react: a command needing the daemon can print
// a start hint, janusfs umount can fall back to a direct OS-level unmount.
var ErrDaemonNotRunning = errors.New("no janusfs daemon is running")

// Request is one command sent by a `janusfs mount`/`umount`/`daemon status`
// client, or by `janusfs exec`, over the control socket. One JSON object per
// connection.
type Request struct {
	Cmd        string `json:"cmd"`                  // "mount" | "unmount" | "list" | "reload"
	Src        string `json:"src,omitempty"`        // mount: source tree
	Mountpoint string `json:"mountpoint,omitempty"` // unmount: mountpoint or src; reload: any path inside either tree
	Label      string `json:"label,omitempty"`      // mount: friendly dashboard name (not a path)
	NoHistory  bool   `json:"no_history,omitempty"`

	// Resume is daemon-internal only and never crosses the socket: it lets the
	// daemon's own startup recreate a recorded mountpoint directory, which a
	// fresh client-initiated mount request must not be able to trigger. json:"-"
	// is a security property here, not a serialization convenience — a remote
	// client encoding Resume:true into a request must have no way to make it
	// take effect.
	Resume bool `json:"-"`
}

// MountStatus describes one active or newly-created mount, returned in a
// Response's Mounts field.
type MountStatus struct {
	Src        string `json:"src"`
	Label      string `json:"label,omitempty"`
	Mountpoint string `json:"mountpoint"`
	Dashboard  string `json:"dashboard"`
}

// Response is the daemon's reply to one Request.
type Response struct {
	OK      bool          `json:"ok"`
	Error   string        `json:"error,omitempty"`
	Message string        `json:"message,omitempty"`
	Mounts  []MountStatus `json:"mounts,omitempty"`
}

// SocketPath is the daemon control socket, ~/.janusfs/daemon.sock.
func SocketPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("control: resolving home directory: %w", err)
	}
	return filepath.Join(home, ".janusfs", "daemon.sock"), nil
}

// Call dials the daemon control socket, sends one request, and returns the
// response. Returns an error wrapping ErrDaemonNotRunning if no daemon is
// listening, so callers can offer a clear next step.
func Call(req Request) (Response, error) {
	sock, err := SocketPath()
	if err != nil {
		return Response{}, err
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return Response{}, ErrDaemonNotRunning
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

// WriteResponse encodes resp to conn, defaulting OK to true when Error is
// empty so a handler that returns a bare Response{} is reported as success.
func WriteResponse(conn net.Conn, resp Response) {
	resp.OK = resp.OK || resp.Error == ""
	_ = json.NewEncoder(conn).Encode(resp)
}
