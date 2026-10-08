// Package ipc is terminatr's local transport: the server's socket, the
// per-session mod sockets, the peer at the other end, and dialing an
// agent's own socket (Claude's messaging socket).
//
// This file set is Unix only (AF_UNIX sockets). The Windows port adds
// files that use named pipes (go-winio) behind this API: an Addr is then
// a pipe name, and PeerOf compares the client's token SID.
package ipc

import (
	"fmt"
	"path/filepath"
)

// Addr is a local address: a socket path on Unix.
type Addr string

// MaxPath keeps a margin under the sun_path limit (104 bytes on macOS,
// 108 on Linux, both including the trailing NUL).
const MaxPath = 100

// ServerName is the server socket's name in its run directory.
const ServerName = "tm.sock"

// Fits reports whether a socket in dir named name fits the sun_path
// budget.
func Fits(dir, name string) bool {
	return len(filepath.Join(dir, name)) <= MaxPath
}

// ServerAddr is the server's socket: override when set, else tm.sock in
// runDir. It fails when the path is over the sun_path budget.
func ServerAddr(runDir, override string) (Addr, error) {
	p := override
	if p == "" {
		p = filepath.Join(runDir, ServerName)
	}
	if len(p) > MaxPath {
		return "", fmt.Errorf("socket path %q is %d bytes; unix sockets allow at most %d here", p, len(p), MaxPath)
	}
	return Addr(p), nil
}

// SessionAddr is the address of a per-session socket named name in the
// session's runtime directory.
func SessionAddr(runtimeDir, name string) Addr {
	return Addr(filepath.Join(runtimeDir, name))
}

// Peer is the process at the other end of a connection.
type Peer struct {
	PID int
	// SameUser: the peer runs as this process's user.
	SameUser bool
}
