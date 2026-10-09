// Package ipc is terminatr's local transport: the server's socket, the
// per-session mod sockets, the peer at the other end, and dialing an
// agent's own socket (Claude's messaging socket).
//
// AF_UNIX sockets on every OS, Windows included (10 1803 and later), so
// Claude's mod reaches the same paths there: its fetch only takes a unix
// socket path, never a named pipe. On Windows, Listen restricts the
// socket file with a DACL rather than a mode, and PeerOf gets the pid
// from SIO_AF_UNIX_GETPEERPID and compares the token's user SID.
package ipc

import (
	"fmt"
	"path/filepath"
)

// Addr is a local address: a socket path.
type Addr string

// MaxPath keeps a margin under the sun_path limit (104 bytes on macOS,
// 108 on Linux and Windows, all including the trailing NUL).
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
